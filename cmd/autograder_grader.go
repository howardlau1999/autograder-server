package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"autograder-server/pkg/grader"
	grader_pb "autograder-server/pkg/grader/proto"
	"autograder-server/pkg/logging"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/storage"
	"github.com/avast/retry-go"
	"github.com/docker/docker/pkg/stdcopy"
	grpc_opentracing "github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const graderInitialConfig = `
[grader]
	concurrency=5
	tags="docker,x64"
	heartbeat-interval="10s"

[numa]
	memset=""
	cpuset=""

[hub]
	address="localhost:9999"
	token=""

[fs.local]
	dir="grader"

[fs.http]
	url="http://localhost:19999"
	token=""
	timeout="10s"

[metrics]
	enabled=true
	port=39999
	path="/metrics"

[log]
	development=false
	level="info"
	file="grader.log"
`

type GraderEnvKeyReplacer struct {
}

func (r *GraderEnvKeyReplacer) Replace(s string) string {
	v := strings.ReplaceAll(s, ".", "_")
	return strings.ReplaceAll(v, "-", "_")
}

type SubmissionContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type LogStreamContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type GraderWorker struct {
	runningSubs       map[uint64]*grader_pb.GradeRequest
	cancelChs         map[uint64]*SubmissionContext
	containerIds      map[uint64]string
	logStreams        map[uint64]map[string]*LogStreamContext
	mu                *sync.Mutex
	dockerGrader      *grader.DockerProgrammingGrader
	graderId          uint64
	basePath          string
	hubAddress        string
	token             string
	ls                *storage.LocalStorage
	sfs               *storage.SimpleHTTPFS
	heartbeatInterval time.Duration
	httpTimeout       time.Duration
	rpcTimeout        time.Duration
	containerWaiters  map[string]map[string]chan bool
	containerStarted  map[string]bool
}

type ReportBuffer struct {
	mu     *sync.Mutex
	cond   *sync.Cond
	buffer []*grader_pb.GradeReport
	closed bool
}

func graderReadConfig() {
	*viper.GetViper() = *viper.NewWithOptions(viper.EnvKeyReplacer(&GraderEnvKeyReplacer{}))
	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath("/etc/autograder-grader/")  // path to look for the config file in
	viper.AddConfigPath("$HOME/.autograder-grader") // call multiple times to add many search paths
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	viper.SetDefault("hub.address", "localhost:9999")
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	viper.SetDefault("grader.hostname", hostname)
	viper.SetDefault("grader.concurrency", 5)
	viper.SetDefault("grader.heartbeat-interval", "10s")
	viper.SetDefault("grader.tags", "docker,x64")
	viper.SetDefault("fs.http.url", "http://localhost:19999")
	viper.SetDefault("fs.http.timeout", "1m")
	viper.SetDefault("fs.local.dir", "grader")
	viper.SetDefault("metrics.port", "39999")
	viper.SetDefault("metrics.enabled", true)
	viper.SetDefault("metrics.path", "/metrics")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.file", "grader.log")
	viper.SetDefault("log.development", "false")

	err = viper.ReadInConfig()
	if err != nil {
		zap.L().Error("ReadConfig", zap.Error(err))
	}
}

func NewReportBuffer() *ReportBuffer {
	b := &ReportBuffer{
		mu:     &sync.Mutex{},
		buffer: nil,
		closed: false,
	}
	b.cond = sync.NewCond(b.mu)
	go func() {
		for {
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				return
			}
			b.buffer = append(b.buffer, &grader_pb.GradeReport{})
			b.mu.Unlock()
			b.cond.Broadcast()
			time.Sleep(5 * time.Second)
		}
	}()
	return b
}

func (b *ReportBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.cond.Broadcast()
}

func (b *ReportBuffer) Send(report *grader_pb.GradeReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		zap.L().Warn("ReportBuffer.SendAfterClosed", zap.Stringer("report", report))
		return
	}
	b.buffer = append(b.buffer, report)
	b.cond.Broadcast()
}

func (g *GraderWorker) uploadFile(ctx context.Context, filePath string) error {
	local, err := g.ls.Open(ctx, filePath)
	if err != nil {
		return err
	}
	defer local.Close()
	return g.sfs.Put(ctx, filePath, local)
}

func (g *GraderWorker) downloadFile(ctx context.Context, filePath string) error {
	data, err := g.sfs.Get(ctx, filePath)
	if err != nil {
		return err
	}
	defer data.Close()
	return g.ls.Put(ctx, filePath, data)
}

func (g *GraderWorker) getMetadataKey(submissionId uint64) []byte {
	return []byte(fmt.Sprintf("docker:metadata:%d", submissionId))
}

func (g *GraderWorker) sendReports(
	rpCli grader_pb.GraderHubService_GradeCallbackClient,
	client grader_pb.GraderHubServiceClient,
	submissionId uint64, reports *[]*grader_pb.GradeReport,
	logger *zap.Logger,
) error {
	var i int
	var err error
	defer logger.Debug("Grader.SendReports.Exit", zap.Uint64("submissionId", submissionId))
	for i = 0; i < len(*reports); i++ {
		report := (*reports)[i]
		logger.Debug("Grader.SendReport", zap.Stringer("brief", report.GetBrief()))
		if report.GetDockerMetadata() != nil {
			g.mu.Lock()
			g.containerIds[report.GetDockerMetadata().GetSubmissionId()] = report.GetDockerMetadata().GetContainerId()
			g.mu.Unlock()

			if report.GetDockerMetadata().GetStarted() {
				g.mu.Lock()
				g.containerStarted[report.GetDockerMetadata().GetContainerId()] = true
				waiters := g.containerWaiters[report.GetDockerMetadata().GetContainerId()]
				delete(g.containerWaiters, report.GetDockerMetadata().GetContainerId())
				g.mu.Unlock()
				for _, ch := range waiters {
					ch <- true
					close(ch)
				}
			}

			value, err := proto.Marshal(report.GetDockerMetadata())
			if err != nil {
				logger.Error("Grader.MarshalMetadata", zap.Error(err))
				continue
			}
			_, err = client.PutMetadata(
				context.Background(), &grader_pb.PutMetadataRequest{
					Key:      g.getMetadataKey(submissionId),
					Value:    value,
					GraderId: g.graderId,
				},
			)
			if err != nil {
				logger.Error("Grader.PutMetadata", zap.Error(err))
			}
			continue
		}
		submissionStatus := report.GetBrief().GetStatus()
		gradeResponse := &grader_pb.GradeResponse{SubmissionId: submissionId, Report: report}
		err := rpCli.Send(gradeResponse)
		if err != nil {
			logger.Error("Grader.GradeCallback.Send", zap.Error(err))
			goto Out
		}
		err = rpCli.RecvMsg(gradeResponse)
		if err != nil {
			logger.Error("Grader.GradeCallback.Recv", zap.Error(err))
			goto Out
		}
		if submissionStatus == model_pb.SubmissionStatus_Finished {
			testcases := report.GetReport().GetTests()
			wg := &sync.WaitGroup{}
			for _, testcase := range testcases {
				if testcase.GetOutputPath() == "" {
					continue
				}
				wg.Add(1)
				go func(outputPath string) {
					logger.Debug("OutputFile.Upload", zap.String("outputPath", outputPath))
					err := retry.Do(
						func() error {
							ctx, cancel := context.WithTimeout(context.Background(), g.httpTimeout)
							defer cancel()
							return g.uploadFile(
								ctx,
								outputPath,
							)
						}, retry.RetryIf(
							func(err error) bool {
								return os.IsTimeout(err)
							},
						),
						retry.Attempts(3),
					)
					wg.Done()
					if err != nil {
						logger.Error("OutputFile.Upload", zap.String("outputPath", outputPath), zap.Error(err))
					}
					_ = g.ls.Delete(context.Background(), outputPath)
				}(path.Join(fmt.Sprintf("runs/submissions/%d/results/outputs", submissionId), testcase.GetOutputPath()))
			}
			wg.Wait()
		}
		if submissionStatus == model_pb.SubmissionStatus_Finished ||
			submissionStatus == model_pb.SubmissionStatus_Cancelled ||
			submissionStatus == model_pb.SubmissionStatus_Failed {
			_, err := client.PutMetadata(
				context.Background(),
				&grader_pb.PutMetadataRequest{GraderId: g.graderId, Key: g.getMetadataKey(submissionId)},
			)
			if err != nil {
				logger.Error("Grader.DeleteMetadata", zap.Error(err))
			}
		}
	}
Out:
	if i < len(*reports) {
		logger.Error("Grader.SendReports.Remain", zap.Int("count", len(*reports)-i))
		*reports = (*reports)[i:]
	} else {
		*reports = nil
	}
	return err
}

func (g *GraderWorker) submissionReporter(submissionId uint64, buffer *ReportBuffer) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId))
	var reports []*grader_pb.GradeReport
	logger.Debug("Grader.GradeCallbackEnter")
	defer logger.Debug("Grader.GradeCallbackExit")
	defer func(runPath string) {
		logger.Debug("Grader.FS.Remove", zap.String("file", runPath))
		err := g.ls.Delete(context.Background(), runPath)
		if err != nil {
			logger.Error("Grader.FS.Remove", zap.String("file", runPath), zap.Error(err))
		}
	}(fmt.Sprintf("runs/submissions/%d", submissionId))
	for !buffer.closed || len(reports) > 0 {
		conn, client := g.getNewClient()
		ctx := context.Background()
		ctx = metadata.AppendToOutgoingContext(
			ctx, "submissionId", strconv.Itoa(int(submissionId)), "graderId",
			strconv.Itoa(int(g.graderId)),
		)
		rpCli, err := client.GradeCallback(ctx)
		if err != nil {
			conn.Close()
			logger.Error("Grader.StartGradeCallback", zap.Error(err))
			if buffer.closed {
				return
			}
			time.Sleep(1 * time.Second)
			continue
		}
		err = g.sendReports(rpCli, client, submissionId, &reports, logger)
		if err != nil {
			time.Sleep(1 * time.Second)
			err := rpCli.CloseSend()
			if err != nil {
				logger.Error("Grader.CloseGradeCallback", zap.Error(err))
			}
			conn.Close()
			continue
		}
		for {
			buffer.mu.Lock()
			for len(buffer.buffer) == 0 && !buffer.closed {
				buffer.cond.Wait()
			}
			if buffer.closed && len(buffer.buffer) == 0 {
				buffer.mu.Unlock()
				rpCli.CloseAndRecv()
				logger.Debug("Grader.SubmissionReporter.BufferClosed", zap.Uint64("submissionId", submissionId))
				break
			}
			reports = append(reports, buffer.buffer...)
			buffer.buffer = nil
			buffer.mu.Unlock()
			err = g.sendReports(rpCli, client, submissionId, &reports, logger)
			if err != nil {
				err := rpCli.CloseSend()
				if err != nil {
					logger.Error("Grader.CloseGradeCallback", zap.Error(err))
				}
				conn.Close()
				time.Sleep(1 * time.Second)
				break
			}
		}
		conn.Close()
	}
}

const ErrCheckFileExists = -101
const ErrDownloadFile = -102

func (g *GraderWorker) onSubmissionFinished(submissionId uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ctx := g.cancelChs[submissionId]
	if ctx != nil {
		ctx.cancel()
	}
	delete(g.cancelChs, submissionId)
	for _, ch := range g.containerWaiters[g.containerIds[submissionId]] {
		close(ch)
	}
	delete(g.containerWaiters, g.containerIds[submissionId])
	g.containerIds[submissionId] = ""
	delete(g.containerIds, submissionId)
	for _, c := range g.logStreams[submissionId] {
		c.cancel()
	}
	delete(g.logStreams, submissionId)
}

func (g *GraderWorker) streamLog(submissionId uint64, requestId string) {
	logger := zap.L().With(
		zap.Uint64("submissionId", submissionId), zap.String("requestId", requestId),
	)
	logger.Debug("StreamLog.Start")
	defer logger.Debug("StreamLog.Exit")
	var subCtx *SubmissionContext
	var containerId string
	var ok bool
	g.mu.Lock()
	subCtx, ok = g.cancelChs[submissionId]
	containerId = g.containerIds[submissionId]
	g.mu.Unlock()
	parentCtx := context.Background()
	if subCtx != nil {
		parentCtx = subCtx.ctx
	}
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(
		ctx, "submissionId", strconv.Itoa(int(submissionId)), "requestId", requestId, "graderId",
		strconv.Itoa(int(g.graderId)),
	)
	g.mu.Lock()
	if g.logStreams[submissionId] == nil {
		g.logStreams[submissionId] = map[string]*LogStreamContext{}
	}
	g.logStreams[submissionId][requestId] = &LogStreamContext{ctx: ctx, cancel: cancel}
	g.mu.Unlock()
	conn, client := g.getNewClient()
	defer conn.Close()
	slCli, err := client.StreamLogCallback(ctx)
	if err != nil {
		logger.Error("StreamLog.Callback", zap.Error(err))
		return
	}
	defer func() {
		err = slCli.CloseSend()
		if err != nil {
			logger.Error("StreamLog.Close", zap.Error(err))
		}
	}()
	if !ok {
		logger.Debug("StreamLog.SubmissionNotFound")
		return
	}
	// Wait For Container Start
	// TODO find a better way
	ticker := time.NewTicker(1 * time.Second)
	seconds := 0
	for {
		if containerId != "" {
			break
		}
		slCli.Send(
			&grader_pb.StreamLogResponse{
				Data: []byte(fmt.Sprintf(
					"creating container...(%ds)\r",
					seconds,
				)),
			},
		)
		select {
		case <-ticker.C:
			g.mu.Lock()
			containerId = g.containerIds[submissionId]
			g.mu.Unlock()
			seconds++
		case <-ctx.Done():
			return
		}
	}

	var r io.ReadCloser
	g.mu.Lock()
	if _, found := g.containerStarted[containerId]; !found {
		slCli.Send(&grader_pb.StreamLogResponse{Data: []byte("starting container...\n")})
		ch := make(chan bool)
		if g.containerWaiters[containerId] == nil {
			g.containerWaiters[containerId] = map[string]chan bool{}
		}
		g.containerWaiters[containerId][requestId] = ch
		g.mu.Unlock()
		started := <-ch
		if !started {
			return
		}
		logger.Debug("StreamLog.ContainerStart", zap.String("containerId", containerId))
	} else {
		g.mu.Unlock()
	}
	r, err = g.dockerGrader.StreamLog(ctx, containerId)
	if err != nil {
		logger.Error("StreamLog.Docker.ContainerLogs", zap.Error(err))
		return
	}
	defer r.Close()
	go func() {
		<-ctx.Done()
		r.Close()
	}()
	logBuf := make([]byte, 32*1024, 32*1024)
	pr, pw := io.Pipe()
	go func() {
		stdcopy.StdCopy(pw, pw, r)
		pw.Close()
	}()
	for {
		n, err := pr.Read(logBuf)
		if err != nil {
			logger.Error("StreamLog.Read", zap.Error(err))
			break
		}
		err = slCli.Send(&grader_pb.StreamLogResponse{Data: logBuf[:n]})
		if err != nil {
			logger.Error("StreamLog.Send", zap.Error(err))
			break
		}
	}
}

func (g *GraderWorker) gradeOneSubmission(
	req *grader_pb.GradeRequest,
) {
	ctx, cancel := context.WithCancel(context.Background())
	notifyC := make(chan *grader_pb.GradeReport)
	buffer := NewReportBuffer()
	g.mu.Lock()
	g.cancelChs[req.SubmissionId] = &SubmissionContext{
		ctx: ctx, cancel: cancel,
	}
	g.mu.Unlock()
	logger := zap.L().With(zap.Uint64("submissionId", req.GetSubmissionId()))
	logger.Debug("Grader.GradeOneSubmission.Start")
	defer logger.Debug("Grader.GradeOneSubmission.Exit")
	go g.submissionReporter(req.SubmissionId, buffer)

	// Download files
	filesWg := &sync.WaitGroup{}
	for _, file := range req.Submission.Files {
		notExists, err := g.ls.NotExists(ctx, path.Join(req.Submission.Path, file))
		if err != nil {
			buffer.Send(
				&grader_pb.GradeReport{
					Brief: &model_pb.SubmissionBriefReport{
						Status:        model_pb.SubmissionStatus_Failed,
						InternalError: ErrCheckFileExists,
					},
					Report: &model_pb.SubmissionReport{InternalError: ErrCheckFileExists},
				},
			)
			g.onSubmissionFinished(req.SubmissionId)
			buffer.Close()
			break
		}
		if !notExists {
			continue
		}
		filesWg.Add(1)
		go func(file string) {
			err := retry.Do(
				func() error {
					httpCtx, cancel := context.WithTimeout(ctx, g.httpTimeout)
					defer cancel()
					return g.downloadFile(httpCtx, file)
				}, retry.RetryIf(
					func(err error) bool {
						return os.IsTimeout(err)
					},
				),
				retry.Attempts(3),
			)
			logger.Debug("Grader.HTTPFS.Download", zap.String("file", file))
			if err != nil {
				logger.Error("Grader.HTTPFS.Download", zap.String("file", file), zap.Error(err))
				buffer.Send(
					&grader_pb.GradeReport{
						Brief: &model_pb.SubmissionBriefReport{
							Status:        model_pb.SubmissionStatus_Failed,
							InternalError: ErrDownloadFile,
						},
						Report: &model_pb.SubmissionReport{InternalError: ErrDownloadFile},
					},
				)
				g.onSubmissionFinished(req.SubmissionId)
				buffer.Close()
			}
			filesWg.Done()
		}(path.Join(req.Submission.Path, file))
	}
	filesWg.Wait()

	defer func(file string) {
		logger.Debug("Grader.FS.Remove", zap.String("file", file))
		err := g.ls.Delete(context.Background(), file)
		if err != nil {
			logger.Error("Grader.FS.Remove", zap.String("file", file), zap.Error(err))
		}
	}(path.Join(req.Submission.Path))

	if ctx.Err() != nil {
		g.onSubmissionFinished(req.SubmissionId)
		return
	}

	go g.dockerGrader.GradeSubmission(
		grader.SetGraderLogger(ctx, logger),
		g.basePath,
		req.GetSubmissionId(),
		req.GetSubmission(),
		req.GetConfig(),
		viper.GetString("numa.memset"),
		viper.GetString("numa.cpuset"),
		notifyC,
	)
	for r := range notifyC {
		logger.Debug(
			"Grader.ProgressReport", zap.Stringer("brief", r.Brief), zap.Stringer("metadata", r.DockerMetadata),
		)
		buffer.Send(r)
	}
	logger.Debug("Grader.ProgressReport.Close")
	g.onSubmissionFinished(req.SubmissionId)
	buffer.Close()
}

func (g *GraderWorker) getNewClient() (*grpc.ClientConn, grader_pb.GraderHubServiceClient) {
	keep := keepalive.ClientParameters{PermitWithoutStream: true, Time: 5 * time.Second, Timeout: 1 * time.Hour}
	conn, err := grpc.Dial(
		g.hubAddress,
		grpc.WithWriteBufferSize(0),
		grpc.WithKeepaliveParams(keep),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(
			grpc_opentracing.UnaryClientInterceptor(),
			grpc_prometheus.UnaryClientInterceptor,
		),
		grpc.WithChainStreamInterceptor(
			grpc_opentracing.StreamClientInterceptor(),
			grpc_prometheus.StreamClientInterceptor,
		),
	)
	if err != nil {
		zap.L().Error("Hub.Dial", zap.Error(err))
		return nil, nil
	}
	client := grader_pb.NewGraderHubServiceClient(conn)
	return conn, client
}

func (g *GraderWorker) WorkLoop() {
	var graderId uint64
	concurrency := uint64(viper.GetUint("grader.concurrency"))
	g.dockerGrader = grader.NewDockerProgrammingGrader(int(concurrency))
	tags := strings.Split(viper.GetString("grader.tags"), ",")
	for i := 0; i < len(tags); i++ {
		tags[i] = strings.TrimSpace(tags[i])
	}
	registerRequest := &grader_pb.RegisterGraderRequest{
		Token: g.token,
		Info: &model_pb.GraderInfo{
			Hostname:    viper.GetString("grader.hostname"),
			Tags:        tags,
			Concurrency: concurrency,
		},
	}
	zap.L().Info("Grader.RegisterRequest", zap.Stringer("request", registerRequest))
	conn, client := g.getNewClient()
	defer conn.Close()
	for {
		resp, err := client.RegisterGrader(context.Background(), registerRequest)
		if err != nil {
			if status.Code(err) == codes.AlreadyExists {
				zap.L().Fatal("Grader.Register.NameAlreadyExists", zap.Error(err))
				return
			}
			zap.L().Error("Grader.Register", zap.Error(err))
			time.Sleep(3 * time.Second)
			continue
		}
		graderId = resp.GetGraderId()
		g.graderId = graderId
		logger := zap.L().With(zap.Uint64("graderId", graderId))
		logger.Info("Grader.Registered")
		ctx, cancel := context.WithCancel(context.Background())
		metadatas, err := client.GetAllMetadata(ctx, &grader_pb.GetAllMetadataRequest{GraderId: graderId})
		cancel()
		if err != nil {
			logger.Error("Grader.GetPreviousMetadata", zap.Error(err))
			time.Sleep(3 * time.Second)
			continue
		}
		wg := &sync.WaitGroup{}
		for i := 0; i < len(metadatas.Keys); i++ {
			key, value := metadatas.Keys[i], metadatas.Values[i]
			metadataPB := &grader_pb.DockerGraderMetadata{}
			err := proto.Unmarshal(value, metadataPB)
			if err != nil {
				logger.Error("Grader.UnmarshalMetadata", zap.ByteString("key", key), zap.Error(err))
				continue
			}
			submissionId, containerId := metadataPB.SubmissionId, metadataPB.ContainerId
			l := logger.With(zap.Uint64("submissionId", submissionId), zap.String("containerId", containerId))
			l.Debug("Grader.RunningSubmission.Found")
			// Stop all remaining containers
			// These submissions have already been rescheduled
			wg.Add(1)
			go func(l *zap.Logger, containerId string, metadataKey []byte) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				// TODO check error
				_ = g.dockerGrader.RemoveContainer(ctx, l, containerId)
				_, _ = client.PutMetadata(ctx, &grader_pb.PutMetadataRequest{GraderId: graderId, Key: metadataKey})
				wg.Done()
			}(l, metadataPB.ContainerId, key)
		}
		wg.Wait()
		for {
			quit := false
			hbCtx, hbCancel := context.WithCancel(context.Background())
			hbCtx = metadata.AppendToOutgoingContext(hbCtx, "graderId", fmt.Sprintf("%d", graderId))
			hbCli, err := client.GraderHeartbeat(hbCtx)
			if err != nil {
				logger.Error("Grader.StartHeartbeat", zap.Error(err))
				time.Sleep(1 * time.Second)
				continue
			}
			// Heartbeat
			go func() {
				logger.Debug("Grader.StartHeartbeat")
				timer := time.NewTimer(g.heartbeatInterval)
				for {
					logger.Debug("Grader.Heartbeat")
					err := hbCli.Send(&grader_pb.GraderHeartbeatRequest{Time: timestamppb.Now(), GraderId: graderId})
					if err != nil {
						logger.Error("Grader.Heartbeat", zap.Error(err))
						hbCancel()
						timer.Stop()
						return
					}
					select {
					case <-timer.C:
						timer.Reset(g.heartbeatInterval)
					case <-hbCtx.Done():
						timer.Stop()
						return
					}
				}
			}()

			// Receive Grade Request
			for {
				request, err := hbCli.Recv()
				if err != nil {
					quit = true
					logger.Error("Grader.Recv", zap.Error(err))
					hbCancel()
					break
				}
				gradeReqs := request.GetRequests()
				for _, req := range gradeReqs {
					logger.Debug("Grader.Recv", zap.Stringer("request", req))
					if req.IsStreamLog {
						if req.IsCancel {
							g.mu.Lock()
							ctx := g.logStreams[req.SubmissionId][req.RequestId]
							if ctx != nil {
								ctx.cancel()
							}
							delete(g.logStreams[req.SubmissionId], req.RequestId)
							g.mu.Unlock()
						} else {
							go g.streamLog(req.SubmissionId, req.RequestId)
						}
					} else {
						if req.IsCancel {
							logger.Warn("Grader.CancelGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
							g.mu.Lock()
							ctx := g.cancelChs[req.SubmissionId]
							if ctx != nil {
								ctx.cancel()
							} else {
								logger.Warn(
									"Grader.CancelGrade.NotFound", zap.Uint64("submissionId", req.GetSubmissionId()),
								)
							}
							delete(g.cancelChs, req.SubmissionId)
							g.mu.Unlock()
						} else {
							logger.Debug("Grader.BeginGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
							go g.gradeOneSubmission(req)
						}
					}
				}
			}
			if quit {
				time.Sleep(1 * time.Second)
				break
			}
		}
	}
}

func graderProcessCommandLineOptions() bool {

	var printTemplate bool
	pflag.BoolVar(&printTemplate, "config", false, "Pass this flag to print config template.")
	pflag.Parse()
	if printTemplate {
		fmt.Print(graderInitialConfig)
		return true
	}
	return false
}

func main() {
	if graderProcessCommandLineOptions() {
		return
	}
	graderReadConfig()
	zapLogger := logging.Init(
		viper.GetString("log.level"), viper.GetString("log.file"), viper.GetBool("log.development"),
	)
	defer zapLogger.Sync()
	heartbeatInterval, err := time.ParseDuration(viper.GetString("grader.heartbeat-interval"))
	if err != nil {
		zapLogger.Fatal("Grader.HeartbeatInterval.Invalid", zap.Error(err))
	}
	basePath := viper.GetString("fs.local.dir")
	cwd, err := os.Getwd()
	if err != nil {
		zap.L().Fatal("OS.Getwd", zap.Error(err))
	}
	basePath = filepath.Join(cwd, basePath)
	worker := &GraderWorker{
		runningSubs:       map[uint64]*grader_pb.GradeRequest{},
		cancelChs:         map[uint64]*SubmissionContext{},
		mu:                &sync.Mutex{},
		basePath:          basePath,
		hubAddress:        viper.GetString("hub.address"),
		token:             viper.GetString("hub.token"),
		ls:                storage.NewLocalStorage(basePath),
		sfs:               storage.NewSimpleHTTPFS(viper.GetString("fs.http.url"), viper.GetString("fs.http.token")),
		containerIds:      map[uint64]string{},
		logStreams:        map[uint64]map[string]*LogStreamContext{},
		containerWaiters:  map[string]map[string]chan bool{},
		containerStarted:  map[string]bool{},
		heartbeatInterval: heartbeatInterval,
	}
	worker.httpTimeout, err = time.ParseDuration(viper.GetString("fs.http.timeout"))
	if err != nil {
		zapLogger.Fatal("HTTP.Timeout.Invalid", zap.Error(err))
	}
	worker.rpcTimeout = 10 * time.Second

	if viper.GetBool("metrics.enabled") {
		metricsPort := viper.GetInt("metrics.port")
		zapLogger.Info("Metrics.Listen", zap.Int("port", metricsPort))
		mux := http.NewServeMux()
		mux.Handle(viper.GetString("metrics.path"), promhttp.Handler())
		go func() {
			if err := http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), mux); err != nil {
				zapLogger.Error("Metrics.Serve", zap.Error(err))
			}
		}()
	}

	worker.WorkLoop()
}
