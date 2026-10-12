package grpc

import (
	"context"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/logging"
	model_pb "autograder-server/pkg/model/proto"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (a *AutograderService) UpdateAssignment(
	ctx context.Context, request *autograder_pb.UpdateAssignmentRequest,
) (*autograder_pb.UpdateAssignmentResponse, error) {
	assignmentId := request.GetAssignmentId()
	assignment := request.GetAssignment()
	l := logging.Extract(ctx).With(zap.Uint64("assignmentId", assignmentId))
	l.Debug("UpdateAssignment")
	_, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		return nil, status.Error(codes.NotFound, CodeInvalidAssignmentId)
	}
	err = a.assignmentRepo.UpdateAssignment(ctx, assignmentId, assignment)
	if err != nil {
		l.Error("UpdateAssignment.UpdateAssignment", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeUpdateAssignment)
	}
	go a.pullImage(assignment.GetProgrammingConfig().GetImage())
	return &autograder_pb.UpdateAssignmentResponse{}, nil
}

func (a *AutograderService) pullImage(image string) {
	err := a.progGrader.PullImage(image)
	if err != nil {
		zap.L().Error("PullImage", zap.String("image", image))
	}
}

func (a *AutograderService) CreateAssignment(
	ctx context.Context, request *autograder_pb.CreateAssignmentRequest,
) (*autograder_pb.CreateAssignmentResponse, error) {
	resp := &autograder_pb.CreateAssignmentResponse{}
	assignment := &model_pb.Assignment{}
	assignment.Name = request.GetName()
	assignment.ReleaseDate = request.GetReleaseDate()
	assignment.DueDate = request.GetDueDate()
	assignment.AssignmentType = request.GetAssignmentType()
	assignment.Description = request.GetDescription()
	assignment.CourseId = request.GetCourseId()
	assignment.ProgrammingConfig = request.GetProgrammingConfig()
	assignment.SubmissionLimit = request.GetSubmissionLimit()
	assignment.UploadLimit = request.GetUploadLimit()
	if len(assignment.Name) == 0 || len(assignment.Name) > 256 {
		return nil, status.Error(codes.InvalidArgument, CodeName)
	}
	if len(assignment.Description) == 0 || len(assignment.Description) > 1024*1024 {
		return nil, status.Error(codes.InvalidArgument, CodeDescription)
	}
	if assignment.GetDueDate().AsTime().Before(assignment.GetReleaseDate().AsTime()) {
		return nil, status.Error(codes.InvalidArgument, CodeDueDate)
	}
	assignmentId, err := a.assignmentRepo.CreateAssignment(ctx, assignment)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeCreateAssignment)
	}
	resp.Assignment = assignment
	resp.AssignmentId = assignmentId
	err = a.courseRepo.AddAssignment(ctx, request.GetCourseId(), assignmentId)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeAddAssignment)
	}
	go a.pullImage(request.GetProgrammingConfig().GetImage())
	return resp, nil
}

func (a *AutograderService) GetAssignment(
	ctx context.Context, request *autograder_pb.GetAssignmentRequest,
) (*autograder_pb.GetAssignmentResponse, error) {
	assignment, err := a.assignmentRepo.GetAssignment(ctx, request.GetAssignmentId())
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	anonymous := a.leaderboardRepo.GetLeaderboardAnonymous(ctx, request.GetAssignmentId())
	role := member.GetRole()
	if role == model_pb.CourseRole_Student || role == model_pb.CourseRole_Reader {
		assignment.ProgrammingConfig = nil
	}
	resp := &autograder_pb.GetAssignmentResponse{Assignment: assignment, Role: role, Anonymous: anonymous}
	return resp, err
}

func (a *AutograderService) GetAssignmentsInCourse(
	ctx context.Context, request *autograder_pb.GetAssignmentsInCourseRequest,
) (*autograder_pb.GetAssignmentsInCourseResponse, error) {
	var assignments []*autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	assignmentIds, err := a.courseRepo.GetAssignmentsByCourse(ctx, request.GetCourseId())
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetAssignments)
	}
	for _, asgnId := range assignmentIds {
		assignment, err := a.assignmentRepo.GetAssignment(ctx, asgnId)
		if err != nil {
			return nil, status.Error(codes.Internal, CodeGetAssignment)
		}
		subs, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, user.GetUserId(), asgnId)
		if err != nil {
			return nil, status.Error(codes.Internal, CodeGetSubmissions)
		}
		ret := &autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo{
			AssignmentId: asgnId,
			Name:         assignment.Name,
			ReleaseDate:  assignment.ReleaseDate,
			DueDate:      assignment.DueDate,
			Submitted:    len(subs) > 0,
		}
		assignments = append(assignments, ret)
	}
	response := &autograder_pb.GetAssignmentsInCourseResponse{
		Assignments: assignments,
	}
	return response, nil
}
