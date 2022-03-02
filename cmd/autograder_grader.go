package main

import (
	"context"
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
	"google.golang.org/grpc/credentials/insecure"
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
	dockerGrader grader.ProgrammingGrader
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

func sendReports(
	rpCli grader_pb.GraderHubService_GradeCallbackClient, submissionId uint64, reports *[]*grader_pb.GradeReport,
	logger *zap.Logger,
) error {
	var i int
	var err error
	for i = 0; i < len(*reports); i++ {
		report := (*reports)[i]
		for {
			err := rpCli.Send(&grader_pb.GradeResponse{SubmissionId: submissionId, Report: report})
			if err != nil {
				logger.Error("Grader.GradeCallback.Send", zap.Error(err))
				goto Out
			} else {
				break
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
		err = sendReports(rpCli, submissionId, &reports, logger)
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
			err = sendReports(rpCli, submissionId, &reports, logger)
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
	go g.dockerGrader.GradeSubmission(ctx, req.GetSubmissionId(), req.GetSubmission(), req.GetConfig(), notifyC)
	for r := range notifyC {
		logger.Debug("Grader.ProgressReport", zap.Stringer("brief", r.Brief))
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
	g.dockerGrader = grader.NewDockerProgrammingGrader(int(concurrency))
	for {
		resp, err := client.RegisterGrader(context.Background(), registerRequest)
		if err != nil {
			zap.L().Error("Grader.Register", zap.Error(err))
			time.Sleep(3 * time.Second)
			continue
		}
		graderId = resp.GetGraderId()
		zap.L().Info("Grader.Registered", zap.Uint64("graderId", graderId))
		for {
			quit := false
			hbCtx, hbCancel := context.WithCancel(context.Background())
			hbCli, err := client.GraderHeartbeat(hbCtx)
			if err != nil {
				zap.L().Error("Grader.StartHeartbeat", zap.Error(err))
				time.Sleep(1 * time.Second)
				continue
			}
			// Heartbeat
			go func() {
				zap.L().Debug("Grader.StartHeartbeat")
				timer := time.NewTimer(10 * time.Second)
				for {
					zap.L().Debug("Grader.Heartbeat")
					err := hbCli.Send(&grader_pb.GraderHeartbeatRequest{Time: timestamppb.Now(), GraderId: graderId})
					if err != nil {
						zap.L().Error("Grader.Heartbeat", zap.Error(err))
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
				zap.L().Debug("Grader.Recv")
				if err != nil {
					quit = true
					zap.L().Error("Grader.Recv", zap.Error(err))
					hbCancel()
					break
				}
				gradeReqs := request.GetRequests()
				for _, req := range gradeReqs {
					if req.IsCancel {
						zap.L().Warn("Grader.CancelGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
						g.mu.Lock()
						cancel := g.cancelChs[req.SubmissionId]
						if cancel != nil {
							cancel()
						}
						delete(g.cancelChs, req.SubmissionId)
						g.mu.Unlock()
					} else {
						zap.L().Debug("Grader.BeginGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
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
