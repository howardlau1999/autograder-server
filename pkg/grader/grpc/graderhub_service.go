package grpc

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"github.com/cockroachdb/pebble"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type GraderHubService struct {
	grader_pb.UnimplementedGraderHubServiceServer
	onlineGraders        map[uint64]*model_pb.GraderStatusMetadata
	graderRepo           repository.GraderRepository
	submissionReportRepo repository.SubmissionReportRepository
	gradeRequestChs      map[uint64]chan *grader_pb.GradeRequest
	submissionSubs       map[uint64][]chan *grader_pb.GradeReport
	mu                   *sync.Mutex
	onlineMu             *sync.Mutex
	subsMu               *sync.Mutex
	monitorMu            *sync.Mutex
	monitorChs           map[uint64]chan *time.Time

	reqMu      *sync.Mutex
	onlineCond *sync.Cond
	reqList    map[uint64]*grader_pb.GradeRequest
}

func (g *GraderHubService) pickGrader(request *grader_pb.GradeRequest) uint64 {
	requestTags := request.GetConfig().GetTags()
	for id, grader := range g.onlineGraders {
		submissions, _ := g.graderRepo.GetSubmissionsByGrader(context.Background(), id)
		// Check concurrency
		if uint64(len(submissions)) >= grader.Info.Concurrency {
			continue
		}
		// Check tags
		ok := true
		graderTags := grader.Info.Tags
		for _, tag := range requestTags {
			found := false
			for _, gt := range graderTags {
				if gt == tag {
					found = true
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		return id
	}
	return 0
}

func (g *GraderHubService) releaseAllSubmissions(graderId uint64) {
	submissions, _ := g.graderRepo.GetSubmissionsByGrader(context.Background(), graderId)
	for _, subId := range submissions {
		g.graderRepo.ReleaseSubmission(context.Background(), subId)
	}
	g.reqMu.Lock()
	var subIds []uint64
	for _, subId := range submissions {
		_, ok := g.reqList[subId]
		if !ok {
			continue
		}
		subIds = append(subIds, subId)
	}
	g.reqMu.Unlock()
	for _, req := range subIds {
		go g.doGrade(req)
	}
}

func (g *GraderHubService) graderMonitor(graderId uint64, alive chan *time.Time) {
	timer := time.NewTimer(10 * time.Second)
	logger := zap.L().With(zap.Uint64("graderId", graderId))
	logger.Info("Grader.Monitor.Start")
	defer logger.Info("Grader.Monitor.Exit")
	for {
		select {
		case t := <-alive:
			if !timer.Stop() {
				<-timer.C
			}
			grader, err := g.graderRepo.GetGraderById(context.Background(), graderId)
			if err != nil {
				zap.L().Error("Grader.Monitor.GetGrader", zap.Error(err))
				return
			}
			if t != nil {
				if grader.GetLastHeartbeat() != nil && t.Before(grader.GetLastHeartbeat().AsTime()) {
					logger.Warn("Grader.Monitor.ExpiredHeartbeat", zap.Time("heartbeatTs", *t))
					continue
				}
				oldStatus := grader.Status
				grader.Status = model_pb.GraderStatusMetadata_Online
				grader.LastHeartbeat = timestamppb.New(*t)
				g.onlineMu.Lock()
				g.onlineGraders[graderId] = grader
				g.onlineMu.Unlock()
				if oldStatus != grader.Status {
					g.onlineCond.Broadcast()
				}
			} else {
				logger.Warn("Grader.Monitor.GraderOffline")
				grader.Status = model_pb.GraderStatusMetadata_Offline
				g.onlineMu.Lock()
				delete(g.onlineGraders, graderId)
				g.onlineMu.Unlock()
				go g.releaseAllSubmissions(graderId)
			}
			err = g.graderRepo.UpdateGrader(context.Background(), graderId, grader)
			if err != nil {
				logger.Error("Grader.Monitor.UpdateGrader", zap.Error(err))
			}
			timer.Reset(15 * time.Second)
		case <-timer.C:
			g.onlineMu.Lock()
			delete(g.onlineGraders, graderId)
			g.onlineMu.Unlock()
			grader, err := g.graderRepo.GetGraderById(context.Background(), graderId)
			if err != nil {
				zap.L().Error("Grader.Monitor.GetGrader", zap.Error(err))
				return
			}
			err = nil
			if grader.Status == model_pb.GraderStatusMetadata_Online {
				logger.Warn("Grader.Monitor.Timeout")
				grader.Status = model_pb.GraderStatusMetadata_Unknown
				err = g.graderRepo.UpdateGrader(context.Background(), graderId, grader)
			} else if grader.Status == model_pb.GraderStatusMetadata_Unknown {
				t := time.Now()
				if t.After(grader.LastHeartbeat.AsTime().Add(30 * time.Second)) {
					logger.Error("Grader.Monitor.Timeout.Offline")
					grader.Status = model_pb.GraderStatusMetadata_Offline
				}
				err = g.graderRepo.UpdateGrader(context.Background(), graderId, grader)
				go g.releaseAllSubmissions(graderId)
			}
			if err != nil {
				logger.Error("Grader.Monitor.UpdateGrader", zap.Error(err))
			}
			timer.Reset(15 * time.Second)
		}
	}
}

func (g *GraderHubService) GetAllGraders(ctx context.Context) (*autograder_pb.GetAllGradersResponse, error) {
	ids, graders, err := g.graderRepo.GetAllGraders(ctx)
	resp := &autograder_pb.GetAllGradersResponse{}
	for i := 0; i < len(ids); i++ {
		submissions, _ := g.graderRepo.GetSubmissionsByGrader(ctx, ids[i])
		resp.Graders = append(
			resp.Graders, &autograder_pb.GetAllGradersResponse_Grader{
				GraderId:    ids[i],
				Metadata:    graders[i],
				Submissions: submissions,
			},
		)
	}
	return resp, err
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

func (g *GraderHubService) doGrade(submissionId uint64) {
	var graderId uint64
	var request *grader_pb.GradeRequest
	logger := zap.L().With(zap.Uint64("submissionId", submissionId))
	g.onlineMu.Lock()
	for {
		g.reqMu.Lock()
		request = g.reqList[submissionId]
		g.reqMu.Unlock()
		if request == nil {
			g.onlineMu.Unlock()
			g.markSubmissionCancelled(submissionId)
			return
		}
		graderId = g.pickGrader(request)
		logger.Debug("GraderHub.PickGrader", zap.Uint64("graderId", graderId))
		if graderId != 0 {
			g.onlineMu.Unlock()
			break
		}
		g.onlineCond.Wait()
	}
	var ch chan *grader_pb.GradeRequest
	g.reqMu.Lock()
	request = g.reqList[submissionId]
	if request == nil {
		g.reqMu.Unlock()
		g.markSubmissionCancelled(submissionId)
		return
	}
	g.graderRepo.ClaimSubmission(context.Background(), graderId, submissionId)
	delete(g.reqList, submissionId)
	g.reqMu.Unlock()
	g.mu.Lock()
	ch = g.gradeRequestChs[graderId]
	g.mu.Unlock()
	ch <- request
}

func (g *GraderHubService) Grade(
	ctx context.Context, request *grader_pb.GradeRequest,
) (*grader_pb.GradeCallbackResponse, error) {
	g.reqMu.Lock()
	g.reqList[request.SubmissionId] = request
	g.reqMu.Unlock()
	go g.doGrade(request.SubmissionId)
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
			LastHeartbeat: timestamppb.Now(),
			Status:        model_pb.GraderStatusMetadata_Online,
			Info:          request.GetInfo(),
			Ip:            ip,
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
	grader.LastHeartbeat = timestamppb.Now()
	g.graderRepo.UpdateGrader(ctx, graderId, grader)
	g.onlineMu.Lock()
	g.onlineGraders[graderId] = grader
	g.onlineMu.Unlock()
	return &grader_pb.RegisterGraderResponse{GraderId: graderId}, nil
}

func (g *GraderHubService) requeueGradeRequest(req *grader_pb.GradeRequest) {
	g.reqMu.Lock()
	g.reqList[req.GetSubmissionId()] = req
	g.reqMu.Unlock()
}

func (g *GraderHubService) GraderHeartbeat(server grader_pb.GraderHubService_GraderHeartbeatServer) error {
	heartbeatRecv := &grader_pb.GraderHeartbeatRequest{}
	graderId := uint64(0)
	var tCh chan *time.Time
	// Write Loop
	requestLoop := func(ch chan *grader_pb.GradeRequest) {
		for r := range ch {
			zap.L().Debug("GraderHeartbeat.SendGradeRequest", zap.Stringer("request", r))
			err := server.Send(&grader_pb.GraderHeartbeatResponse{Requests: []*grader_pb.GradeRequest{r}})
			if err != nil {
				zap.L().Error("GraderHeartbeat.Send", zap.Error(err))
				go func(req *grader_pb.GradeRequest) {
					time.Sleep(1 * time.Second)
					g.requeueGradeRequest(req)
				}(r)
				break
			}
		}
		for r := range ch {
			go func(req *grader_pb.GradeRequest) {
				time.Sleep(1 * time.Second)
				g.requeueGradeRequest(req)
			}(r)
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
			g.onlineMu.Lock()
			if _, ok := g.onlineGraders[graderId]; !ok {
				g.onlineMu.Unlock()
				return status.Error(codes.NotFound, "GRADER_NOT_REGISTERED")
			}
			g.onlineMu.Unlock()
			g.onlineCond.Broadcast()
			zap.L().Info("GraderHeartbeat.First", zap.Uint64("graderId", graderId))
			ch := make(chan *grader_pb.GradeRequest)
			g.mu.Lock()
			g.gradeRequestChs[heartbeatRecv.GraderId] = ch
			g.mu.Unlock()
			go requestLoop(ch)
			g.monitorMu.Lock()
			tCh = g.monitorChs[graderId]
			if tCh == nil {
				tCh = make(chan *time.Time)
				g.monitorChs[graderId] = tCh
				go g.graderMonitor(graderId, tCh)
			}
			g.monitorMu.Unlock()
		}
		t := heartbeatRecv.Time.AsTime()
		tCh <- &t
	}
	zap.L().Info("GraderHeartbeat.ReadLoop.Exit", zap.Uint64("graderId", graderId))
	if graderId != 0 {
		g.mu.Lock()
		close(g.gradeRequestChs[heartbeatRecv.GraderId])
		delete(g.gradeRequestChs, heartbeatRecv.GraderId)
		g.mu.Unlock()
	}
	go func() { tCh <- nil }()
	return nil
}

func (g *GraderHubService) writeAndSendBriefReport(
	ctx context.Context, submissionId uint64, brief *model_pb.SubmissionBriefReport,
) error {
	err := g.submissionReportRepo.UpdateSubmissionBriefReport(
		ctx, submissionId,
		brief,
	)
	g.sendGradeReport(submissionId, &grader_pb.GradeReport{Brief: brief})
	return err
}

func (g *GraderHubService) markSubmissionCancelled(submissionId uint64) {
	err := g.writeAndSendBriefReport(
		context.Background(), submissionId,
		&model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Cancelled},
	)
	if err != nil {
		zap.L().Error("GraderHub.MarkSubmissionCancelling", zap.Uint64("submissionId", submissionId), zap.Error(err))
	}
	g.closeAllSubmissionSubscribers(submissionId)
}

func (g *GraderHubService) markSubmissionCancelling(submissionId uint64) {
	err := g.writeAndSendBriefReport(
		context.Background(), submissionId,
		&model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Cancelling},
	)
	if err != nil {
		zap.L().Error("GraderHub.MarkSubmissionCancelling", zap.Uint64("submissionId", submissionId), zap.Error(err))
	}
}

func (g *GraderHubService) sendGradeReport(submissionId uint64, report *grader_pb.GradeReport) {
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
}

func (g *GraderHubService) closeAllSubmissionSubscribers(submissionId uint64) {
	g.subsMu.Lock()
	for _, sub := range g.submissionSubs[submissionId] {
		close(sub)
	}
	delete(g.submissionSubs, submissionId)
	g.subsMu.Unlock()
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
		logger := zap.L().With(zap.Uint64("submissionId", submissionId))
		report := r.GetReport()
		if report.GetBrief() != nil {
			err = g.submissionReportRepo.UpdateSubmissionBriefReport(
				context.Background(), submissionId, report.GetBrief(),
			)
			if err != nil {
				logger.Error("GradeCallback.UpdateBrief", zap.Error(err))
			}
		}
		if report.GetReport() != nil {
			err = g.submissionReportRepo.UpdateSubmissionReport(context.Background(), submissionId, report.GetReport())
			if err != nil {
				logger.Error("GradeCallback.UpdateReport", zap.Error(err))
			}
		}
		g.sendGradeReport(submissionId, report)
		if report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Failed ||
			report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Finished ||
			report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Cancelled {
			break
		}
	}
	g.closeAllSubmissionSubscribers(submissionId)
	g.submissionReportRepo.DeleteUnfinishedSubmission(context.Background(), submissionId)
	g.graderRepo.ReleaseSubmission(context.Background(), submissionId)
	g.onlineCond.Broadcast()
	return nil
}

func (g *GraderHubService) CancelGrade(
	ctx context.Context, submissionId uint64,
) error {
	// Queued, not running
	g.reqMu.Lock()
	if _, ok := g.reqList[submissionId]; ok {
		delete(g.reqList, submissionId)
		g.onlineCond.Broadcast()
		g.markSubmissionCancelled(submissionId)
		g.reqMu.Unlock()
		return nil
	}
	g.reqMu.Unlock()
	// Running
	graderId, err := g.graderRepo.GetGraderIdBySubmissionId(ctx, submissionId)
	if err != nil {
		return status.Error(codes.Internal, "GET_GRADER")
	}
	var ch chan *grader_pb.GradeRequest
	g.mu.Lock()
	ch = g.gradeRequestChs[graderId]
	g.mu.Unlock()
	if ch != nil {
		ch <- &grader_pb.GradeRequest{IsCancel: true, SubmissionId: submissionId}
	} else {
		g.markSubmissionCancelled(submissionId)
	}
	return nil
}

func NewGraderHubService(db *pebble.DB, srr repository.SubmissionReportRepository) *GraderHubService {
	gr := repository.NewKVGraderRepository(db)
	svc := &GraderHubService{
		graderRepo:           gr,
		submissionReportRepo: srr,
		monitorMu:            &sync.Mutex{},
		onlineMu:             &sync.Mutex{},
		mu:                   &sync.Mutex{},
		subsMu:               &sync.Mutex{},
		reqMu:                &sync.Mutex{},
		reqList:              map[uint64]*grader_pb.GradeRequest{},
		onlineGraders:        map[uint64]*model_pb.GraderStatusMetadata{},
		submissionSubs:       map[uint64][]chan *grader_pb.GradeReport{},
		gradeRequestChs:      map[uint64]chan *grader_pb.GradeRequest{},
		monitorChs:           map[uint64]chan *time.Time{},
	}
	svc.onlineCond = sync.NewCond(svc.onlineMu)
	ids, graders, err := gr.GetAllGraders(context.Background())
	if err != nil {
		panic(err)
	}
	for i := 0; i < len(ids); i++ {
		if graders[i].GetStatus() == model_pb.GraderStatusMetadata_Online {
			graders[i].Status = model_pb.GraderStatusMetadata_Unknown
			gr.UpdateGrader(context.Background(), ids[i], graders[i])
		}
		tCh := make(chan *time.Time)
		svc.monitorChs[ids[i]] = tCh
	}
	for id, tCh := range svc.monitorChs {
		go svc.graderMonitor(id, tCh)
	}
	return svc
}
