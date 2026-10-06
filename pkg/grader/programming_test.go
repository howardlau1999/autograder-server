package grader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
)

func TestDockerProgrammingGrader(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker integration test in short mode")
	}

	dockerGrader := NewDockerProgrammingGrader(1)
	if _, err := dockerGrader.cli.Ping(context.Background()); err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}

	basePath := t.TempDir()
	submissionPath := "uploads/manifests/1"
	if err := os.MkdirAll(filepath.Join(basePath, submissionPath), 0755); err != nil {
		t.Fatalf("failed to create submission dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(basePath, submissionPath, "main.txt"), []byte("hello"), 0644,
	); err != nil {
		t.Fatalf("failed to write submission file: %v", err)
	}

	cfg := &model_pb.ProgrammingAssignmentConfig{Image: "howardlau1999/hello-world"}
	submission := &model_pb.Submission{Path: submissionPath, Files: []string{"main.txt"}}
	notifyC := make(chan *grader_pb.GradeReport)
	go dockerGrader.GradeSubmission(
		context.Background(), basePath, 1, submission, cfg, "", "", notifyC,
	)

	var last *grader_pb.GradeReport
	timeout := time.After(5 * time.Minute)
Loop:
	for {
		select {
		case report, ok := <-notifyC:
			if !ok {
				break Loop
			}
			last = report
		case <-timeout:
			t.Fatal("timed out waiting for grade reports")
		}
	}

	if last == nil || last.GetBrief() == nil {
		t.Fatalf("no brief report received, last = %v", last)
	}
	t.Logf("last report: %v", last.GetBrief())
}
