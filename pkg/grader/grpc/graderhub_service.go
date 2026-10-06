package grpc

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"github.com/cockroachdb/pebble"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GradeRequestQueue is the per-grader outbox drained by graderRequestSendLoop.
// All access to requests/closed goes through its methods under mu; a closed
// queue (grader gone) rejects new requests so callers can re-route them.
type GradeRequestQueue struct {
	mu       *sync.Mutex
	cond     *sync.Cond
	requests []*grader_pb.GradeRequest
	closed   bool
}

type PendingRequest struct {
	rank    int
	request *grader_pb.GradeRequest
}

func (q *GradeRequestQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

// Push appends req for delivery and reports whether the queue accepted it.
func (q *GradeRequestQueue) Push(req *grader_pb.GradeRequest) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.requests = append(q.requests, req)
	q.cond.Signal()
	return true
}

// Drain removes and returns every queued request.
func (q *GradeRequestQueue) Drain() []*grader_pb.GradeRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	requests := q.requests
	q.requests = nil
	return requests
}

// WaitAndDrain blocks until there is something to send or the queue is
// closed. It returns nil, false once the queue is closed and empty.
func (q *GradeRequestQueue) WaitAndDrain() ([]*grader_pb.GradeRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for !q.closed && len(q.requests) == 0 {
		q.cond.Wait()
	}
	if q.closed && len(q.requests) == 0 {
		return nil, false
	}
	requests := q.requests
	q.requests = nil
	return requests, true
}

func NewGradeRequestQueue() *GradeRequestQueue {
	mu := &sync.Mutex{}
	cond := sync.NewCond(mu)
	return &GradeRequestQueue{mu: mu, cond: cond}
}

type ClientLogStream struct {
	ch  chan []byte
	ctx context.Context
}

type GraderHubService struct {
	grader_pb.UnimplementedGraderHubServiceServer
	token                string
	graderRepo           repository.GraderRepository
	submissionReportRepo repository.SubmissionReportRepository
	gradeRequestMu       *sync.Mutex
	gradeRequestQueues   map[uint64]*GradeRequestQueue
	heartbeatTimeout     time.Duration

	onlineMu      *sync.Mutex
	onlineGraders map[uint64]*model_pb.GraderStatusMetadata

	// sessions maps a grader id to the secret issued at its last registration.
	// Every later call must present it, binding the claimed grader id to the
	// connection that registered it.
	sessions   map[uint64]string
	sessionsMu *sync.Mutex

	submissionSubs map[uint64][]*ReportMailbox
	subsMu         *sync.Mutex
	monitorChs     map[uint64]chan *time.Time
	monitorMu      *sync.Mutex

	logStreams  map[uint64]map[string]*ClientLogStream
	logStreamMu *sync.Mutex

	queuedMu        *sync.Mutex
	schedulerCond   *sync.Cond
	queuedListIndex map[uint64]*list.Element
	queuedList      *list.List

	runningMu   *sync.Mutex
	runningList map[uint64]*grader_pb.GradeRequest
}

func (g *GraderHubService) removePendingGradeRequest(submissionId uint64) {
	elem := g.queuedListIndex[submissionId]
	if elem == nil {
		return
	}
	for p := elem.Next(); p != nil; p = p.Next() {
		pending := p.Value.(*PendingRequest)
		pending.rank--
		g.onPendingRankChanged(pending.request.SubmissionId, pending.rank, g.queuedList.Len()-1)
	}
	g.queuedList.Remove(elem)
	delete(g.queuedListIndex, submissionId)
}

func (g *GraderHubService) GetPendingRank(submissionId uint64) (rank int, total int) {
	g.queuedMu.Lock()
	defer g.queuedMu.Unlock()
	if elem, ok := g.queuedListIndex[submissionId]; ok {
		total = g.queuedList.Len()
		rank = elem.Value.(*PendingRequest).rank
	}
	return
}

func (g *GraderHubService) pushPendingGradeQueue(request *grader_pb.GradeRequest) *list.Element {
	g.queuedMu.Lock()
	defer g.queuedMu.Unlock()
	pending := &PendingRequest{request: request, rank: g.queuedList.Len() + 1}
	g.onPendingRankChanged(request.SubmissionId, g.queuedList.Len()+1, g.queuedList.Len()+1)
	elem := g.queuedList.PushBack(pending)
	g.queuedListIndex[request.SubmissionId] = elem
	g.schedulerCond.Broadcast()
	return elem
}

// graderLoad is a scheduling snapshot of one online grader: enough to decide
// feasibility without touching the DB again during a pass.
type graderLoad struct {
	concurrency uint64
	tags        []string
	running     uint64
}

// snapshotGraderLoads captures every online grader's capacity and current load
// once, so a scheduling pass does not issue a DB query per candidate per
// pending request. The caller must hold onlineMu.
func (g *GraderHubService) snapshotGraderLoads() map[uint64]*graderLoad {
	loads := make(map[uint64]*graderLoad, len(g.onlineGraders))
	for id, grader := range g.onlineGraders {
		submissions, _ := g.graderRepo.GetSubmissionsByGrader(context.Background(), id)
		loads[id] = &graderLoad{
			concurrency: grader.GetInfo().GetConcurrency(),
			tags:        grader.GetInfo().GetTags(),
			running:     uint64(len(submissions)),
		}
	}
	return loads
}

func graderHasTags(graderTags, requestTags []string) bool {
	for _, tag := range requestTags {
		found := false
		for _, gt := range graderTags {
			if gt == tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// pickGrader selects the least-loaded online grader that has spare concurrency
// and satisfies the request's tags, iterating ids in sorted order so ties and
// the overall distribution are deterministic. The chosen grader's running
// count in loads is incremented so repeated calls within one pass spread work
// instead of piling onto the same grader. Returns 0 when none is eligible.
func (g *GraderHubService) pickGrader(request *grader_pb.GradeRequest, loads map[uint64]*graderLoad) uint64 {
	requestTags := request.GetConfig().GetTags()
	ids := make([]uint64, 0, len(loads))
	for id := range loads {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var best uint64
	var bestLoad *graderLoad
	for _, id := range ids {
		load := loads[id]
		if load.running >= load.concurrency {
			continue
		}
		if !graderHasTags(load.tags, requestTags) {
			continue
		}
		if bestLoad == nil || load.running < bestLoad.running {
			best = id
			bestLoad = load
		}
	}
	if bestLoad != nil {
		bestLoad.running++
	}
	return best
}

func (g *GraderHubService) onGraderOffline(graderId uint64) {
	g.onlineMu.Lock()
	delete(g.onlineGraders, graderId)
	g.onlineMu.Unlock()

	g.logStreamMu.Lock()
	// We don't need to close channels because the stream loop will close it
	delete(g.logStreams, graderId)
	g.logStreamMu.Unlock()

	g.gradeRequestMu.Lock()
	queue := g.gradeRequestQueues[graderId]
	delete(g.gradeRequestQueues, graderId)
	g.gradeRequestMu.Unlock()

	// Collect everything that was in flight on this grader first, then
	// requeue outside of runningMu/queue.mu: requeueing takes queuedMu, which
	// the scheduler acquires before those locks, so nesting them here the
	// other way round would risk a deadlock.
	var requeue []*grader_pb.GradeRequest
	seen := map[uint64]bool{}
	submissions, _ := g.graderRepo.GetSubmissionsByGrader(context.Background(), graderId)
	g.runningMu.Lock()
	for _, subId := range submissions {
		if req := g.runningList[subId]; req != nil && !seen[subId] {
			delete(g.runningList, subId)
			seen[subId] = true
			requeue = append(requeue, req)
		}
	}
	if queue != nil {
		queue.Close()
		// A request may sit both in runningList and, not yet sent, in the
		// outbox; requeue each submission once.
		for _, req := range queue.Drain() {
			if req.GetIsStreamLog() || req.GetIsCancel() || seen[req.SubmissionId] {
				continue
			}
			delete(g.runningList, req.SubmissionId)
			seen[req.SubmissionId] = true
			requeue = append(requeue, req)
		}
	}
	g.runningMu.Unlock()

	for _, subId := range submissions {
		_ = g.graderRepo.ReleaseSubmission(context.Background(), subId)
	}
	for _, req := range requeue {
		if !g.isSubmissionTerminal(req.SubmissionId) {
			g.queueGradeRequest(req)
		}
	}
}

// isSubmissionTerminal reports whether a submission has already reached a final
// state, so a disconnecting grader does not cause it to be regraded.
func (g *GraderHubService) isSubmissionTerminal(submissionId uint64) bool {
	brief, err := g.submissionReportRepo.GetSubmissionBriefReport(context.Background(), submissionId)
	if err != nil || brief == nil {
		return false
	}
	switch brief.GetStatus() {
	case model_pb.SubmissionStatus_Finished,
		model_pb.SubmissionStatus_Failed,
		model_pb.SubmissionStatus_Cancelled:
		return true
	default:
		return false
	}
}

func (g *GraderHubService) graderMonitor(graderId uint64, alive chan *time.Time) {
	timer := time.NewTimer(g.heartbeatTimeout)
	logger := zap.L().With(zap.Uint64("graderId", graderId))
	logger.Info("Grader.Monitor.Start")
	defer logger.Info("Grader.Monitor.Exit")
	for {
		select {
		case t := <-alive:
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(g.heartbeatTimeout)
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
			timer.Reset(g.heartbeatTimeout)
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
				g.onGraderOffline(graderId)
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

// Subscribe registers a mailbox that receives every grade report for the
// submission until it reaches a terminal state, at which point the mailbox is
// closed. Call Unsubscribe to drop out early.
func (g *GraderHubService) Subscribe(submissionId uint64) *ReportMailbox {
	mailbox := NewReportMailbox()
	g.subsMu.Lock()
	g.submissionSubs[submissionId] = append(g.submissionSubs[submissionId], mailbox)
	g.subsMu.Unlock()
	return mailbox
}

// Unsubscribe removes and closes a mailbox obtained from Subscribe.
func (g *GraderHubService) Unsubscribe(submissionId uint64, mailbox *ReportMailbox) {
	g.subsMu.Lock()
	subs := g.submissionSubs[submissionId]
	for i, sub := range subs {
		if sub == mailbox {
			subs = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(subs) == 0 {
		delete(g.submissionSubs, submissionId)
	} else {
		g.submissionSubs[submissionId] = subs
	}
	g.subsMu.Unlock()
	mailbox.Close()
}

func (g *GraderHubService) onSubmissionScheduled(submissionId uint64, graderId uint64) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId), zap.Uint64("graderId", graderId))
	err := g.graderRepo.ClaimSubmission(context.Background(), graderId, submissionId)
	if err != nil {
		logger.Error("GradeHub.ClaimSubmission", zap.Error(err))
	}
	zap.L().Debug(
		"GraderHub.Scheduled",
		zap.Uint64("submissionId", submissionId),
		zap.Uint64("graderId", graderId),
	)
}

func (g *GraderHubService) onSubmissionQueued(submissionId uint64) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId))
	err := g.onSubmissionBriefReportUpdate(
		context.Background(), submissionId, &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Queued},
	)
	if err != nil {
		logger.Error("GraderHub.UpdateBriefReport", zap.Error(err))
	}
}

// Grade is part of the generated service surface but grading is only ever
// requested from inside this process (see EnqueueGrade); graders have no
// business submitting work to each other, so the RPC is refused.
func (g *GraderHubService) Grade(
	ctx context.Context, request *grader_pb.GradeRequest,
) (*grader_pb.GradeCallbackResponse, error) {
	return nil, status.Error(codes.PermissionDenied, "INTERNAL_ONLY")
}

// EnqueueGrade queues a submission for grading. Callers that want progress
// updates should Subscribe before calling it so the initial queued/rank
// reports are not missed.
func (g *GraderHubService) EnqueueGrade(request *grader_pb.GradeRequest) {
	g.queueGradeRequest(request)
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
	// The hub token was already verified by the auth interceptor.
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
		session, err := g.newSession(graderId)
		if err != nil {
			return nil, status.Error(codes.Internal, "ISSUE_SESSION")
		}
		return &grader_pb.RegisterGraderResponse{GraderId: graderId, SessionToken: session}, nil
	}
	// A grader that still looks online may only re-register if it proved, via a
	// valid current session token, that it is the same grader reconnecting.
	if grader.Status == model_pb.GraderStatusMetadata_Online {
		if authId, ok := GraderIdFromContext(ctx); !ok || authId != graderId {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("'%s' is already taken by %s", hostname, grader.Ip))
		}
	}
	grader.Status = model_pb.GraderStatusMetadata_Online
	grader.Ip = ip
	grader.Info = request.GetInfo()
	grader.LastHeartbeat = timestamppb.Now()
	if err := g.graderRepo.UpdateGrader(ctx, graderId, grader); err != nil {
		logger.Error("GraderHub.GraderRegister.UpdateGrader", zap.Uint64("graderId", graderId), zap.Error(err))
	}
	g.onlineGraders[graderId] = grader
	session, err := g.newSession(graderId)
	if err != nil {
		return nil, status.Error(codes.Internal, "ISSUE_SESSION")
	}
	return &grader_pb.RegisterGraderResponse{GraderId: graderId, SessionToken: session}, nil
}

func (g *GraderHubService) onPendingRankChanged(submissionId uint64, newRank int, total int) {
	zap.L().Debug(
		"GraderHub.Rank.Changed", zap.Uint64("submissionId", submissionId), zap.Int("rank", newRank),
		zap.Int("total", total),
	)
	g.sendGradeReport(
		submissionId,
		&grader_pb.GradeReport{PendingRank: &model_pb.PendingRank{Rank: uint64(newRank), Total: uint64(total)}},
	)
}

func (g *GraderHubService) scheduler() {
	zap.L().Debug("GraderHub.Scheduler.Start")
	defer zap.L().Debug("GraderHub.Scheduler.Exit")
	g.queuedMu.Lock()
	defer g.queuedMu.Unlock()
	for {
		for g.queuedList.Len() == 0 {
			g.schedulerCond.Wait()
		}
		if !g.schedulePass() {
			// Nothing could be placed (no grader online, all at capacity, or
			// no tag match). Sleep until something changes: a new request, a
			// grader (re)connecting, or a running submission finishing, all of
			// which broadcast schedulerCond. Spinning here would burn CPU and
			// hammer the DB for as long as the backlog persists.
			g.schedulerCond.Wait()
		}
	}
}

// schedulePass walks the pending list once and hands every request that has
// an eligible grader to that grader's outbox. It reports whether anything was
// scheduled. The caller must hold queuedMu; the lock order is
// queuedMu -> onlineMu / gradeRequestMu / queue.mu / runningMu, never nested
// the other way.
func (g *GraderHubService) schedulePass() bool {
	// Snapshot grader capacity and load once per pass instead of querying
	// the DB for every candidate of every pending request.
	g.onlineMu.Lock()
	loads := g.snapshotGraderLoads()
	g.onlineMu.Unlock()
	if len(loads) == 0 {
		return false
	}
	progressed := false
	for cur := g.queuedList.Front(); cur != nil; {
		pending := cur.Value.(*PendingRequest)
		cur = cur.Next()
		graderId := g.pickGrader(pending.request, loads)
		if graderId == 0 {
			continue
		}
		g.gradeRequestMu.Lock()
		queue := g.gradeRequestQueues[graderId]
		g.gradeRequestMu.Unlock()
		if queue == nil {
			// Registered but its heartbeat stream is not up yet.
			continue
		}
		// Claim before pushing: the grader may start reporting the instant it
		// receives the request, and GradeCallback only accepts reports from
		// the grader that holds the claim.
		g.runningMu.Lock()
		g.runningList[pending.request.SubmissionId] = pending.request
		g.runningMu.Unlock()
		g.onSubmissionScheduled(pending.request.SubmissionId, graderId)
		if !queue.Push(pending.request) {
			// The grader went away between the snapshot and now; leave the
			// request pending for the next pass.
			g.runningMu.Lock()
			delete(g.runningList, pending.request.SubmissionId)
			g.runningMu.Unlock()
			_ = g.graderRepo.ReleaseSubmission(context.Background(), pending.request.SubmissionId)
			continue
		}
		g.removePendingGradeRequest(pending.request.SubmissionId)
		progressed = true
	}
	return progressed
}

func (g *GraderHubService) sendGraderGradeRequest(graderId uint64, req *grader_pb.GradeRequest) bool {
	g.gradeRequestMu.Lock()
	queue := g.gradeRequestQueues[graderId]
	g.gradeRequestMu.Unlock()
	if queue == nil {
		return false
	}
	return queue.Push(req)
}

func (g *GraderHubService) queueGradeRequest(req *grader_pb.GradeRequest) {
	g.pushPendingGradeQueue(req)
	g.onSubmissionQueued(req.SubmissionId)
}

func (g *GraderHubService) graderRequestSendLoop(
	server grader_pb.GraderHubService_GraderHeartbeatServer, graderId uint64, queue *GradeRequestQueue,
) {
	logger := zap.L().With(zap.Uint64("graderId", graderId))
	for {
		requests, ok := queue.WaitAndDrain()
		if !ok {
			break
		}
		if err := server.Send(&grader_pb.GraderHeartbeatResponse{Requests: requests}); err != nil {
			logger.Error("GraderHeartbeat.Send", zap.Error(err))
			break
		}
	}
	logger.Info("GraderHeartbeat.RequestLoop.Exit")
}

func (g *GraderHubService) GraderHeartbeat(server grader_pb.GraderHubService_GraderHeartbeatServer) error {
	graderId, ok := GraderIdFromContext(server.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "INVALID_SESSION")
	}
	zap.L().Info("GraderHeartbeat.First", zap.Uint64("graderId", graderId))

	queue := NewGradeRequestQueue()
	g.gradeRequestMu.Lock()
	g.gradeRequestQueues[graderId] = queue
	g.gradeRequestMu.Unlock()

	go g.graderRequestSendLoop(server, graderId, queue)
	// The scheduler may have a chance to schedule a grade task now
	g.schedulerCond.Broadcast()

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
			"GraderHeartbeat.RecvMsg", zap.Uint64("graderId", graderId),
			zap.Time("time", heartbeatRecv.Time.AsTime()),
		)
		g.onlineMu.Lock()
		if _, ok := g.onlineGraders[graderId]; !ok {
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
	g.schedulerCond.Broadcast()
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
	var subs []*ReportMailbox
	g.subsMu.Lock()
	subs = append(subs, g.submissionSubs[submissionId]...)
	g.subsMu.Unlock()
	for _, sub := range subs {
		sub.Publish(report)
	}
}

func (g *GraderHubService) closeAllSubmissionSubscribers(submissionId uint64) {
	g.subsMu.Lock()
	subs := g.submissionSubs[submissionId]
	delete(g.submissionSubs, submissionId)
	g.subsMu.Unlock()
	for _, sub := range subs {
		sub.Close()
	}
}

var errGraderOffline = errors.New("grader offline")

func (g *GraderHubService) StreamLog(ctx context.Context, submissionId uint64) (chan []byte, error) {
	requestId := uuid.NewString()
	logger := zap.L().With(zap.Uint64("submissionId", submissionId), zap.String("requestId", requestId))
	logger.Debug("StreamLog.Start")
	defer logger.Debug("StreamLog.Exit")
	graderId, err := g.graderRepo.GetGraderIdBySubmissionId(ctx, submissionId)
	if err != nil {
		return nil, err
	}
	g.gradeRequestMu.Lock()
	queue := g.gradeRequestQueues[graderId]
	g.gradeRequestMu.Unlock()
	if queue == nil {
		return nil, errGraderOffline
	}
	ch := make(chan []byte)
	g.logStreamMu.Lock()
	if g.logStreams[graderId] == nil {
		g.logStreams[graderId] = map[string]*ClientLogStream{}
	}
	g.logStreams[graderId][requestId] = &ClientLogStream{ctx: ctx, ch: ch}
	g.logStreamMu.Unlock()
	if !queue.Push(&grader_pb.GradeRequest{IsStreamLog: true, SubmissionId: submissionId, RequestId: requestId}) {
		g.logStreamMu.Lock()
		delete(g.logStreams[graderId], requestId)
		g.logStreamMu.Unlock()
		return nil, errGraderOffline
	}
	go func() {
		<-ctx.Done()
		logger.Debug("StreamLog.Client.Done")
		g.sendGraderGradeRequest(
			graderId, &grader_pb.GradeRequest{
				IsStreamLog: true, SubmissionId: submissionId, RequestId: requestId, IsCancel: true,
			},
		)
	}()
	return ch, nil
}

func (g *GraderHubService) StreamLogCallback(server grader_pb.GraderHubService_StreamLogCallbackServer) error {
	r := &grader_pb.StreamLogResponse{}
	var err error
	var client *ClientLogStream
	var logStream chan []byte
	graderId, ok := GraderIdFromContext(server.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "INVALID_SESSION")
	}
	md, _ := metadata.FromIncomingContext(server.Context())
	requestIdVals := md.Get("requestId")
	if len(requestIdVals) == 0 {
		return status.Error(codes.InvalidArgument, "METADATA")
	}
	requestId := requestIdVals[0]
	g.logStreamMu.Lock()
	client = g.logStreams[graderId][requestId]
	g.logStreamMu.Unlock()
	logger := zap.L().With(zap.Uint64("graderId", graderId), zap.String("requestId", requestId))
	logger.Debug("LogStreamCallback.Start")
	defer logger.Debug("LogStreamCallback.Exit")
	if client == nil {
		return nil
	}
	logStream = client.ch
	for {
		err = server.RecvMsg(r)
		if err != nil {
			break
		}
		if client.ctx.Err() != nil {
			break
		}
		logStream <- r.Data
	}
	g.logStreamMu.Lock()
	delete(g.logStreams[graderId], requestId)
	g.logStreamMu.Unlock()
	close(logStream)
	return nil
}

const ErrGradeCallback = -201

func (g *GraderHubService) GradeCallback(server grader_pb.GraderHubService_GradeCallbackServer) error {
	r := &grader_pb.GradeResponse{}
	var submissionId uint64
	graderId, ok := GraderIdFromContext(server.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "INVALID_SESSION")
	}
	finished := false
	for {
		err := server.RecvMsg(r)
		if err != nil {
			if err != io.EOF {
				zap.L().Error("GradeCallback.RecvMsg", zap.Error(err))
			}
			break
		}
		submissionId = r.GetSubmissionId()
		logger := zap.L().With(zap.Uint64("submissionId", submissionId), zap.Uint64("graderId", graderId))
		// Only the grader that holds the claim may report on a submission;
		// anything else is a stale stream from a previous assignment or a
		// misbehaving grader, and must not overwrite the current result.
		owner, err := g.graderRepo.GetGraderIdBySubmissionId(context.Background(), submissionId)
		if err != nil || owner != graderId {
			logger.Warn("GradeCallback.NotOwner", zap.Uint64("owner", owner))
			return status.Error(codes.PermissionDenied, "NOT_SUBMISSION_OWNER")
		}
		report := r.GetReport()
		logger.Debug("GradeCallback.Recved", zap.Stringer("brief", report.GetBrief()))
		if report.GetBrief() != nil {
			err = g.submissionReportRepo.UpdateSubmissionBriefReport(
				context.Background(), submissionId, report.GetBrief(),
			)
			if err != nil {
				logger.Error("GradeCallback.UpdateBrief", zap.Error(err))
			}
		}
		if report.GetReport() != nil {
			logger.Debug("GradeCallback.UpdateReport")
			err = g.submissionReportRepo.UpdateSubmissionReport(context.Background(), submissionId, report.GetReport())
			if err != nil {
				logger.Error("GradeCallback.UpdateReport", zap.Error(err))
			}
		}
		if report.GetBrief() != nil || report.GetPendingRank() != nil || report.GetReport() != nil {
			g.sendGradeReport(submissionId, report)
		}
		if report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Failed ||
			report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Finished ||
			report.GetBrief().GetStatus() == model_pb.SubmissionStatus_Cancelled {
			finished = true
			break
		}
	}
	if finished {
		g.onSubmissionFinished(submissionId)
	}
	if err := server.SendAndClose(&grader_pb.GradeCallbackResponse{}); err != nil {
		zap.L().Error("GradeCallback.SendAndClose", zap.Uint64("submissionId", submissionId), zap.Error(err))
	}
	zap.L().Debug("GradeCallback.Exit", zap.Uint64("submissionId", submissionId))
	return nil
}

func (g *GraderHubService) CancelGrade(
	ctx context.Context, submissionId uint64,
) error {
	// Queued, not running
	g.queuedMu.Lock()
	if _, ok := g.queuedListIndex[submissionId]; ok {
		g.removePendingGradeRequest(submissionId)
		g.queuedMu.Unlock()
		g.onSubmissionCancelled(submissionId)
		return nil
	}
	g.queuedMu.Unlock()

	// Running: ask the owning grader to stop. If it is unreachable the
	// submission is finalised as cancelled right away.
	graderId, err := g.graderRepo.GetGraderIdBySubmissionId(ctx, submissionId)
	if err != nil {
		g.onSubmissionCancelled(submissionId)
		return nil
	}
	if g.sendGraderGradeRequest(graderId, &grader_pb.GradeRequest{IsCancel: true, SubmissionId: submissionId}) {
		g.onSubmissionCancelling(submissionId)
		return nil
	}
	g.onSubmissionCancelled(submissionId)
	return nil
}

func NewGraderHubService(
	db *pebble.DB, srr repository.SubmissionReportRepository, token string, heartbeatInterval time.Duration,
) *GraderHubService {
	gr := repository.NewKVGraderRepository(db)
	svc := &GraderHubService{
		graderRepo:           gr,
		submissionReportRepo: srr,
		monitorMu:            &sync.Mutex{},
		onlineMu:             &sync.Mutex{},
		sessions:             map[uint64]string{},
		sessionsMu:           &sync.Mutex{},
		gradeRequestMu:       &sync.Mutex{},
		subsMu:               &sync.Mutex{},
		queuedMu:             &sync.Mutex{},
		runningMu:            &sync.Mutex{},
		logStreamMu:          &sync.Mutex{},
		logStreams:           map[uint64]map[string]*ClientLogStream{},
		runningList:          map[uint64]*grader_pb.GradeRequest{},
		queuedListIndex:      map[uint64]*list.Element{},
		queuedList:           list.New(),
		onlineGraders:        map[uint64]*model_pb.GraderStatusMetadata{},
		submissionSubs:       map[uint64][]*ReportMailbox{},
		gradeRequestQueues:   map[uint64]*GradeRequestQueue{},
		monitorChs:           map[uint64]chan *time.Time{},
		token:                token,
		heartbeatTimeout:     heartbeatInterval,
	}
	svc.schedulerCond = sync.NewCond(svc.queuedMu)
	ids, graders, err := gr.GetAllGraders(context.Background())
	if err != nil {
		panic(err)
	}
	gr.ClearRunning(context.Background())
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
	go svc.scheduler()
	return svc
}
