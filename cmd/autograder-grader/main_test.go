package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// reporterTestHub holds the stream's final response until the test has checked
// that the reporter delivered every message and still retains its local files.
type reporterTestHub struct {
	grader_pb.UnimplementedGraderHubServiceServer
	received chan []*grader_pb.GradeResponse
	complete chan error
}

func (h *reporterTestHub) GradeCallback(stream grader_pb.GraderHubService_GradeCallbackServer) error {
	var reports []*grader_pb.GradeResponse
	for {
		report, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}
	select {
	case h.received <- reports:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	select {
	case err := <-h.complete:
		if err != nil {
			return err
		}
		return stream.SendAndClose(&grader_pb.GradeCallbackResponse{})
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
}

func (h *reporterTestHub) PutMetadata(context.Context, *grader_pb.PutMetadataRequest) (*grader_pb.PutMetadataResponse, error) {
	return &grader_pb.PutMetadataResponse{}, nil
}

func TestSubmissionReporterDrainsClosedBufferBeforeCleanup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []model_pb.SubmissionStatus
		retry    bool
	}{
		{name: "immediate failure", statuses: []model_pb.SubmissionStatus{model_pb.SubmissionStatus_Failed}},
		{name: "ordered reports", statuses: []model_pb.SubmissionStatus{model_pb.SubmissionStatus_Queued, model_pb.SubmissionStatus_Running, model_pb.SubmissionStatus_Failed}},
		{name: "retry stream completion", statuses: []model_pb.SubmissionStatus{model_pb.SubmissionStatus_Failed}, retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			hub := &reporterTestHub{received: make(chan []*grader_pb.GradeResponse, 1), complete: make(chan error, 1)}
			server := grpc.NewServer()
			grader_pb.RegisterGraderHubServiceServer(server, hub)
			go server.Serve(listener)
			t.Cleanup(server.Stop)

			base := t.TempDir()
			worker := &GraderWorker{hubAddress: listener.Addr().String(), ls: storage.NewLocalStorage(base), mu: &sync.Mutex{}}
			worker.setIdentity(7, "test-session")
			const resultPath = "runs/submissions/1/results/report.json"
			const result = "retained grading result"
			if err := worker.ls.Put(context.Background(), resultPath, strings.NewReader(result)); err != nil {
				t.Fatal(err)
			}
			buffer := NewReportBuffer()
			var want []*grader_pb.GradeResponse
			for _, state := range tc.statuses {
				report := &grader_pb.GradeReport{Brief: &model_pb.SubmissionBriefReport{Status: state}}
				buffer.Send(report)
				want = append(want, &grader_pb.GradeResponse{SubmissionId: 1, Report: report})
			}
			// A fast failure can close the producer before the reporter is scheduled.
			buffer.Close()
			done := make(chan struct{})
			go func() {
				worker.submissionReporter(1, buffer)
				close(done)
			}()
			t.Cleanup(func() { close(hub.complete) })

			assertRetained := func() {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(base, resultPath))
				if err != nil || string(data) != result {
					t.Fatalf("result deleted before successful stream completion: data=%q error=%v", data, err)
				}
				select {
				case <-done:
					t.Fatal("reporter exited before the hub completed the stream")
				default:
				}
			}
			waitReports := func() []*grader_pb.GradeResponse {
				t.Helper()
				select {
				case reports := <-hub.received:
					return reports
				case <-done:
					t.Fatal("reporter exited before delivering the closed buffer to the hub")
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for the hub to receive the report stream")
				}
				return nil
			}
			got := waitReports()
			if len(got) != len(want) {
				t.Fatalf("hub received %d reports, want %d", len(got), len(want))
			}
			for i := range want {
				if !proto.Equal(got[i], want[i]) {
					t.Errorf("report %d = %v, want %v", i, got[i], want[i])
				}
			}
			assertRetained()
			if tc.retry {
				// Even after Send succeeds, a failed CloseAndRecv must trigger
				// another session before cleanup, despite the closed empty buffer.
				hub.complete <- status.Error(codes.Unavailable, "retry completion")
				waitReports()
				assertRetained()
			}
			hub.complete <- nil
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("reporter did not exit after successful stream completion")
			}
			if _, err := os.Stat(filepath.Join(base, "runs/submissions/1")); !os.IsNotExist(err) {
				t.Fatalf("completed run was not cleaned up: %v", err)
			}
		})
	}
}
