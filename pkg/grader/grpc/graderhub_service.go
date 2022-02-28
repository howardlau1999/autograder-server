package grpc

import (
	"context"
	"fmt"
	"io"
	"sync"

	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"github.com/cockroachdb/pebble"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type GraderHubService struct {
	grader_pb.UnimplementedGraderHubServiceServer
	onlineGraders        map[uint64]*model_pb.GraderStatusMetadata
	graderRepo           repository.GraderRepository
	submissionReportRepo repository.SubmissionReportRepository
	gradeRequest         map[uint64]chan *grader_pb.GradeRequest
	submissionSubs       map[uint64][]chan *grader_pb.GradeReport
	mu                   *sync.Mutex
	onlineMu             *sync.Mutex
	subsMu               *sync.Mutex
}

func (g *GraderHubService) pickGrader(request *grader_pb.GradeRequest) uint64 {
	return 71
}

func (g *GraderHubService) SubscribeSubmission(
	submissionId uint64, notifyC chan *grader_pb.GradeReport,
) {
	if notifyC == nil {
		return
	}
	internalNotifyC := make(chan *grader_pb.GradeReport)
	g.subsMu.Lock()
	g.submissionSubs[submissionId] = append(g.submissionSubs[submissionId], internalNotifyC)
	g.subsMu.Unlock()
	for r := range internalNotifyC {
		notifyC <- r
	}
	close(notifyC)
}

func (g *GraderHubService) Grade(
	ctx context.Context, request *grader_pb.GradeRequest,
) (*grader_pb.GradeCallbackResponse, error) {
	graderId := g.pickGrader(request)
	var ch chan *grader_pb.GradeRequest
	g.mu.Lock()
	ch = g.gradeRequest[graderId]
	g.mu.Unlock()
	if ch != nil {
		ch <- request
	}
	return &grader_pb.GradeCallbackResponse{}, nil
}

func (g *GraderHubService) RegisterGrader(
	ctx context.Context, request *grader_pb.RegisterGraderRequest,
) (*grader_pb.RegisterGraderResponse, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unavailable, "GET_PEER")
	}
	ip := p.Addr.String()
	hostname := request.GetInfo().GetHostname()
	name := fmt.Sprintf("%s", hostname)
	grader, graderId, err := g.graderRepo.GetGraderByName(ctx, name)
	if err != nil && err != pebble.ErrNotFound {
		return nil, status.Error(codes.Internal, "GET_GRADER")
	}
	if err == pebble.ErrNotFound {
		metadata := &model_pb.GraderStatusMetadata{
			Status: model_pb.GraderStatusMetadata_Online,
			Info:   request.GetInfo(),
			Ip:     ip,
		}
		graderId, err := g.graderRepo.CreateGrader(ctx, name, metadata)
		if err != nil {
			return nil, status.Error(codes.Internal, "CREATE_GRADER")
		}
		g.onlineMu.Lock()
		g.onlineGraders[graderId] = metadata
		g.onlineMu.Unlock()
		return &grader_pb.RegisterGraderResponse{GraderId: graderId}, nil
	}
	grader.Status = model_pb.GraderStatusMetadata_Online
	grader.Ip = ip
	grader.Info = request.GetInfo()
	g.graderRepo.UpdateGrader(ctx, graderId, grader)
	g.onlineMu.Lock()
	g.onlineGraders[graderId] = grader
	g.onlineMu.Unlock()
	return &grader_pb.RegisterGraderResponse{GraderId: graderId}, nil
}

func (g *GraderHubService) GraderHeartbeat(server grader_pb.GraderHubService_GraderHeartbeatServer) error {
	heartbeatRecv := &grader_pb.GraderHeartbeatRequest{}
	graderId := uint64(0)
	// Write Loop
	requestLoop := func(ch chan *grader_pb.GradeRequest) {
		for r := range ch {
			zap.L().Debug("GraderHeartbeat.SendGradeRequest", zap.Stringer("request", r))
			err := server.Send(&grader_pb.GraderHeartbeatResponse{Requests: []*grader_pb.GradeRequest{r}})
			if err != nil {
				zap.L().Error("GraderHeartbeat.Send", zap.Error(err))
			}
		}
		zap.L().Info("GraderHeartbeat.RequestLoop.Exit")
	}
	// Read Loop
	for {
		err := server.RecvMsg(heartbeatRecv)
		if err != nil {
			if err != io.EOF {
				zap.L().Error("GraderHeartbeat.RecvMsg", zap.Error(err))
			}
			break
		}
		zap.L().Debug(
			"GraderHeartbeat.RecvMsg", zap.Uint64("graderId", heartbeatRecv.GraderId),
			zap.Time("time", heartbeatRecv.Time.AsTime()),
		)
		if graderId == 0 {
			graderId = heartbeatRecv.GraderId
			zap.L().Info("GraderHeartbeat.First", zap.Uint64("graderId", graderId))
			ch := make(chan *grader_pb.GradeRequest)
			g.mu.Lock()
			g.gradeRequest[heartbeatRecv.GraderId] = ch
			g.mu.Unlock()
			go requestLoop(ch)
		}
	}
	zap.L().Info("GraderHeartbeat.ReadLoop.Exit", zap.Uint64("graderId", graderId))
	if graderId != 0 {
		g.mu.Lock()
		close(g.gradeRequest[heartbeatRecv.GraderId])
		delete(g.gradeRequest, heartbeatRecv.GraderId)
		g.mu.Unlock()
	}
	return nil
}

func (g *GraderHubService) GradeCallback(server grader_pb.GraderHubService_GradeCallbackServer) error {
	r := &grader_pb.GradeResponse{}
	var submissionId uint64
	for {
		err := server.RecvMsg(r)
		if err != nil {
			break
		}
		submissionId = r.GetSubmissionId()
		report := r.GetReport()
		if report.GetBrief() != nil {
			g.submissionReportRepo.UpdateSubmissionBriefReport(context.Background(), submissionId, report.GetBrief())
		}
		if report.GetReport() != nil {
			g.submissionReportRepo.UpdateSubmissionReport(context.Background(), submissionId, report.GetReport())
		}
		var subs []chan *grader_pb.GradeReport
		g.subsMu.Lock()
		l := len(g.submissionSubs[submissionId])
		subs = make([]chan *grader_pb.GradeReport, l)
		copy(subs, g.submissionSubs[submissionId])
		g.subsMu.Unlock()
		for _, sub := range subs {
			if sub == nil {
				continue
			}
			sub <- report
		}
		if report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Failed || report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Finished {
			break
		}
	}
	g.subsMu.Lock()
	for _, sub := range g.submissionSubs[submissionId] {
		close(sub)
	}
	delete(g.submissionSubs, submissionId)
	g.subsMu.Unlock()
	return nil
}

func NewGraderHubService(db *pebble.DB, srr repository.SubmissionReportRepository) *GraderHubService {
	return &GraderHubService{
		graderRepo:           repository.NewKVGraderRepository(db),
		submissionReportRepo: srr,
		onlineMu:             &sync.Mutex{},
		mu:                   &sync.Mutex{},
		subsMu:               &sync.Mutex{},
		onlineGraders:        map[uint64]*model_pb.GraderStatusMetadata{},
		submissionSubs:       map[uint64][]chan *grader_pb.GradeReport{},
		gradeRequest:         map[uint64]chan *grader_pb.GradeRequest{},
	}
}
