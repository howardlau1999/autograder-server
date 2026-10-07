package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	cancelChs    map[uint64]*SubmissionContext
	containerIds map[uint64]string
	logStreams   map[uint64]map[string]*LogStreamContext
	mu           *sync.Mutex
	dockerGrader *grader.DockerProgrammingGrader
	// identity is written once per (re-)registration in WorkLoop and read by
	// every reporter/log-stream goroutine, hence the atomic pointer.
	identity          atomic.Pointer[graderIdentity]
	basePath          string
	hubAddress        string
	token             string
	ls                *storage.LocalStorage
	sfs               *storage.SimpleHTTPFS
	heartbeatInterval time.Duration
	httpTimeout       time.Duration
	containerWaiters  map[string]map[string]chan bool
	containerStarted  map[string]bool
}

// graderIdentity is the id and session secret the hub issued at registration.
type graderIdentity struct {
	graderId     uint64
	sessionToken string
}

func (g *GraderWorker) setIdentity(graderId uint64, sessionToken string) {
	g.identity.Store(&graderIdentity{graderId: graderId, sessionToken: sessionToken})
}

func (g *GraderWorker) currentIdentity() graderIdentity {
	if id := g.identity.Load(); id != nil {
		return *id
	}
	return graderIdentity{}
}

func (g *GraderWorker) graderId() uint64 {
	return g.currentIdentity().graderId
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

// reportKeepaliveInterval bounds how long the submission reporter waits for a
// real report before probing the hub stream with an empty one.
const reportKeepaliveInterval = 5 * time.Second

func NewReportBuffer() *ReportBuffer {
	b := &ReportBuffer{
		mu:     &sync.Mutex{},
		buffer: nil,
		closed: false,
	}
	b.cond = sync.NewCond(b.mu)
	return b
}

func (b *ReportBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.cond.Broadcast()
}

func (b *ReportBuffer) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// waitAndDrain blocks until at least one report is buffered, the buffer is
// closed, or timeout elapses. open=false means the buffer is closed and empty;
// timedOut=true means there is nothing to send yet but the caller should probe
// the stream.
func (b *ReportBuffer) waitAndDrain(timeout time.Duration) (
	reports []*grader_pb.GradeReport, open bool, timedOut bool,
) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buffer) == 0 && !b.closed {
		timer := time.AfterFunc(timeout, func() {
			b.mu.Lock()
			b.cond.Broadcast()
			b.mu.Unlock()
		})
		defer timer.Stop()
		deadline := time.Now().Add(timeout)
		for len(b.buffer) == 0 && !b.closed && time.Now().Before(deadline) {
			b.cond.Wait()
		}
	}
	if len(b.buffer) == 0 {
		if b.closed {
			return nil, false, false
		}
		return nil, true, true
	}
	reports = b.buffer
	b.buffer = nil
	return reports, true, false
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

// sendReports forwards the pending reports to the hub in order. On the first
// failure the unsent tail is kept in *reports so the caller can reconnect and
// resume; on success *reports is emptied.
func (g *GraderWorker) sendReports(
	rpCli grader_pb.GraderHubService_GradeCallbackClient,
	client grader_pb.GraderHubServiceClient,
	submissionId uint64, reports *[]*grader_pb.GradeReport,
	logger *zap.Logger,
) error {
	defer logger.Debug("Grader.SendReports.Exit", zap.Uint64("submissionId", submissionId))
	sent := 0
	for sent < len(*reports) {
		if err := g.sendReport(rpCli, client, submissionId, (*reports)[sent], logger); err != nil {
			logger.Error("Grader.SendReports.Remain", zap.Int("count", len(*reports)-sent), zap.Error(err))
			*reports = (*reports)[sent:]
			return err
		}
		sent++
	}
	*reports = nil
	return nil
}

// recordDockerMetadata remembers the container backing a submission, wakes any
// log streams waiting for it to start, and mirrors the metadata to the hub so
// an orphaned container can be cleaned up after a grader restart.
func (g *GraderWorker) recordDockerMetadata(
	client grader_pb.GraderHubServiceClient, submissionId uint64, md *grader_pb.DockerGraderMetadata, logger *zap.Logger,
) {
	g.mu.Lock()
	g.containerIds[md.GetSubmissionId()] = md.GetContainerId()
	var waiters map[string]chan bool
	if md.GetStarted() {
		g.containerStarted[md.GetContainerId()] = true
		waiters = g.containerWaiters[md.GetContainerId()]
		delete(g.containerWaiters, md.GetContainerId())
	}
	g.mu.Unlock()
	for _, ch := range waiters {
		ch <- true
		close(ch)
	}

	value, err := proto.Marshal(md)
	if err != nil {
		logger.Error("Grader.MarshalMetadata", zap.Error(err))
		return
	}
	_, err = client.PutMetadata(
		g.authOutgoing(context.Background()), &grader_pb.PutMetadataRequest{
			Key:      g.getMetadataKey(submissionId),
			Value:    value,
			GraderId: g.graderId(),
		},
	)
	if err != nil {
		logger.Error("Grader.PutMetadata", zap.Error(err))
	}
}

func (g *GraderWorker) uploadTestOutputs(submissionId uint64, report *model_pb.SubmissionReport, logger *zap.Logger) {
	wg := &sync.WaitGroup{}
	for _, testcase := range report.GetTests() {
		if testcase.GetOutputPath() == "" {
			continue
		}
		wg.Add(1)
		go func(outputPath string) {
			defer wg.Done()
			logger.Debug("OutputFile.Upload", zap.String("outputPath", outputPath))
			err := retry.Do(
				func() error {
					ctx, cancel := context.WithTimeout(context.Background(), g.httpTimeout)
					defer cancel()
					return g.uploadFile(ctx, outputPath)
				}, retry.RetryIf(
					func(err error) bool {
						return os.IsTimeout(err)
					},
				),
				retry.Attempts(3),
			)
			if err != nil {
				logger.Error("OutputFile.Upload", zap.String("outputPath", outputPath), zap.Error(err))
			}
			_ = g.ls.Delete(context.Background(), outputPath)
		}(path.Join(fmt.Sprintf("runs/submissions/%d/results/outputs", submissionId), testcase.GetOutputPath()))
	}
	wg.Wait()
}

func (g *GraderWorker) sendReport(
	rpCli grader_pb.GraderHubService_GradeCallbackClient,
	client grader_pb.GraderHubServiceClient,
	submissionId uint64, report *grader_pb.GradeReport,
	logger *zap.Logger,
) error {
	logger.Debug("Grader.SendReport", zap.Stringer("brief", report.GetBrief()))
	if report.GetDockerMetadata() != nil {
		// Metadata is bookkeeping between grader and hub, not part of the
		// report stream; a failure here must not stall the stream.
		g.recordDockerMetadata(client, submissionId, report.GetDockerMetadata(), logger)
		return nil
	}
	if err := rpCli.Send(&grader_pb.GradeResponse{SubmissionId: submissionId, Report: report}); err != nil {
		logger.Error("Grader.GradeCallback.Send", zap.Error(err))
		return err
	}
	submissionStatus := report.GetBrief().GetStatus()
	if submissionStatus == model_pb.SubmissionStatus_Finished {
		g.uploadTestOutputs(submissionId, report.GetReport(), logger)
	}
	if submissionStatus == model_pb.SubmissionStatus_Finished ||
		submissionStatus == model_pb.SubmissionStatus_Cancelled ||
		submissionStatus == model_pb.SubmissionStatus_Failed {
		_, err := client.PutMetadata(
			g.authOutgoing(context.Background()),
			&grader_pb.PutMetadataRequest{GraderId: g.graderId(), Key: g.getMetadataKey(submissionId)},
		)
		if err != nil {
			logger.Error("Grader.DeleteMetadata", zap.Error(err))
		}
	}
	return nil
}

// submissionReporter streams the buffered reports of one submission to the hub
// over a GradeCallback stream, reconnecting on transport errors until the
// buffer is closed and drained. It gives up only when the hub rejects the
// stream outright (e.g. this grader no longer owns the submission).
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
	for !buffer.isClosed() || len(reports) > 0 {
		retry, err := g.reportSession(submissionId, buffer, &reports, logger)
		if err != nil {
			logger.Error("Grader.SubmissionReporter", zap.Error(err))
		}
		if !retry {
			return
		}
		if buffer.isClosed() && len(reports) == 0 {
			return
		}
		time.Sleep(1 * time.Second)
	}
}

// reportSession runs one GradeCallback stream. It returns retry=false when the
// hub permanently refused the stream, in which case the local grading run is
// cancelled because its result can no longer be delivered.
func (g *GraderWorker) reportSession(
	submissionId uint64, buffer *ReportBuffer, reports *[]*grader_pb.GradeReport, logger *zap.Logger,
) (retry bool, err error) {
	conn, client := g.getNewClient()
	if conn == nil {
		return true, errors.New("dial hub")
	}
	defer conn.Close()
	ctx := g.authOutgoing(context.Background())
	ctx = metadata.AppendToOutgoingContext(ctx, "submissionId", strconv.FormatUint(submissionId, 10))
	rpCli, err := client.GradeCallback(ctx)
	if err != nil {
		return g.shouldRetryCallback(submissionId, err, logger), err
	}
	for {
		if err := g.sendReports(rpCli, client, submissionId, reports, logger); err != nil {
			// Send only reports the transport failure; the real status (for
			// example PermissionDenied) is what the server closed with.
			if _, recvErr := rpCli.CloseAndRecv(); recvErr != nil {
				err = recvErr
			}
			return g.shouldRetryCallback(submissionId, err, logger), err
		}
		pending, open, timedOut := buffer.waitAndDrain(reportKeepaliveInterval)
		if !open {
			if _, err := rpCli.CloseAndRecv(); err != nil && err != io.EOF {
				return g.shouldRetryCallback(submissionId, err, logger), err
			}
			logger.Debug("Grader.SubmissionReporter.BufferClosed")
			return false, nil
		}
		if timedOut {
			// Liveness probe: grading can run for minutes without producing a
			// report, and the transport keepalive timeout is far too long to
			// notice a dead hub connection in time. Send an empty report so a
			// broken stream errors out here and the caller reconnects before
			// the real report is due; the hub ignores reports with no fields.
			if err := g.sendReport(rpCli, client, submissionId, &grader_pb.GradeReport{}, logger); err != nil {
				if _, recvErr := rpCli.CloseAndRecv(); recvErr != nil {
					err = recvErr
				}
				return g.shouldRetryCallback(submissionId, err, logger), err
			}
			continue
		}
		*reports = append(*reports, pending...)
	}
}

// shouldRetryCallback decides whether a failed GradeCallback stream is worth
// re-establishing. A PermissionDenied means the hub will never accept reports
// for this submission from us again (it was reassigned or we lost our
// session), so grading is cancelled locally instead of retrying forever.
func (g *GraderWorker) shouldRetryCallback(submissionId uint64, err error, logger *zap.Logger) bool {
	switch status.Code(err) {
	case codes.PermissionDenied, codes.Unauthenticated:
		logger.Warn("Grader.GradeCallback.Rejected", zap.Error(err))
		g.mu.Lock()
		if sc := g.cancelChs[submissionId]; sc != nil {
			sc.cancel()
		}
		g.mu.Unlock()
		return false
	default:
		return true
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
	ctx = g.authOutgoing(ctx)
	ctx = metadata.AppendToOutgoingContext(
		ctx, "submissionId", strconv.Itoa(int(submissionId)), "requestId", requestId,
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
		err = slCli.Send(
			&grader_pb.StreamLogResponse{
				Data: []byte(fmt.Sprintf(
					"creating container...(%ds)\r",
					seconds,
				)),
			},
		)
		if err != nil {
			logger.Error("StreamLog.Send", zap.Error(err))
			return
		}
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
		if err = slCli.Send(&grader_pb.StreamLogResponse{Data: []byte("starting container...\n")}); err != nil {
			g.mu.Unlock()
			logger.Error("StreamLog.Send", zap.Error(err))
			return
		}
		ch := make(chan bool, 1)
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
	logBuf := make([]byte, 32*1024)
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

// authOutgoing attaches the hub token, this grader's id, and the session token
// issued at registration so the hub can authenticate the call and bind it to
// this grader's identity.
func (g *GraderWorker) authOutgoing(ctx context.Context) context.Context {
	id := g.currentIdentity()
	return metadata.AppendToOutgoingContext(
		ctx,
		"token", g.token,
		"graderid", strconv.FormatUint(id.graderId, 10),
		"session-token", id.sessionToken,
	)
}

func (g *GraderWorker) getNewClient() (*grpc.ClientConn, grader_pb.GraderHubServiceClient) {
	keep := keepalive.ClientParameters{PermitWithoutStream: true, Time: 5 * time.Second, Timeout: 1 * time.Hour}
	// NewClient does not dial eagerly; a hub that is down surfaces as an
	// Unavailable error on the first RPC, which every caller retries.
	conn, err := grpc.NewClient(
		g.hubAddress,
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
	dockerGrader, err := grader.NewDockerProgrammingGrader(int(concurrency))
	if err != nil {
		zap.L().Fatal("Docker.Client", zap.Error(err))
	}
	g.dockerGrader = dockerGrader
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
	if conn == nil {
		// Only fails on an invalid target/option, not on an unreachable hub
		// (that surfaces as an Unavailable error on the RPC and is retried).
		zap.L().Fatal("Hub.Dial", zap.String("target", g.hubAddress))
	}
	defer conn.Close()
	for {
		resp, err := client.RegisterGrader(g.authOutgoing(context.Background()), registerRequest)
		if err != nil {
			if status.Code(err) == codes.AlreadyExists {
				// Either another grader runs under the same hostname, or the hub
				// has not yet noticed that our previous connection died. The
				// latter resolves itself once the hub's heartbeat timeout fires,
				// so keep retrying instead of exiting.
				zap.L().Warn(
					"Grader.Register.NameAlreadyExists", zap.Error(err),
					zap.String("hint", "another grader may use the same hostname; retrying"),
				)
			} else {
				zap.L().Error("Grader.Register", zap.Error(err))
			}
			time.Sleep(3 * time.Second)
			continue
		}
		graderId = resp.GetGraderId()
		g.setIdentity(graderId, resp.GetSessionToken())
		logger := zap.L().With(zap.Uint64("graderId", graderId))
		logger.Info("Grader.Registered")
		ctx, cancel := context.WithCancel(context.Background())
		metadatas, err := client.GetAllMetadata(g.authOutgoing(ctx), &grader_pb.GetAllMetadataRequest{GraderId: graderId})
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
			hbCtx = g.authOutgoing(hbCtx)
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
