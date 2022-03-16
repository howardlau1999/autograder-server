package grpc

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	model_pb "autograder-server/pkg/model/proto"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/auth"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const ServerPrefix = "/AutograderService/"

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

func getFullName(method string) string {
	return fmt.Sprintf("%s%s", ServerPrefix, method)
}

func (a *AutograderService) NoopAuth(ctx context.Context, req interface{}) (context.Context, error) {
	return ctx, nil
}

func (a *AutograderService) RequireCaptcha(ctx context.Context, req interface{}) (context.Context, error) {
	l := ctxzap.Extract(ctx)
	r, ok := req.(IGetCaptcha)
	if !ok {
		l.Error("RequireCaptcha.NoGetCaptcha")
		return nil, status.Error(codes.InvalidArgument, "NO_CAPTCHA")
	}
	captcha := r.GetCaptcha()
	if captcha == "" {
		return nil, status.Error(codes.InvalidArgument, "NO_CAPTCHA")
	}
	verifyResp := a.captchaVerifier.VerifyToken(captcha)
	if !verifyResp.Success {
		l.Debug("hCaptcha.Response", zap.Strings("error-codes", verifyResp.ErrorCodes))
		return nil, status.Error(codes.InvalidArgument, "INVALID_CAPTCHA")
	}
	return ctx, nil
}

func (a *AutograderService) RequireLogin(ctx context.Context, req interface{}) (context.Context, error) {
	token, err := grpc_auth.AuthFromMD(ctx, "bearer")
	if err != nil {
		return nil, err
	}
	payload, err := a.parseTokenPayload(UserJWTSignKey, token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "INVALID_TOKEN")
	}
	payloadPB := &autograder_pb.UserTokenPayload{}
	err = proto.Unmarshal(payload, payloadPB)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "INVALID_TOKEN")
	}
	ss, err := a.signPayloadToken(UserJWTSignKey, payloadPB, time.Now().Add(3*time.Hour))
	if err == nil {
		refreshMD := metadata.Pairs("token", ss)
		_ = grpc.SetHeader(ctx, refreshMD)
	}
	return context.WithValue(ctx, userInfoCtxKey{}, payloadPB), nil
}

func (a *AutograderService) getCourseIdFromAssignmentId(ctx context.Context, assignmentId uint64) (
	uint64, *model_pb.Assignment, error,
) {
	assignment, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		return 0, assignment, status.Error(codes.NotFound, "INVALID_ASSIGNMENT_ID")
	}
	return assignment.GetCourseId(), assignment, nil
}

func (a *AutograderService) getAssignmentIdFromSubmissionId(ctx context.Context, submissionId uint64) (
	uint64, *model_pb.Submission, error,
) {
	submission, err := a.submissionRepo.GetSubmission(ctx, submissionId)
	if err != nil {
		return 0, submission, status.Error(codes.NotFound, "INVALID_SUBMISSION_ID")
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
		return nil, status.Error(codes.InvalidArgument, "MISSING_COURSE_ID")
	}
	return context.WithValue(ctx, courseIdCtxKey{}, courseId), nil
}

func (a *AutograderService) RequireInCourse(ctx context.Context, req interface{}) (context.Context, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	userId := user.GetUserId()
	member := a.userRepo.GetCourseMember(ctx, userId, courseId)
	if member == nil {
		return nil, status.Error(codes.PermissionDenied, "NOT_IN_COURSE")
	}
	return context.WithValue(ctx, courseMemberCtxKey{}, member), nil
}

func (a *AutograderService) RequireAdmin(ctx context.Context, req interface{}) (context.Context, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	if !user.IsAdmin {
		return nil, status.Error(codes.PermissionDenied, "NOT_ADMIN")
	}
	return ctx, nil
}

func (a *AutograderService) CheckManifest(ctx context.Context, req interface{}) (context.Context, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	manifestReq, ok := req.(IGetManifestId)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "MANIFEST_ID")
	}
	manifest, err := a.manifestRepo.GetManifest(ctx, manifestReq.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.DeadlineExceeded, "MANIFEST_EXPIRED")
	}
	if manifest.GetUserId() != user.GetUserId() {
		return nil, status.Error(codes.PermissionDenied, "WRONG_USER")
	}
	return ctx, nil
}

func (a *AutograderService) RequireCourseWrite(ctx context.Context, req interface{}) (context.Context, error) {
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	if member.GetRole() == model_pb.CourseRole_Reader || member.GetRole() == model_pb.CourseRole_Student {
		return nil, status.Error(codes.PermissionDenied, "ROLE_UNAUTHORIZED")
	}
	return ctx, nil
}

func (a *AutograderService) RequireSubmissionRead(ctx context.Context, req interface{}) (context.Context, error) {
	submission, ok := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)
	if !ok {
		return nil, status.Error(codes.PermissionDenied, "SUBMISSION_UNAUTHORIZED")
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
	return nil, status.Error(codes.PermissionDenied, "SUBMISSION_UNAUTHORIZED")
}

func (a *AutograderService) NotAfterDueDate(ctx context.Context, req interface{}) (context.Context, error) {
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	if member.GetRole() == model_pb.CourseRole_Instructor || member.GetRole() == model_pb.CourseRole_TA {
		return ctx, nil
	}
	assignment := ctx.Value(assignmentCtxKey{}).(*model_pb.Assignment)
	now := time.Now()
	if now.After(assignment.GetDueDate().AsTime()) {
		return ctx, status.Error(codes.PermissionDenied, "AFTER_DUE_DATE")
	}
	if now.Before(assignment.GetReleaseDate().AsTime()) {
		return ctx, status.Error(codes.PermissionDenied, "BEFORE_RELEASE_DATE")
	}
	return ctx, nil
}

func getFunctionName(f interface{}) string {
	name := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	parts := strings.Split(name, ".")
	name = parts[len(parts)-1]
	return name
}

func (a *AutograderService) initAuthFuncs() {
	a.authFuncs = map[string][]MethodAuthFunc{}
	authMaps := []struct {
		Methods   []interface{}
		AuthFuncs []MethodAuthFunc
	}{
		{
			Methods: []interface{}{
				(*AutograderService).Login,
				(*AutograderService).SignUp,
				(*AutograderService).ResetPassword,
				(*AutograderService).GithubLogin,
			},
			AuthFuncs: []MethodAuthFunc{a.NoopAuth},
		},
		{
			Methods: []interface{}{
				(*AutograderService).RequestPasswordReset,
				(*AutograderService).RequestSignUpToken,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireCaptcha},
		},
		{
			Methods: []interface{}{
				(*AutograderService).GetCourseList,
				(*AutograderService).GetUser,
				(*AutograderService).BindGithub,
				(*AutograderService).UnbindGithub,
				(*AutograderService).UpdateUser,
				(*AutograderService).UpdatePassword,
				(*AutograderService).JoinCourse,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin},
		},
		{
			Methods: []interface{}{
				(*AutograderService).InitUpload,
				(*AutograderService).DeleteFileInManifest,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.CheckManifest},
		},
		{
			Methods: []interface{}{
				(*AutograderService).GetAllGraders,
				(*AutograderService).SearchUser,
				(*AutograderService).SetAdmin,
				(*AutograderService).CreateCourse,
				(*AutograderService).GetAllUsers,
				(*AutograderService).GetAllCourses,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.RequireAdmin},
		},
		{
			Methods: []interface{}{
				(*AutograderService).GetAssignment,
				(*AutograderService).GetAssignmentsInCourse,
				(*AutograderService).GetSubmissionsInAssignment,
				(*AutograderService).GetLeaderboard,
				(*AutograderService).HasLeaderboard,
				(*AutograderService).GetCourse,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse},
		},
		{
			Methods: []interface{}{
				(*AutograderService).CreateSubmission,
				(*AutograderService).CreateManifest,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.NotAfterDueDate},
		},
		{

			Methods: []interface{}{
				(*AutograderService).CreateAssignment,
				(*AutograderService).GetCourseMembers,
				(*AutograderService).RemoveCourseMembers,
				(*AutograderService).AddCourseMembers,
				(*AutograderService).UpdateCourse,
				(*AutograderService).UpdateAssignment,
				(*AutograderService).UpdateCourseMember,
				(*AutograderService).CanWriteCourse,
				(*AutograderService).GenerateJoinCode,
				(*AutograderService).ChangeAllowsJoinCourse,
				(*AutograderService).InspectAllSubmissionsInAssignment,
				(*AutograderService).InspectUserSubmissionHistory,
				(*AutograderService).RegradeSubmission,
				(*AutograderService).RegradeAssignment,
				(*AutograderService).ChangeLeaderboardAnonymous,
				(*AutograderService).ExportAssignmentGrades,
				(*AutograderService).DeleteLeaderboard,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.RequireCourseWrite},
		},
		{
			Methods: []interface{}{
				(*AutograderService).InitDownload,
				(*AutograderService).GetFilesInSubmission,
				(*AutograderService).GetSubmissionReport,
				(*AutograderService).SubscribeSubmission,
				(*AutograderService).CancelSubmission,
				(*AutograderService).StreamLog,
			},
			AuthFuncs: []MethodAuthFunc{a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.RequireSubmissionRead},
		},
		{
			Methods: []interface{}{
				(*AutograderService).ActivateSubmission,
			},
			AuthFuncs: []MethodAuthFunc{
				a.RequireLogin, a.GetCourseId, a.RequireInCourse, a.RequireSubmissionRead, a.NotAfterDueDate,
			},
		},
	}

	for _, authMap := range authMaps {
		for _, method := range authMap.Methods {
			a.authFuncs[getFullName(getFunctionName(method))] = authMap.AuthFuncs
		}
	}
}

func (a *AutograderService) AuthFunc(ctx context.Context, req interface{}, fullMethodName string) (
	context.Context, error,
) {
	authFuncs := a.authFuncs[fullMethodName]
	if authFuncs == nil {
		return ctx, status.Error(codes.NotFound, "METHOD_AUTH")
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

		wrapped := grpc_middleware.WrapServerStream(ss)
		wrapped.WrappedContext = ctx
		return handler(srv, wrapped)
	}
}
