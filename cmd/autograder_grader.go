package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"autograder-server/pkg/grader"
	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type GraderEnvKeyReplacer struct {
}

func (r *GraderEnvKeyReplacer) Replace(s string) string {
	v := strings.ReplaceAll(s, ".", "_")
	return strings.ReplaceAll(v, "-", "_")
}

type GraderWorker struct {
	runningSubs  map[uint64]*grader_pb.GradeRequest
	cancelChs    map[uint64]context.CancelFunc
	mu           *sync.Mutex
	client       grader_pb.GraderHubServiceClient
	dockerGrader *grader.DockerProgrammingGrader
	graderId     uint64
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
	viper.SetDefault("grader.tags", "docker,x64")

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
	return b
}

func (b *ReportBuffer) Send(report *grader_pb.GradeReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffer = append(b.buffer, report)
	b.cond.Broadcast()
}

func (g *GraderWorker) getMetadataKey(submissionId uint64) []byte {
	return []byte(fmt.Sprintf("docker:metadata:%d", submissionId))
}

func (g *GraderWorker) sendReports(
	rpCli grader_pb.GraderHubService_GradeCallbackClient,
	submissionId uint64, reports *[]*grader_pb.GradeReport,
	logger *zap.Logger,
) error {
	var i int
	var err error
	for i = 0; i < len(*reports); i++ {
		report := (*reports)[i]
		if report.GetDockerMetadata() != nil {
			value, err := proto.Marshal(report.GetDockerMetadata())
			if err != nil {
				logger.Error("Grader.MarshalMetadata", zap.Error(err))
				continue
			}
			_, err = g.client.PutMetadata(
				context.Background(), &grader_pb.PutMetadataRequest{
					Key:      g.getMetadataKey(submissionId),
					Value:    value,
					GraderId: g.graderId,
				},
			)
			if err != nil {
				logger.Error("Grader.PutMetadata", zap.Error(err))
				continue
			}
			continue
		}
		if report.Report == nil && report.Brief == nil {
			continue
		}
		submissionStatus := report.GetBrief().GetStatus()
		err := rpCli.Send(&grader_pb.GradeResponse{SubmissionId: submissionId, Report: report})
		if err != nil {
			logger.Error("Grader.GradeCallback.Send", zap.Error(err))
			goto Out
		}
		if submissionStatus == model_pb.SubmissionStatus_Finished || submissionStatus == model_pb.SubmissionStatus_Cancelled || submissionStatus == model_pb.SubmissionStatus_Failed {
			_, err := g.client.PutMetadata(
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
		*reports = (*reports)[i:]
	}
	return err
}

func (g *GraderWorker) submissionReporter(submissionId uint64, buffer *ReportBuffer) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId))
	var reports []*grader_pb.GradeReport
	logger.Debug("Grader.GradeCallbackEnter")
	defer logger.Debug("Grader.GradeCallbackExit")
	for !buffer.closed || len(reports) > 0 {
		rpCli, err := g.client.GradeCallback(context.Background())
		if err != nil {
			logger.Error("Grader.StartGradeCallback", zap.Error(err))
			if buffer.closed {
				return
			}
			time.Sleep(1 * time.Second)
			continue
		}
		err = g.sendReports(rpCli, submissionId, &reports, logger)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		for {
			buffer.mu.Lock()
			for len(buffer.buffer) == 0 && !buffer.closed {
				buffer.cond.Wait()
			}
			if buffer.closed && len(buffer.buffer) == 0 {
				err := rpCli.CloseSend()
				if err != nil {
					logger.Error("Grader.CloseGradeCallback", zap.Error(err))
				}
				buffer.mu.Unlock()
				return
			}
			reports = append(reports, buffer.buffer...)
			buffer.buffer = nil
			buffer.mu.Unlock()
			err = g.sendReports(rpCli, submissionId, &reports, logger)
			if err != nil {
				time.Sleep(1 * time.Second)
				break
			}
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
	g.cancelChs[req.SubmissionId] = cancel
	g.mu.Unlock()
	logger := zap.L().With(zap.Uint64("submissionId", req.GetSubmissionId()))
	go g.submissionReporter(req.SubmissionId, buffer)
	go g.dockerGrader.GradeSubmission(
		grader.SetGraderLogger(ctx, logger),
		req.GetSubmissionId(),
		req.GetSubmission(),
		req.GetConfig(),
		notifyC,
	)
	for r := range notifyC {
		logger.Debug(
			"Grader.ProgressReport", zap.Stringer("brief", r.Brief), zap.Stringer("metadata", r.DockerMetadata),
		)
		buffer.Send(r)
	}
	cancel()
	buffer.mu.Lock()
	buffer.closed = true
	buffer.mu.Unlock()
	buffer.cond.Signal()
	g.mu.Lock()
	delete(g.cancelChs, req.SubmissionId)
	g.mu.Unlock()
}

func (g *GraderWorker) WorkLoop() {
	var graderId uint64
	conn, err := grpc.Dial(viper.GetString("hub.address"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	client := grader_pb.NewGraderHubServiceClient(conn)
	concurrency := uint64(viper.GetUint("grader.concurrency"))
	g.dockerGrader = grader.NewDockerProgrammingGrader(int(concurrency))
	registerRequest := &grader_pb.RegisterGraderRequest{
		Token: "",
		Info: &model_pb.GraderInfo{
			Hostname:    viper.GetString("grader.hostname"),
			Tags:        strings.Split(viper.GetString("grader.tags"), ","),
			Concurrency: concurrency,
		},
	}
	zap.L().Info("Grader.RegisterRequest", zap.Stringer("request", registerRequest))
	g.client = client
	for {
		resp, err := client.RegisterGrader(context.Background(), registerRequest)
		if err != nil {
			if status.Code(err) == codes.AlreadyExists {
				zap.L().Error("Grader.Register.NameAlreadyExists", zap.Error(err))
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
				_, _ = g.client.PutMetadata(ctx, &grader_pb.PutMetadataRequest{GraderId: graderId, Key: metadataKey})
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
				timer := time.NewTimer(10 * time.Second)
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
						timer.Reset(10 * time.Second)
					case <-hbCtx.Done():
						timer.Stop()
						return
					}
				}
			}()

			// Receive Grade Request
			for {
				request, err := hbCli.Recv()
				logger.Debug("Grader.Recv")
				if err != nil {
					quit = true
					logger.Error("Grader.Recv", zap.Error(err))
					hbCancel()
					break
				}
				gradeReqs := request.GetRequests()
				for _, req := range gradeReqs {
					if req.IsCancel {
						logger.Warn("Grader.CancelGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
						g.mu.Lock()
						cancel := g.cancelChs[req.SubmissionId]
						if cancel != nil {
							cancel()
						}
						delete(g.cancelChs, req.SubmissionId)
						g.mu.Unlock()
					} else {
						logger.Debug("Grader.BeginGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
						go g.gradeOneSubmission(req)
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

func main() {
	l, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(l)
	graderReadConfig()
	worker := &GraderWorker{
		runningSubs: map[uint64]*grader_pb.GradeRequest{},
		cancelChs:   map[uint64]context.CancelFunc{},
		mu:          &sync.Mutex{},
	}
	worker.WorkLoop()
}
