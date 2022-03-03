package grpc

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"github.com/cockroachdb/pebble"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type GradeRequestQueue struct {
	mu       *sync.Mutex
	cond     *sync.Cond
	requests []*grader_pb.GradeRequest
	closed   bool
}

func (q *GradeRequestQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

func NewGradeRequestQueue() *GradeRequestQueue {
	mu := &sync.Mutex{}
	cond := sync.NewCond(mu)
	return &GradeRequestQueue{mu: mu, cond: cond}
}

type GraderHubService struct {
	grader_pb.UnimplementedGraderHubServiceServer
	graderRepo           repository.GraderRepository
	submissionReportRepo repository.SubmissionReportRepository
	gradeRequestMu       *sync.Mutex
	gradeRequestQueues   map[uint64]*GradeRequestQueue

	onlineMu      *sync.Mutex
	onlineGraders map[uint64]*model_pb.GraderStatusMetadata
	onlineCond    *sync.Cond

	submissionSubs map[uint64][]chan *grader_pb.GradeReport
	subsMu         *sync.Mutex
	monitorChs     map[uint64]chan *time.Time
	monitorMu      *sync.Mutex

	queuedMu   *sync.Mutex
	queuedList map[uint64]*grader_pb.GradeRequest

	runningMu   *sync.Mutex
	runningList map[uint64]*grader_pb.GradeRequest
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

func (g *GraderHubService) onGraderUnknown(graderId uint64) {
	g.onlineMu.Lock()
	delete(g.onlineGraders, graderId)
	g.onlineMu.Unlock()
}

func (g *GraderHubService) onGraderOffline(graderId uint64) {
	g.onlineMu.Lock()
	delete(g.onlineGraders, graderId)
	g.onlineMu.Unlock()

	g.gradeRequestMu.Lock()
	queue := g.gradeRequestQueues[graderId]
	delete(g.gradeRequestQueues, graderId)
	g.gradeRequestMu.Unlock()

	if queue != nil {
		queue.Close()
	}

	submissions, _ := g.graderRepo.GetSubmissionsByGrader(context.Background(), graderId)
	for _, subId := range submissions {
		_ = g.graderRepo.ReleaseSubmission(context.Background(), subId)
		g.runningMu.Lock()
		if req := g.runningList[subId]; req != nil {
			g.queueGradeRequest(req)
			delete(g.runningList, subId)
		}
		g.runningMu.Unlock()
	}

	if queue != nil {
		queue.mu.Lock()
		for _, req := range queue.requests {
			g.runningMu.Lock()
			delete(g.runningList, req.SubmissionId)
			g.runningMu.Unlock()
			g.queueGradeRequest(req)
		}
		queue.requests = nil
		queue.mu.Unlock()
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
			timer.Reset(15 * time.Second)
			grader, err := g.graderRepo.GetGraderById(context.Background(), graderId)
			if err != nil {
				logger.Error("Grader.Monitor.GetGrader", zap.Error(err))
				return
			}
			if t != nil {
				if grader.GetLastHeartbeat() != nil && t.Before(grader.GetLastHeartbeat().AsTime()) {
					logger.Warn("Grader.Monitor.ExpiredHeartbeat", zap.Time("heartbeatTs", *t))
					continue
				}
				grader.Status = model_pb.GraderStatusMetadata_Online
				grader.LastHeartbeat = timestamppb.New(*t)
				g.onlineMu.Lock()
				g.onlineGraders[graderId] = grader
				g.onlineMu.Unlock()
			} else {
				logger.Warn("Grader.Monitor.GraderOffline")
				grader.Status = model_pb.GraderStatusMetadata_Offline
				g.onGraderOffline(graderId)
			}
			err = g.graderRepo.UpdateGrader(context.Background(), graderId, grader)
			if err != nil {
				logger.Error("Grader.Monitor.UpdateGrader", zap.Error(err))
			}
		case t := <-timer.C:
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
				g.onGraderUnknown(graderId)
			} else if grader.Status == model_pb.GraderStatusMetadata_Unknown {
				if t.After(grader.LastHeartbeat.AsTime().Add(30 * time.Second)) {
					logger.Error("Grader.Monitor.Timeout.Offline")
					grader.Status = model_pb.GraderStatusMetadata_Offline
					g.onGraderOffline(graderId)
				}
				err = g.graderRepo.UpdateGrader(context.Background(), graderId, grader)
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

func (g *GraderHubService) onSubmissionScheduled(submissionId uint64, graderId uint64) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId), zap.Uint64("graderId", graderId))
	err := g.graderRepo.ClaimSubmission(context.Background(), graderId, submissionId)
	if err != nil {
		logger.Error("GradeHub.ClaimSubmission", zap.Error(err))
	}
}

func (g *GraderHubService) onSubmissionQueued(submissionId uint64) {
	var graderId uint64
	var request *grader_pb.GradeRequest
	logger := zap.L().With(zap.Uint64("submissionId", submissionId))
	err := g.onSubmissionBriefReportUpdate(
		context.Background(), submissionId, &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Queued},
	)
	if err != nil {
		logger.Error("GraderHub.UpdateBriefReport", zap.Error(err))
	}
	g.onlineMu.Lock()
	for {
		g.queuedMu.Lock()
		request = g.queuedList[submissionId]
		g.queuedMu.Unlock()
		if request == nil {
			g.onlineMu.Unlock()
			g.onSubmissionCancelled(submissionId)
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
	g.queuedMu.Lock()
	request = g.queuedList[submissionId]
	if request == nil {
		g.queuedMu.Unlock()
		g.onSubmissionCancelled(submissionId)
		return
	}
	var queue *GradeRequestQueue
	delete(g.queuedList, submissionId)
	g.gradeRequestMu.Lock()
	queue = g.gradeRequestQueues[graderId]
	g.gradeRequestMu.Unlock()
	if queue != nil {
		queue.mu.Lock()
		if queue.closed {
			g.queuedMu.Unlock()
			queue.mu.Unlock()
			g.onSubmissionQueued(submissionId)
			return
		}
		queue.requests = append(queue.requests, request)
		queue.cond.Signal()
		queue.mu.Unlock()
	}
	g.runningMu.Lock()
	g.runningList[submissionId] = request
	g.runningMu.Unlock()
	g.onSubmissionScheduled(submissionId, graderId)
	delete(g.queuedList, submissionId)
	g.queuedMu.Unlock()
}

func (g *GraderHubService) Grade(
	ctx context.Context, request *grader_pb.GradeRequest,
) (*grader_pb.GradeCallbackResponse, error) {
	g.queueGradeRequest(request)
	return &grader_pb.GradeCallbackResponse{}, nil
}

func (g *GraderHubService) GetMetadata(
	ctx context.Context, request *grader_pb.GetMetadataRequest,
) (*grader_pb.GetMetadataResponse, error) {
	graderId, key := request.GetGraderId(), request.GetKey()
	value, err := g.graderRepo.GetMetadata(ctx, graderId, key)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &grader_pb.GetMetadataResponse{Value: value}, nil
}

func (g *GraderHubService) PutMetadata(
	ctx context.Context, request *grader_pb.PutMetadataRequest,
) (*grader_pb.PutMetadataResponse, error) {
	graderId, key, value := request.GetGraderId(), request.GetKey(), request.GetValue()
	if value != nil {
		_ = g.graderRepo.PutMetadata(ctx, graderId, key, value)
	} else {
		_ = g.graderRepo.DeleteMetadata(ctx, graderId, key)
	}
	return &grader_pb.PutMetadataResponse{}, nil
}

func (g *GraderHubService) GetAllMetadata(
	ctx context.Context, request *grader_pb.GetAllMetadataRequest,
) (*grader_pb.GetAllMetadataResponse, error) {
	graderId := request.GetGraderId()
	keys, values, _ := g.graderRepo.GetAllMetadata(ctx, graderId)
	return &grader_pb.GetAllMetadataResponse{Keys: keys, Values: values}, nil
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
	logger := zap.L().With(zap.String("ip", ip), zap.String("hostname", hostname))
	logger.Debug("GraderHub.GraderRegister")
	g.onlineMu.Lock()
	defer g.onlineMu.Unlock()
	grader, graderId, err := g.graderRepo.GetGraderByName(ctx, hostname)
	if err != nil && err != pebble.ErrNotFound {
		return nil, status.Error(codes.Internal, "GET_GRADER")
	}
	if err == pebble.ErrNotFound {
		grader := &model_pb.GraderStatusMetadata{
			LastHeartbeat: timestamppb.Now(),
			Status:        model_pb.GraderStatusMetadata_Online,
			Info:          request.GetInfo(),
			Ip:            ip,
		}
		graderId, err := g.graderRepo.CreateGrader(ctx, hostname, grader)
		if err != nil {
			logger.Error("GraderHub.GraderRegister.CreateGrader", zap.Uint64("graderId", graderId), zap.Error(err))
			return nil, status.Error(codes.Internal, "CREATE_GRADER")
		}
		g.onlineGraders[graderId] = grader
		return &grader_pb.RegisterGraderResponse{GraderId: graderId}, nil
	}
	if grader.Status == model_pb.GraderStatusMetadata_Online {
		return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("'%s' is already taken by %s", hostname, grader.Ip))
	}
	grader.Status = model_pb.GraderStatusMetadata_Online
	grader.Ip = ip
	grader.Info = request.GetInfo()
	grader.LastHeartbeat = timestamppb.Now()
	if err := g.graderRepo.UpdateGrader(ctx, graderId, grader); err != nil {
		logger.Error("GraderHub.GraderRegister.UpdateGrader", zap.Uint64("graderId", graderId), zap.Error(err))
	}
	g.onlineGraders[graderId] = grader
	return &grader_pb.RegisterGraderResponse{GraderId: graderId}, nil
}

func (g *GraderHubService) queueGradeRequest(req *grader_pb.GradeRequest) {
	g.queuedMu.Lock()
	g.queuedList[req.GetSubmissionId()] = req
	g.queuedMu.Unlock()
	go g.onSubmissionQueued(req.SubmissionId)
}

func (g *GraderHubService) graderRequestSendLoop(
	server grader_pb.GraderHubService_GraderHeartbeatServer, graderId uint64, queue *GradeRequestQueue,
) {
	logger := zap.L().With(zap.Uint64("graderId", graderId))
	var err error
	for {
		queue.mu.Lock()
		if !queue.closed && len(queue.requests) == 0 {
			queue.cond.Wait()
		}
		if queue.closed && len(queue.requests) == 0 {
			queue.mu.Unlock()
			break
		}
		requests := make([]*grader_pb.GradeRequest, len(queue.requests), len(queue.requests))
		copy(requests, queue.requests)
		queue.requests = nil
		queue.mu.Unlock()
		var i int
		for i = 0; i < len(requests); i++ {
			r := requests[i]
			logger.Debug("GraderHeartbeat.SendGradeRequest", zap.Stringer("request", r))
			err = server.Send(&grader_pb.GraderHeartbeatResponse{Requests: []*grader_pb.GradeRequest{r}})
			if err != nil {
				logger.Error("GraderHeartbeat.Send", zap.Error(err))
				break
			}
		}
		if err != nil {
			for j := i; i < len(requests); j++ {
				g.queueGradeRequest(requests[j])
			}
			break
		}
	}
	logger.Info("GraderHeartbeat.RequestLoop.Exit")
}

func (g *GraderHubService) GraderHeartbeat(server grader_pb.GraderHubService_GraderHeartbeatServer) error {
	md, ok := metadata.FromIncomingContext(server.Context())
	if !ok {
		return status.Error(codes.InvalidArgument, "METADATA")
	}
	graderIdStr := md.Get("graderId")
	if len(graderIdStr) != 1 {
		return status.Error(codes.InvalidArgument, "METADATA")
	}
	graderIdInt, err := strconv.Atoi(graderIdStr[0])
	graderId := uint64(graderIdInt)
	if err != nil {
		return status.Error(codes.InvalidArgument, "METADATA")
	}
	zap.L().Info("GraderHeartbeat.First", zap.Uint64("graderId", graderId))

	queue := NewGradeRequestQueue()
	g.gradeRequestMu.Lock()
	g.gradeRequestQueues[graderId] = queue
	g.gradeRequestMu.Unlock()

	go g.graderRequestSendLoop(server, graderId, queue)
	// The scheduler may have a chance to schedule a grade task now
	g.onlineCond.Broadcast()

	var tCh chan *time.Time
	g.monitorMu.Lock()
	tCh = g.monitorChs[graderId]
	if tCh == nil {
		tCh = make(chan *time.Time)
		g.monitorChs[graderId] = tCh
		go g.graderMonitor(graderId, tCh)
	}
	g.monitorMu.Unlock()
	heartbeatRecv := &grader_pb.GraderHeartbeatRequest{}

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
		g.onlineMu.Lock()
		if _, ok := g.onlineGraders[heartbeatRecv.GraderId]; !ok {
			g.onlineMu.Unlock()
			return status.Error(codes.NotFound, "GRADER_NOT_REGISTERED")
		}
		g.onlineMu.Unlock()
		t := heartbeatRecv.Time.AsTime()
		tCh <- &t
	}
	zap.L().Info("GraderHeartbeat.ReadLoop.Exit", zap.Uint64("graderId", graderId))
	go func() {
		if tCh != nil {
			tCh <- nil
		}
	}()
	return nil
}

func (g *GraderHubService) onSubmissionBriefReportUpdate(
	ctx context.Context, submissionId uint64, brief *model_pb.SubmissionBriefReport,
) error {
	err := g.submissionReportRepo.UpdateSubmissionBriefReport(
		ctx, submissionId,
		brief,
	)
	g.sendGradeReport(submissionId, &grader_pb.GradeReport{Brief: brief})
	return err
}

func (g *GraderHubService) onSubmissionFinished(submissionId uint64) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId))
	err := g.submissionReportRepo.DeleteUnfinishedSubmission(context.Background(), submissionId)
	if err != nil {
		logger.Error("GraderHub.DeleteUnfinishedSubmission", zap.Error(err))
	}
	err = g.graderRepo.ReleaseSubmission(context.Background(), submissionId)
	if err != nil {
		logger.Error("GraderHub.ReleaseSubmission", zap.Error(err))
	}
	g.closeAllSubmissionSubscribers(submissionId)
	g.runningMu.Lock()
	delete(g.runningList, submissionId)
	g.runningMu.Unlock()
	g.onlineCond.Broadcast()
}

func (g *GraderHubService) onSubmissionCancelled(submissionId uint64) {
	err := g.onSubmissionBriefReportUpdate(
		context.Background(), submissionId,
		&model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Cancelled},
	)
	if err != nil {
		zap.L().Error("GraderHub.MarkSubmissionCancelling", zap.Uint64("submissionId", submissionId), zap.Error(err))
	}
	g.onSubmissionFinished(submissionId)
}

func (g *GraderHubService) onSubmissionCancelling(submissionId uint64) {
	err := g.onSubmissionBriefReportUpdate(
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
	var err error
	for {
		err = server.RecvMsg(r)
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
	if err == nil {
		g.onSubmissionFinished(submissionId)
	}
	return nil
}

func (g *GraderHubService) CancelGrade(
	ctx context.Context, submissionId uint64,
) error {
	// Queued, not running
	g.queuedMu.Lock()
	if _, ok := g.queuedList[submissionId]; ok {
		delete(g.queuedList, submissionId)
		g.onSubmissionCancelled(submissionId)
		g.queuedMu.Unlock()
		return nil
	}
	g.queuedMu.Unlock()
	// Running
	graderId, err := g.graderRepo.GetGraderIdBySubmissionId(ctx, submissionId)
	if err != nil {
		g.onSubmissionCancelled(submissionId)
		return nil
	}
	var queue *GradeRequestQueue
	g.gradeRequestMu.Lock()
	queue = g.gradeRequestQueues[graderId]
	g.gradeRequestMu.Unlock()
	if queue != nil {
		req := &grader_pb.GradeRequest{IsCancel: true, SubmissionId: submissionId}
		queue.mu.Lock()
		if !queue.closed {
			g.onSubmissionCancelling(submissionId)
			queue.requests = append(queue.requests, req)
			queue.mu.Unlock()
			queue.cond.Signal()
			return nil
		}
		queue.mu.Unlock()
		g.onSubmissionCancelled(submissionId)
	} else {
		g.onSubmissionCancelled(submissionId)
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
		gradeRequestMu:       &sync.Mutex{},
		subsMu:               &sync.Mutex{},
		queuedMu:             &sync.Mutex{},
		runningMu:            &sync.Mutex{},
		runningList:          map[uint64]*grader_pb.GradeRequest{},
		queuedList:           map[uint64]*grader_pb.GradeRequest{},
		onlineGraders:        map[uint64]*model_pb.GraderStatusMetadata{},
		submissionSubs:       map[uint64][]chan *grader_pb.GradeReport{},
		gradeRequestQueues:   map[uint64]*GradeRequestQueue{},
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
			err := gr.UpdateGrader(context.Background(), ids[i], graders[i])
			if err != nil {
				zap.L().Error("GraderHub.Init.UpdateGrader", zap.Error(err))
			}
		}
		tCh := make(chan *time.Time)
		svc.monitorChs[ids[i]] = tCh
	}
	for id, tCh := range svc.monitorChs {
		go svc.graderMonitor(id, tCh)
	}
	return svc
}
