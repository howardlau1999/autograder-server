package grpc

import (
	"container/list"
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
)

type schedulerGraderRepo struct {
	repository.GraderRepository
	mu            sync.Mutex
	claims        map[uint64]uint64
	afterSnapshot func()
}

func (r *schedulerGraderRepo) GetSubmissionsByGrader(_ context.Context, graderID uint64) ([]uint64, error) {
	r.mu.Lock()
	var ids []uint64
	for id, owner := range r.claims {
		if owner == graderID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	if r.afterSnapshot != nil {
		r.afterSnapshot()
	}
	return ids, nil
}

func (r *schedulerGraderRepo) ClaimSubmission(_ context.Context, graderID, submissionID uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims[submissionID] = graderID
	return nil
}

func (r *schedulerGraderRepo) ReleaseSubmission(_ context.Context, submissionID uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.claims, submissionID)
	return nil
}

type schedulerReportRepo struct {
	repository.SubmissionReportRepository
	briefs map[uint64]*model_pb.SubmissionBriefReport
}

func (r *schedulerReportRepo) DeleteUnfinishedSubmission(context.Context, uint64) error {
	return nil
}

func (r *schedulerReportRepo) UpdateSubmissionBriefReport(_ context.Context, id uint64, brief *model_pb.SubmissionBriefReport) error {
	r.briefs[id] = brief
	return nil
}

func (r *schedulerReportRepo) GetSubmissionBriefReport(_ context.Context, id uint64) (*model_pb.SubmissionBriefReport, error) {
	return r.briefs[id], nil
}

func newSchedulerTestHub() (*GraderHubService, *schedulerGraderRepo) {
	repo := &schedulerGraderRepo{claims: map[uint64]uint64{}}
	ctx, cancel := context.WithCancel(context.Background())
	return &GraderHubService{
		graderRepo:           repo,
		submissionReportRepo: &schedulerReportRepo{briefs: map[uint64]*model_pb.SubmissionBriefReport{}},
		queuedMu:             &sync.Mutex{},
		schedulerWake:        make(chan struct{}, 1),
		queuedList:           list.New(),
		queuedListIndex:      map[uint64]*list.Element{},
		onlineMu:             &sync.Mutex{},
		onlineGraders:        map[uint64]*model_pb.GraderStatusMetadata{},
		gradeRequestMu:       &sync.Mutex{},
		gradeRequestQueues:   map[uint64]*GradeRequestQueue{},
		runningMu:            &sync.Mutex{},
		runningList:          map[uint64]*grader_pb.GradeRequest{},
		subsMu:               &sync.Mutex{},
		submissionSubs:       map[uint64][]*ReportMailbox{},
		logStreamMu:          &sync.Mutex{},
		logStreams:           map[uint64]map[string]*ClientLogStream{},
		ctx:                  ctx,
		cancel:               cancel,
	}, repo
}

func addSchedulerTestGrader(g *GraderHubService, id uint64, queue *GradeRequestQueue) {
	g.onlineGraders[id] = &model_pb.GraderStatusMetadata{Info: &model_pb.GraderInfo{Concurrency: 1}}
	if queue != nil {
		g.gradeRequestQueues[id] = queue
	}
}

func startTestScheduler(g *GraderHubService) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.scheduler()
	}()
	return func() {
		g.Close()
		<-done
	}
}

func TestSchedulerKeepsCompletionWakeDuringPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, repo := newSchedulerTestHub()
		queue := NewGradeRequestQueue()
		addSchedulerTestGrader(g, 1, queue)
		repo.claims[1] = 1
		g.pushPendingGradeQueue(&grader_pb.GradeRequest{SubmissionId: 2})
		// Consume the enqueue notification: only completion may wake the
		// scheduler after it reads the deliberately stale capacity snapshot.
		<-g.schedulerWake
		snapshotRead, resume := make(chan struct{}), make(chan struct{})
		var once sync.Once
		repo.afterSnapshot = func() {
			once.Do(func() {
				close(snapshotRead)
				<-resume
			})
		}
		stop := startTestScheduler(g)
		defer stop()
		<-snapshotRead
		g.onSubmissionFinished(1)
		close(resume)
		synctest.Wait()
		requests := queue.Drain()
		if len(requests) != 1 || requests[0].GetSubmissionId() != 2 {
			t.Fatalf("completion did not schedule pending submission: %v", requests)
		}
		if rank, _ := g.GetPendingRank(2); rank != 0 {
			t.Fatalf("scheduled submission still has pending rank %d", rank)
		}
	})
}

func TestSchedulePassFallsBackFromUnavailableGrader(t *testing.T) {
	for _, unavailable := range []string{"missing outbox", "closed outbox"} {
		t.Run(unavailable, func(t *testing.T) {
			g, repo := newSchedulerTestHub()
			defer g.Close()
			var first *GradeRequestQueue
			if unavailable == "closed outbox" {
				first = NewGradeRequestQueue()
				first.Close()
			}
			ready := NewGradeRequestQueue()
			addSchedulerTestGrader(g, 1, first)
			addSchedulerTestGrader(g, 2, ready)
			g.pushPendingGradeQueue(&grader_pb.GradeRequest{SubmissionId: 3})
			g.queuedMu.Lock()
			progressed := g.schedulePass()
			g.queuedMu.Unlock()
			if !progressed {
				t.Fatal("available grader was ignored")
			}
			requests := ready.Drain()
			if len(requests) != 1 || requests[0].GetSubmissionId() != 3 {
				t.Fatalf("ready grader received %v", requests)
			}
			if repo.claims[3] != 2 || len(repo.claims) != 1 {
				t.Fatalf("submission claims = %v, want only 3 -> 2", repo.claims)
			}
		})
	}
}

func TestSchedulerCloseWhileWaiting(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "no available grader"}[pending], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g, _ := newSchedulerTestHub()
				if pending {
					g.pushPendingGradeQueue(&grader_pb.GradeRequest{SubmissionId: 1})
				}
				stop := startTestScheduler(g)
				synctest.Wait()
				stop()
			})
		})
	}
}

func TestSchedulerCloseWaitsForActivePass(t *testing.T) {
	g, repo := newSchedulerTestHub()
	addSchedulerTestGrader(g, 1, NewGradeRequestQueue())
	g.pushPendingGradeQueue(&grader_pb.GradeRequest{SubmissionId: 1})
	snapshotRead, resume := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(resume) })
	repo.afterSnapshot = func() {
		close(snapshotRead)
		<-resume
	}
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		g.scheduler()
	}()
	<-snapshotRead
	closed := make(chan struct{})
	go func() {
		g.Close()
		close(closed)
	}()
	// Cancellation proves Close has started. The snapshot barrier keeps
	// the database-using pass active until the test explicitly releases it.
	<-g.ctx.Done()
	select {
	case <-closed:
		t.Error("Close returned while a scheduling pass was still using the repository")
	case <-time.After(20 * time.Millisecond):
	}
	unblock.Do(func() { close(resume) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the active pass finished")
	}
	select {
	case <-schedulerDone:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after Close")
	}
}

func TestSchedulerDoesNotDeliverCancelledPendingSubmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, repo := newSchedulerTestHub()
		queue := NewGradeRequestQueue()
		addSchedulerTestGrader(g, 1, queue)
		g.pushPendingGradeQueue(&grader_pb.GradeRequest{SubmissionId: 1})
		g.pushPendingGradeQueue(&grader_pb.GradeRequest{SubmissionId: 2})
		if err := g.CancelGrade(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		stop := startTestScheduler(g)
		defer stop()
		synctest.Wait()
		requests := queue.Drain()
		if len(requests) != 1 || requests[0].GetSubmissionId() != 2 {
			t.Fatalf("requests after cancellation = %v, want only submission 2", requests)
		}
		if _, claimed := repo.claims[1]; claimed {
			t.Fatal("cancelled submission was claimed")
		}
		brief, _ := g.submissionReportRepo.GetSubmissionBriefReport(context.Background(), 1)
		if brief.GetStatus() != model_pb.SubmissionStatus_Cancelled {
			t.Fatalf("cancelled submission status = %v", brief.GetStatus())
		}
	})
}

func TestSchedulerRequeuesOfflineGraderWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, repo := newSchedulerTestHub()
		oldQueue, ready := NewGradeRequestQueue(), NewGradeRequestQueue()
		addSchedulerTestGrader(g, 1, oldQueue)
		addSchedulerTestGrader(g, 2, ready)
		request := &grader_pb.GradeRequest{SubmissionId: 1}
		repo.claims[1] = 1
		g.runningList[1] = request
		oldQueue.Push(request)
		g.onGraderOffline(1)
		stop := startTestScheduler(g)
		defer stop()
		synctest.Wait()
		requests := ready.Drain()
		if len(requests) != 1 || requests[0].GetSubmissionId() != 1 {
			t.Fatalf("requeued requests = %v, want submission 1 exactly once", requests)
		}
		if repo.claims[1] != 2 {
			t.Fatalf("requeued submission owner = %d, want 2", repo.claims[1])
		}
	})
}
