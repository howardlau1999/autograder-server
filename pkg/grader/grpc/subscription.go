package grpc

import (
	"sync"

	grader_pb "autograder-server/pkg/grader/proto"
)

// ReportMailbox hands grade reports to a single subscriber without ever
// blocking the publisher. It holds at most one pending report: when the
// subscriber is slow the stale report is replaced by the newer one, folding in
// any field the newer report does not carry so no state is lost. Only the
// latest state matters to subscribers (score, status, queue position), so
// dropping intermediate updates is safe. A publisher that blocked here could
// stall the grade-callback stream and every other subscriber of the same
// submission.
type ReportMailbox struct {
	mu     sync.Mutex
	ch     chan *grader_pb.GradeReport
	closed bool
}

func NewReportMailbox() *ReportMailbox {
	return &ReportMailbox{ch: make(chan *grader_pb.GradeReport, 1)}
}

// Chan returns the receive side the subscriber ranges over. It is closed by
// Close.
func (m *ReportMailbox) Chan() <-chan *grader_pb.GradeReport {
	return m.ch
}

// Publish delivers report to the subscriber, never blocking. If an unread
// report is still queued it is merged with the new one so no field is lost.
func (m *ReportMailbox) Publish(report *grader_pb.GradeReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	for {
		select {
		case m.ch <- report:
			return
		default:
		}
		// The slot is full: take the stale report out and merge it into the
		// new one. The subscriber may win the race and drain it first, in
		// which case the next loop iteration sends successfully.
		select {
		case stale := <-m.ch:
			report = coalesceReports(stale, report)
		default:
		}
	}
}

// Close releases the subscriber; its Chan is closed and further Publish calls
// are no-ops. It is safe to call more than once.
func (m *ReportMailbox) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.ch)
}

// coalesceReports returns a report carrying every field of newer, falling back
// to older for the fields newer leaves unset. Neither input is modified since
// the same report pointer is shared by all subscribers.
func coalesceReports(older, newer *grader_pb.GradeReport) *grader_pb.GradeReport {
	merged := &grader_pb.GradeReport{
		Report:         newer.GetReport(),
		Brief:          newer.GetBrief(),
		DockerMetadata: newer.GetDockerMetadata(),
		PendingRank:    newer.GetPendingRank(),
	}
	if merged.Report == nil {
		merged.Report = older.GetReport()
	}
	if merged.Brief == nil {
		merged.Brief = older.GetBrief()
	}
	if merged.DockerMetadata == nil {
		merged.DockerMetadata = older.GetDockerMetadata()
	}
	if merged.PendingRank == nil {
		merged.PendingRank = older.GetPendingRank()
	}
	return merged
}
