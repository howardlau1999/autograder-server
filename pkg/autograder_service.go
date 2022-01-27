package pkg

import (
	autograder_pb "autograder-server/pkg/api/proto"
	"context"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AutograderService struct {
	autograder_pb.UnimplementedAutograderServiceServer
}

func (a AutograderService) StreamSubmissionLog(request *autograder_pb.StreamSubmissionLogRequest, server autograder_pb.AutograderService_StreamSubmissionLogServer) error {
	return nil
}

func (a AutograderService) SubscribeSubmissions(request *autograder_pb.SubscribeSubmissionsRequest, server autograder_pb.AutograderService_SubscribeSubmissionsServer) error {
	return nil
}

func (a AutograderService) GetSubmissionsInAssignment(ctx context.Context, request *autograder_pb.GetSubmissionsInAssignmentRequest) (*autograder_pb.GetSubmissionsInAssignmentResponse, error) {
	submitters := []*autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo_Submitter{
		{
			UserId:   1,
			Username: "root",
		}, {
			UserId:   2,
			Username: "howard",
		},
	}
	response := &autograder_pb.GetSubmissionsInAssignmentResponse{
		Submissions: []*autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo{
			{
				SubmissionId: 1,
				SubmittedAt:  timestamppb.Now(),
				Submitters:   submitters,
				Score:        20,
				Status:       autograder_pb.SubmissionStatus_Finished,
				MaxScore:     100,
			},
			{
				SubmissionId: 2,
				SubmittedAt:  timestamppb.Now(),
				Submitters:   submitters,
				Score:        80,
				Status:       autograder_pb.SubmissionStatus_Running,
				MaxScore:     100,
			},
		},
	}
	return response, nil
}

func (a AutograderService) GetAssignmentsInCourse(ctx context.Context, request *autograder_pb.GetAssignmentsInCourseRequest) (*autograder_pb.GetAssignmentsInCourseResponse, error) {
	response := &autograder_pb.GetAssignmentsInCourseResponse{
		Assignments: []*autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo{
			{AssignmentId: 1, Name: "单周期 CPU", ReleaseDate: timestamppb.Now(), DueDate: timestamppb.Now(), Submitted: true},
			{AssignmentId: 2, Name: "多周期 CPU", ReleaseDate: timestamppb.Now(), DueDate: timestamppb.Now(), Submitted: false},
		},
	}
	return response, nil
}

func (a AutograderService) GetCourseList(ctx context.Context, request *autograder_pb.GetCourseListRequest) (*autograder_pb.GetCourseListResponse, error) {
	response := &autograder_pb.GetCourseListResponse{Courses: []*autograder_pb.GetCourseListResponse_CourseCardInfo{
		{ShortName: "CS 101", Name: "程序设计 I"},
		{ShortName: "CS 201", Name: "计算机组成原理"},
	}}
	return response, nil
}

func (a AutograderService) Login(ctx context.Context, request *autograder_pb.LoginRequest) (*autograder_pb.LoginResponse, error) {
	response := &autograder_pb.LoginResponse{
		UserId:   0,
		Token:    "deadbeef",
		ExpireAt: nil,
	}
	return response, nil
}

func NewAutograderServiceServer() autograder_pb.AutograderServiceServer {
	return &AutograderService{}
}
