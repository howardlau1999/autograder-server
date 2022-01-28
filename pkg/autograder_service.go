package pkg

import (
	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/repository"
	"context"
	"github.com/cockroachdb/pebble"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AutograderService struct {
	autograder_pb.UnimplementedAutograderServiceServer
	manifestRepo repository.ManifestRepository
}

func (a *AutograderService) GetFile(request *autograder_pb.GetFileRequest, server autograder_pb.AutograderService_GetFileServer) error {
	//TODO implement me
	panic("implement me")
}

func (a *AutograderService) GetSubmissionDetails(ctx context.Context, request *autograder_pb.GetSubmissionDetailsRequest) (*autograder_pb.GetSubmissionDetailsResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (a *AutograderService) CreateManifest(ctx context.Context, request *autograder_pb.CreateManifestRequest) (*autograder_pb.CreateManifestResponse, error) {
	id, err := a.manifestRepo.CreateManifest(request.GetUserId(), request.GetAssignmentId())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create manifest")
	}
	resp := &autograder_pb.CreateManifestResponse{ManifestId: id}
	return resp, nil
}

func (a *AutograderService) CreateSubmission(ctx context.Context, request *autograder_pb.CreateSubmissionRequest) (*autograder_pb.CreateSubmissionResponse, error) {
	//TODO implement me
	panic("implement me")
}

func (a *AutograderService) InitUpload(ctx context.Context, request *autograder_pb.InitUploadRequest) (*autograder_pb.InitUploadResponse, error) {
	_, err := a.manifestRepo.AddFileToManifest(request.GetFilename(), request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to add file to manifest")
	}
	resp := &autograder_pb.InitUploadResponse{}
	return resp, nil
}

func (a *AutograderService) StreamSubmissionLog(request *autograder_pb.StreamSubmissionLogRequest, server autograder_pb.AutograderService_StreamSubmissionLogServer) error {
	return nil
}

func (a *AutograderService) SubscribeSubmissions(request *autograder_pb.SubscribeSubmissionsRequest, server autograder_pb.AutograderService_SubscribeSubmissionsServer) error {
	return nil
}

func (a *AutograderService) GetSubmissionsInAssignment(ctx context.Context, request *autograder_pb.GetSubmissionsInAssignmentRequest) (*autograder_pb.GetSubmissionsInAssignmentResponse, error) {
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

func (a *AutograderService) GetAssignmentsInCourse(ctx context.Context, request *autograder_pb.GetAssignmentsInCourseRequest) (*autograder_pb.GetAssignmentsInCourseResponse, error) {
	response := &autograder_pb.GetAssignmentsInCourseResponse{
		Assignments: []*autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo{
			{AssignmentId: 1, Name: "单周期 CPU", ReleaseDate: timestamppb.Now(), DueDate: timestamppb.Now(), Submitted: true},
			{AssignmentId: 2, Name: "多周期 CPU", ReleaseDate: timestamppb.Now(), DueDate: timestamppb.Now(), Submitted: false},
		},
	}
	return response, nil
}

func (a *AutograderService) GetCourseList(ctx context.Context, request *autograder_pb.GetCourseListRequest) (*autograder_pb.GetCourseListResponse, error) {
	response := &autograder_pb.GetCourseListResponse{Courses: []*autograder_pb.GetCourseListResponse_CourseCardInfo{
		{ShortName: "CS 101", Name: "程序设计 I"},
		{ShortName: "CS 201", Name: "计算机组成原理"},
	}}
	return response, nil
}

func (a *AutograderService) Login(ctx context.Context, request *autograder_pb.LoginRequest) (*autograder_pb.LoginResponse, error) {
	response := &autograder_pb.LoginResponse{
		UserId:   0,
		Token:    "deadbeef",
		ExpireAt: nil,
	}
	return response, nil
}

func NewAutograderServiceServer() autograder_pb.AutograderServiceServer {
	db, err := pebble.Open("db", &pebble.Options{Merger: repository.NewKVMerger()})
	if err != nil {
		panic(err)
	}
	return &AutograderService{
		manifestRepo: repository.NewKVManifestRepository(db),
	}
}
