package grpc

import (
	"context"
	"fmt"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/logging"
	model_pb "autograder-server/pkg/model/proto"

	middleware "github.com/grpc-ecosystem/go-grpc-middleware/v2"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type userInfoCtxKey struct{}

type courseIdCtxKey struct{}

type submissionIdCtxKey struct{}

type submissionCtxKey struct{}

type assignmentCtxKey struct{}

type courseMemberCtxKey struct{}

type IGetCourseId interface {
	GetCourseId() uint64
}

type IGetAssignmentId interface {
	GetAssignmentId() uint64
}

type IGetSubmissionId interface {
	GetSubmissionId() uint64
}

type IGetCaptcha interface {
	GetCaptcha() string
}

type IGetManifestId interface {
	GetManifestId() uint64
}

type ServiceAuthFunc interface {
	AuthFunc(ctx context.Context, req interface{}, fullMethod string) (context.Context, error)
}

type MethodAuthFunc func(context.Context, interface{}) (context.Context, error)

func (a *AutograderService) NoopAuth(ctx context.Context, req interface{}) (context.Context, error) {
	return ctx, nil
}

func (a *AutograderService) RequireCaptcha(ctx context.Context, req interface{}) (context.Context, error) {
	l := logging.Extract(ctx)
	r, ok := req.(IGetCaptcha)
	if !ok {
		l.Error("RequireCaptcha.NoGetCaptcha")
		return nil, status.Error(codes.InvalidArgument, CodeNoCaptcha)
	}
	captcha := r.GetCaptcha()
	if captcha == "" {
		return nil, status.Error(codes.InvalidArgument, CodeNoCaptcha)
	}
	verifyResp := a.captchaVerifier.VerifyToken(captcha)
	if !verifyResp.Success {
		l.Debug("hCaptcha.Response", zap.Strings("error-codes", verifyResp.ErrorCodes))
		return nil, status.Error(codes.InvalidArgument, CodeInvalidCaptcha)
	}
	return ctx, nil
}

func (a *AutograderService) RequireLogin(ctx context.Context, req interface{}) (context.Context, error) {
	token, err := auth.AuthFromMD(ctx, "bearer")
	if err != nil {
		return nil, err
	}
	payload, err := a.parseTokenPayload(a.userJWTSignKey, token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, CodeInvalidToken)
	}
	payloadPB := &autograder_pb.UserTokenPayload{}
	err = proto.Unmarshal(payload, payloadPB)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, CodeInvalidToken)
	}
	ss, err := a.signPayloadToken(a.userJWTSignKey, payloadPB, time.Now().Add(UserTokenValidDuration))
	if err == nil {
		refreshMD := metadata.Pairs("token", ss)
		if err := grpc.SetHeader(ctx, refreshMD); err != nil {
			// The token is still valid for this call; only the sliding
			// renewal was lost, so log it rather than failing the request.
			logging.Extract(ctx).Error("RequireLogin.SetRefreshHeader", zap.Error(err))
		}
	}
	return context.WithValue(ctx, userInfoCtxKey{}, payloadPB), nil
}

func (a *AutograderService) getCourseIdFromAssignmentId(ctx context.Context, assignmentId uint64) (
	uint64, *model_pb.Assignment, error,
) {
	assignment, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		return 0, assignment, status.Error(codes.NotFound, CodeInvalidAssignmentId)
	}
	return assignment.GetCourseId(), assignment, nil
}

func (a *AutograderService) getAssignmentIdFromSubmissionId(ctx context.Context, submissionId uint64) (
	uint64, *model_pb.Submission, error,
) {
	submission, err := a.submissionRepo.GetSubmission(ctx, submissionId)
	if err != nil {
		return 0, submission, status.Error(codes.NotFound, CodeInvalidSubmissionId)
	}
	return submission.GetAssignmentId(), submission, nil
}

func (a *AutograderService) GetCourseId(ctx context.Context, req interface{}) (context.Context, error) {
	courseId := uint64(0)
	var err error
	var assignment *model_pb.Assignment
	if r, ok := (req).(IGetCourseId); ok {
		courseId = r.GetCourseId()
	} else if r, ok := (req).(IGetAssignmentId); ok {
		courseId, assignment, err = a.getCourseIdFromAssignmentId(ctx, r.GetAssignmentId())
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, assignmentCtxKey{}, assignment)
	} else if r, ok := (req).(IGetSubmissionId); ok {
		ctx = context.WithValue(ctx, submissionIdCtxKey{}, r.GetSubmissionId())
		assignmentId, submission, err := a.getAssignmentIdFromSubmissionId(ctx, r.GetSubmissionId())
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, submissionCtxKey{}, submission)
		courseId, assignment, err = a.getCourseIdFromAssignmentId(ctx, assignmentId)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, assignmentCtxKey{}, assignment)
	}
	if courseId == 0 {
		return nil, status.Error(codes.InvalidArgument, CodeMissingCourseId)
	}
	return context.WithValue(ctx, courseIdCtxKey{}, courseId), nil
}

func (a *AutograderService) RequireInCourse(ctx context.Context, req interface{}) (context.Context, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	userId := user.GetUserId()
	member := a.userRepo.GetCourseMember(ctx, userId, courseId)
	if member == nil {
		return nil, status.Error(codes.PermissionDenied, CodeNotInCourse)
	}
	return context.WithValue(ctx, courseMemberCtxKey{}, member), nil
}

func (a *AutograderService) RequireAdmin(ctx context.Context, req interface{}) (context.Context, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	if !user.IsAdmin {
		return nil, status.Error(codes.PermissionDenied, CodeNotAdmin)
	}
	return ctx, nil
}

func (a *AutograderService) CheckManifest(ctx context.Context, req interface{}) (context.Context, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	manifestReq, ok := req.(IGetManifestId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, CodeManifestId)
	}
	manifest, err := a.manifestRepo.GetManifest(ctx, manifestReq.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.DeadlineExceeded, CodeManifestExpired)
	}
	if manifest.GetUserId() != user.GetUserId() {
		return nil, status.Error(codes.PermissionDenied, CodeWrongUser)
	}
	return ctx, nil
}

func (a *AutograderService) RequireCourseWrite(ctx context.Context, req interface{}) (context.Context, error) {
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	if member.GetRole() == model_pb.CourseRole_Reader || member.GetRole() == model_pb.CourseRole_Student {
		return nil, status.Error(codes.PermissionDenied, CodeRoleUnauthorized)
	}
	return ctx, nil
}

func (a *AutograderService) RequireSubmissionRead(ctx context.Context, req interface{}) (context.Context, error) {
	submission, ok := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)
	if !ok {
		return nil, status.Error(codes.PermissionDenied, CodeSubmissionUnauthorized)
	}
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	for _, submitter := range submission.GetSubmitters() {
		if submitter == user.GetUserId() {
			return ctx, nil
		}
	}
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	if member.GetRole() == model_pb.CourseRole_TA || member.GetRole() == model_pb.CourseRole_Instructor {
		return ctx, nil
	}
	return nil, status.Error(codes.PermissionDenied, CodeSubmissionUnauthorized)
}

func (a *AutograderService) NotAfterDueDate(ctx context.Context, req interface{}) (context.Context, error) {
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	if member.GetRole() == model_pb.CourseRole_Instructor || member.GetRole() == model_pb.CourseRole_TA {
		return ctx, nil
	}
	assignment := ctx.Value(assignmentCtxKey{}).(*model_pb.Assignment)
	now := time.Now()
	if now.After(assignment.GetDueDate().AsTime()) {
		return ctx, status.Error(codes.PermissionDenied, CodeAfterDueDate)
	}
	if now.Before(assignment.GetReleaseDate().AsTime()) {
		return ctx, status.Error(codes.PermissionDenied, CodeBeforeReleaseDate)
	}
	return ctx, nil
}

// initAuthFuncs builds the method -> auth chain table. Keys are the generated
// full method names, so a renamed or removed RPC fails to compile here instead
// of silently dropping out of the table; TestAuthTableCoversEveryMethod checks
// the converse, that every RPC in the service descriptor has an entry.
func (a *AutograderService) initAuthFuncs() {
	a.authFuncs = map[string][]MethodAuthFunc{}
	authMaps := []struct {
		Methods   []string
		AuthFuncs []MethodAuthFunc
	}{
		{
			Methods: []string{
				autograder_pb.AutograderService_Login_FullMethodName,
				autograder_pb.AutograderService_SignUp_FullMethodName,
				autograder_pb.AutograderService_ResetPassword_FullMethodName,
				autograder_pb.AutograderService_GithubLogin_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.NoopAuth},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_RequestPasswordReset_FullMethodName,
				autograder_pb.AutograderService_RequestSignUpToken_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireCaptcha},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_GetCourseList_FullMethodName,
				autograder_pb.AutograderService_GetUser_FullMethodName,
				autograder_pb.AutograderService_BindGithub_FullMethodName,
				autograder_pb.AutograderService_UnbindGithub_FullMethodName,
				autograder_pb.AutograderService_UpdateUser_FullMethodName,
				autograder_pb.AutograderService_UpdatePassword_FullMethodName,
				autograder_pb.AutograderService_JoinCourse_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_InitUpload_FullMethodName,
				autograder_pb.AutograderService_DeleteFileInManifest_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.CheckManifest},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_GetAllGraders_FullMethodName,
				autograder_pb.AutograderService_SearchUser_FullMethodName,
				autograder_pb.AutograderService_SetAdmin_FullMethodName,
				autograder_pb.AutograderService_CreateCourse_FullMethodName,
				autograder_pb.AutograderService_GetAllUsers_FullMethodName,
				autograder_pb.AutograderService_GetAllCourses_FullMethodName,
				// Declared in proto since 2022 but never implemented (calls
				// hit the embedded Unimplemented server). Registered so the
				// table stays exhaustive and they are protected if implemented.
				autograder_pb.AutograderService_RemoveGrader_FullMethodName,
				autograder_pb.AutograderService_GetGradeQueue_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.RequireAdmin},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_GetAssignment_FullMethodName,
				autograder_pb.AutograderService_GetAssignmentsInCourse_FullMethodName,
				autograder_pb.AutograderService_GetSubmissionsInAssignment_FullMethodName,
				autograder_pb.AutograderService_GetLeaderboard_FullMethodName,
				autograder_pb.AutograderService_HasLeaderboard_FullMethodName,
				autograder_pb.AutograderService_GetCourse_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_CreateSubmission_FullMethodName,
				autograder_pb.AutograderService_CreateManifest_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.NotAfterDueDate},
		},
		{

			Methods: []string{
				autograder_pb.AutograderService_CreateAssignment_FullMethodName,
				autograder_pb.AutograderService_GetCourseMembers_FullMethodName,
				autograder_pb.AutograderService_RemoveCourseMembers_FullMethodName,
				autograder_pb.AutograderService_AddCourseMembers_FullMethodName,
				autograder_pb.AutograderService_UpdateCourse_FullMethodName,
				autograder_pb.AutograderService_UpdateAssignment_FullMethodName,
				autograder_pb.AutograderService_UpdateCourseMember_FullMethodName,
				autograder_pb.AutograderService_CanWriteCourse_FullMethodName,
				autograder_pb.AutograderService_GenerateJoinCode_FullMethodName,
				autograder_pb.AutograderService_ChangeAllowsJoinCourse_FullMethodName,
				autograder_pb.AutograderService_InspectAllSubmissionsInAssignment_FullMethodName,
				autograder_pb.AutograderService_InspectUserSubmissionHistory_FullMethodName,
				autograder_pb.AutograderService_RegradeSubmission_FullMethodName,
				autograder_pb.AutograderService_RegradeAssignment_FullMethodName,
				autograder_pb.AutograderService_ChangeLeaderboardAnonymous_FullMethodName,
				autograder_pb.AutograderService_ExportAssignmentGrades_FullMethodName,
				autograder_pb.AutograderService_DeleteLeaderboard_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.RequireCourseWrite},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_InitDownload_FullMethodName,
				autograder_pb.AutograderService_GetFilesInSubmission_FullMethodName,
				autograder_pb.AutograderService_GetSubmissionReport_FullMethodName,
				autograder_pb.AutograderService_SubscribeSubmission_FullMethodName,
				autograder_pb.AutograderService_CancelSubmission_FullMethodName,
				autograder_pb.AutograderService_StreamLog_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.RequireSubmissionRead},
		},
		{
			Methods: []string{
				autograder_pb.AutograderService_ActivateSubmission_FullMethodName,
			},
			AuthFuncs: []MethodAuthFunc{
				a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.RequireSubmissionRead, a.NotAfterDueDate,
			},
		},
	}

	for _, authMap := range authMaps {
		for _, method := range authMap.Methods {
			if _, dup := a.authFuncs[method]; dup {
				panic(fmt.Sprintf("auth chain for %s registered twice", method))
			}
			a.authFuncs[method] = authMap.AuthFuncs
		}
	}
}

func (a *AutograderService) AuthFunc(ctx context.Context, req interface{}, fullMethodName string) (
	context.Context, error,
) {
	authFuncs := a.authFuncs[fullMethodName]
	if authFuncs == nil {
		return ctx, status.Error(codes.NotFound, CodeMethodAuth)
	}
	var err error
	for _, authFunc := range authFuncs {
		ctx, err = authFunc(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	return ctx, nil
}

func UnaryAuth() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (interface{}, error) {
		srv := info.Server.(ServiceAuthFunc)
		var err error
		ctx, err = srv.AuthFunc(ctx, req, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func StreamAuth() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()

		wrapped := middleware.WrapServerStream(ss)
		wrapped.WrappedContext = ctx
		return handler(srv, wrapped)
	}
}
