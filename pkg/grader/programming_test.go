package grader

import (
	model_pb "autograder-server/pkg/model/proto"
	"testing"
)

func TestDockerProgrammingGrader(t *testing.T) {
	grader := NewDockerProgrammingGrader()
	cfg := &model_pb.ProgrammingAssignmentConfig{Image: "ubuntu:20.04"}
	grader.GradeSubmission(nil, cfg)
}
