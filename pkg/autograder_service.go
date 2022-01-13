package pkg

import (
	autograder_pb "autograder-server/pkg/api/proto"
	"context"
)

type AutograderService struct {
	autograder_pb.UnimplementedAutograderServiceServer
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
