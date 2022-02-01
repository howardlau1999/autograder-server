package grader

import (
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"context"
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"log"
	"testing"
)

func TestDockerProgrammingGrader(t *testing.T) {
	db, err := pebble.Open("db", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		panic(err)
	}
	defer db.Close()
	srr := repository.NewKVSubmissionReportRepository(db)
	grader := NewDockerProgrammingGrader(srr)
	cfg := &model_pb.ProgrammingAssignmentConfig{Image: "howardlau1999/hello-world"}
	grader.GradeSubmission(1, &model_pb.Submission{Path: "uploads/manifests/1"}, cfg)
	log.Println(srr.GetSubmissionReport(context.Background(), 1))
}
