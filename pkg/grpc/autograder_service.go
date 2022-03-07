package grpc

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/mail"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/grader"
	grader_grpc "autograder-server/pkg/grader/grpc"
	grader_pb "autograder-server/pkg/grader/proto"
	"autograder-server/pkg/mailer"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"autograder-server/pkg/storage"
	"autograder-server/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/cockroachdb/pebble"
	"github.com/go-chi/chi"
	"github.com/gogo/protobuf/sortkeys"
	"github.com/golang-jwt/jwt"
	"github.com/google/go-github/v42/github"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	"github.com/kataras/hcaptcha"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AutograderService struct {
	autograder_pb.UnimplementedAutograderServiceServer
	manifestRepo         repository.ManifestRepository
	userRepo             repository.UserRepository
	submissionRepo       repository.SubmissionRepository
	submissionReportRepo repository.SubmissionReportRepository
	assignmentRepo       repository.AssignmentRepository
	courseRepo           repository.CourseRepository
	leaderboardRepo      repository.LeaderboardRepository
	verificationCodeRepo repository.VerificationCodeRepository
	progGrader           grader.ProgrammingGrader
	mailer               mailer.Mailer
	ls                   *storage.LocalStorage
	captchaVerifier      *hcaptcha.Client
	reportSubs           map[uint64][]chan *grader_pb.GradeReport
	subsMu               *sync.Mutex
	authFuncs            map[string][]MethodAuthFunc
	githubOAuth2Config   *oauth2.Config
	graderHubSvc         *grader_grpc.GraderHubService
}

var DownloadJWTSignKey = []byte("download-token-sign-key")
var UploadJWTSignKey = []byte("upload-token-sign-key")
var UserJWTSignKey = []byte("user-token-sign-key")
var ResetCodeMax = 900000

const MaxMultipartFormParseMemory = 10 * 1024 * 1024
const PasswordResetSubject = "Autograder 密码重置验证码"
const PasswordResetTemplate = "您的密码重置验证码为：%s，10 分钟内有效。\nAutograder"
const PasswordResetRepoType = "password_reset"
const SignUpSubject = "Autograder 注册验证码"
const SignUpTemplate = "您的注册验证码为：%s，10 分钟内有效。\nAutograder"
const SignUpRepoType = "sign_up"

var UsernameRegExp = regexp.MustCompile("^[a-zA-Z][a-zA-Z0-9_-]{3,20}$")
var EmailCodeValidDuration = 10 * time.Minute

func (a *AutograderService) sendEmailCode(to string, subject string, code string, template string) error {
	return a.mailer.SendMail(
		viper.GetString("smtp.from"),
		[]string{to},
		subject,
		fmt.Sprintf(template, code),
	)
}

func (a *AutograderService) validateEmailCode(ctx context.Context, typ, email, code string) error {
	valid, err := a.verificationCodeRepo.Validate(ctx, typ, email, code)
	if !valid || err != nil {
		return status.Error(codes.InvalidArgument, "CODE")
	}
	return nil
}

func (a *AutograderService) CanWriteCourse(
	ctx context.Context, request *autograder_pb.CanWriteCourseRequest,
) (*autograder_pb.CanWriteCourseResponse, error) {
	return &autograder_pb.CanWriteCourseResponse{WritePermission: true}, nil
}

func (a *AutograderService) ResetPassword(
	ctx context.Context, request *autograder_pb.ResetPasswordRequest,
) (*autograder_pb.ResetPasswordResponse, error) {
	l := ctxzap.Extract(ctx)
	email := request.GetEmail()
	code := request.GetCode()
	l.Debug("ResetPassword", zap.String("email", email), zap.String("code", code))
	if err := a.validateEmailCode(ctx, PasswordResetRepoType, email, code); err != nil {
		return nil, err
	}
	user, userId, err := a.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		l.Error("ResetPassword.GetUser", zap.String("email", email), zap.Error(err))
		return nil, status.Error(codes.NotFound, "EMAIL")
	}
	if len(request.GetPassword()) < 8 {
		return nil, status.Error(codes.InvalidArgument, "PASSWORD")
	}
	user.Password, err = bcrypt.GenerateFromPassword([]byte(request.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		l.Error("ResetPassword.Hash", zap.String("email", email), zap.Error(err))
		return nil, status.Error(codes.Internal, "INTERNAL_ERROR")
	}
	err = a.userRepo.UpdateUser(ctx, userId, user)
	if err != nil {
		l.Error("ResetPassword.UpdateUser", zap.String("email", email), zap.Error(err))
		return nil, status.Error(codes.Internal, "INTERNAL_ERROR")
	}
	return &autograder_pb.ResetPasswordResponse{}, nil
}

func isMailValid(email string) bool {
	_, err := mail.ParseAddress(email)
	return err == nil
}

func isUsernameValid(username string) bool {
	return UsernameRegExp.MatchString(username)
}

func (a *AutograderService) requestEmailCode(ctx context.Context, subject, email string, typ string, template string) {
	if !isMailValid(email) {
		return
	}
	l := ctxzap.Extract(ctx).With(zap.String("email", email))
	codeInt := rand.Intn(ResetCodeMax)
	codeInt += 100000
	code := fmt.Sprintf("%d", codeInt)
	l.Debug("RequestEmailCode.CodeGenerated", zap.String("code", code))
	err := a.verificationCodeRepo.Issue(ctx, typ, email, code, time.Now().Add(EmailCodeValidDuration))
	if err != nil {
		l.Error("RequestEmailCode.Issue", zap.Error(err))
		return
	}
	go func() {
		err := retry.Do(
			func() error {
				err := a.sendEmailCode(email, subject, code, template)
				if err == nil {
					l.Debug("RequestEmailCode.MailSent")
				}
				return err
			}, retry.Attempts(3),
		)
		if err != nil {
			l.Error("RequestEmailCode.SendMail", zap.Error(err))
		}
	}()
}

func (a *AutograderService) validateUsernameAndEmail(ctx context.Context, username string, email string) error {
	if !isUsernameValid(username) {
		return status.Error(codes.InvalidArgument, "USERNAME")
	}
	if !isMailValid(email) {
		return status.Error(codes.InvalidArgument, "EMAIL")
	}
	userId, _ := a.userRepo.GetUserIdByEmail(ctx, email)
	if userId != 0 {
		return status.Error(codes.AlreadyExists, "EMAIL")
	}
	userId, _ = a.userRepo.GetUserIdByUsername(ctx, username)
	if userId != 0 {
		return status.Error(codes.AlreadyExists, "USERNAME")
	}
	return nil
}

func (a *AutograderService) signUpNewUser(ctx context.Context, email, username string, password string) (
	uint64, error,
) {
	l := ctxzap.Extract(ctx)
	if err := a.validateUsernameAndEmail(ctx, username, email); err != nil {
		return 0, err
	}
	var passwordHash []byte
	var err error
	if len(password) > 0 {
		passwordHash, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			l.Error("SignUp", zap.Error(err))
			return 0, status.Error(codes.Internal, "PASSWORD_HASH")
		}
	}
	user := &model_pb.User{
		Username: username,
		Password: passwordHash,
		Email:    email,
		Nickname: username,
	}
	userId, err := a.userRepo.CreateUser(ctx, user)
	if err != nil {
		l.Error("SignUp.CreateUser", zap.Error(err))
		return 0, status.Error(codes.Internal, "CREATE_USER")
	}
	return userId, nil
}

func (a *AutograderService) SignUp(
	ctx context.Context, request *autograder_pb.SignUpRequest,
) (*autograder_pb.SignUpResponse, error) {
	if err := a.validateEmailCode(ctx, SignUpRepoType, request.GetEmail(), request.GetCode()); err != nil {
		return nil, err
	}
	if len(request.GetPassword()) < 8 {
		return nil, status.Error(codes.InvalidArgument, "PASSWORD")
	}
	userId, err := a.signUpNewUser(ctx, request.GetEmail(), request.GetUsername(), request.GetPassword())
	if err != nil {
		return nil, err
	}
	return &autograder_pb.SignUpResponse{UserId: userId}, nil
}

func (a *AutograderService) RequestSignUpToken(
	ctx context.Context, request *autograder_pb.RequestSignUpTokenRequest,
) (*autograder_pb.RequestSignUpTokenResponse, error) {
	if err := a.validateUsernameAndEmail(ctx, request.GetUsername(), request.GetEmail()); err != nil {
		return nil, err
	}
	a.requestEmailCode(ctx, SignUpSubject, request.GetEmail(), SignUpRepoType, SignUpTemplate)
	return &autograder_pb.RequestSignUpTokenResponse{}, nil
}

func (a *AutograderService) RequestPasswordReset(
	ctx context.Context, request *autograder_pb.RequestPasswordResetRequest,
) (*autograder_pb.RequestPasswordResetResponse, error) {
	l := ctxzap.Extract(ctx)
	email := request.GetEmail()
	userId, _ := a.userRepo.GetUserIdByEmail(ctx, email)
	if userId == 0 {
		return nil, nil
	}
	l.Debug("RequestPasswordReset", zap.String("email", email))
	a.requestEmailCode(ctx, PasswordResetSubject, email, PasswordResetRepoType, PasswordResetTemplate)
	return &autograder_pb.RequestPasswordResetResponse{}, nil
}

func (a *AutograderService) UpdateCourseMember(
	ctx context.Context, request *autograder_pb.UpdateCourseMemberRequest,
) (*autograder_pb.UpdateCourseMemberResponse, error) {
	courseId := request.GetCourseId()
	member := request.GetMember()
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId), zap.Uint64("userId", member.GetUserId()))
	l.Debug("UpdateCourseMember")
	dbMember := a.userRepo.GetCourseMember(ctx, member.GetUserId(), courseId)
	if dbMember == nil {
		return nil, status.Error(codes.NotFound, "MEMBER_NOT_FOUND")
	}
	dbMember.Role = member.GetRole()
	err := a.userRepo.AddCourse(ctx, dbMember)
	if err != nil {
		l.Error("UpdateCourseMember.AddCourse", zap.Error(err))
		return nil, status.Error(codes.InvalidArgument, "ADD_COURSE")
	}
	err = a.courseRepo.AddUser(ctx, dbMember)
	if err != nil {
		l.Error("UpdateCourseMember.AddUser", zap.Error(err))
		return nil, status.Error(codes.InvalidArgument, "ADD_USER")
	}
	return &autograder_pb.UpdateCourseMemberResponse{}, nil
}

func (a *AutograderService) UpdateCourse(
	ctx context.Context, request *autograder_pb.UpdateCourseRequest,
) (*autograder_pb.UpdateCourseResponse, error) {
	courseId := request.GetCourseId()

	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId))
	l.Debug("UpdateCourse")
	course, err := a.courseRepo.GetCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "INVALID_COURSE_ID")
	}
	course.Description = request.GetDescription()
	course.Name = request.GetName()
	course.ShortName = request.GetShortName()
	err = a.courseRepo.UpdateCourse(ctx, courseId, course)
	if err != nil {
		l.Error("UpdateCourse.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "UPDATE_COURSE")
	}
	return &autograder_pb.UpdateCourseResponse{}, nil
}

func (a *AutograderService) UpdateAssignment(
	ctx context.Context, request *autograder_pb.UpdateAssignmentRequest,
) (*autograder_pb.UpdateAssignmentResponse, error) {
	assignmentId := request.GetAssignmentId()
	assignment := request.GetAssignment()
	l := ctxzap.Extract(ctx).With(zap.Uint64("assignmentId", assignmentId))
	l.Debug("UpdateAssignment")
	_, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "INVALID_ASSIGNMENT_ID")
	}
	err = a.assignmentRepo.UpdateAssignment(ctx, assignmentId, assignment)
	if err != nil {
		l.Error("UpdateAssignment.UpdateAssignment", zap.Error(err))
		return nil, status.Error(codes.Internal, "UPDATE_ASSIGNMENT")
	}
	go a.pullImage(assignment.GetProgrammingConfig().GetImage())
	return &autograder_pb.UpdateAssignmentResponse{}, nil
}

func (a *AutograderService) AddCourseMembers(
	ctx context.Context, request *autograder_pb.AddCourseMembersRequest,
) (*autograder_pb.AddCourseMembersResponse, error) {
	courseId := request.GetCourseId()
	membersToAdd := request.GetMembers()
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId))
	var added []*model_pb.CourseMember
	for _, memberToAdd := range membersToAdd {
		var err error
		userId, _ := a.userRepo.GetUserIdByEmail(ctx, memberToAdd.GetEmail())
		l.Debug(
			"AddCourseMember.GetUserIdByEmail", zap.Uint64("userId", userId),
			zap.String("email", memberToAdd.GetEmail()),
		)
		if userId == 0 {
			newUsername := memberToAdd.GetName()
			if len(newUsername) < 3 {
				continue
			}
			existId, err := a.userRepo.GetUserIdByUsername(ctx, newUsername)
			if existId != 0 {
				continue
			}
			newUser := &model_pb.User{Email: memberToAdd.GetEmail(), Username: newUsername, Nickname: newUsername}
			userId, err = a.userRepo.CreateUser(ctx, newUser)
			l.Debug(
				"AddCourseMember.CreateUser", zap.String("email", memberToAdd.GetEmail()),
				zap.String("username", newUsername), zap.Uint64("userId", userId),
			)
			if err != nil {
				l.Error("AddCourseMember.CreateUser", zap.Error(err))
				continue
			}
		}
		member := a.userRepo.GetCourseMember(ctx, userId, courseId)
		if member != nil {
			continue
		}
		newMember := &model_pb.CourseMember{
			UserId:   userId,
			CourseId: courseId,
			Role:     memberToAdd.GetRole(),
		}
		ml := l.With(zap.Uint64("userId", userId), zap.Stringer("role", memberToAdd.GetRole()))
		err = a.userRepo.AddCourse(ctx, newMember)
		if err != nil {
			ml.Error("AddCourseMembers.AddCourse", zap.Error(err))
			continue
		}
		err = a.courseRepo.AddUser(ctx, newMember)
		if err != nil {
			ml.Error("AddCourseMembers.AddUser", zap.Error(err))
			continue
		}
		added = append(added, newMember)
	}
	resp := &autograder_pb.AddCourseMembersResponse{Added: added}
	return resp, nil
}

func (a *AutograderService) RemoveCourseMembers(
	ctx context.Context, request *autograder_pb.RemoveCourseMembersRequest,
) (*autograder_pb.RemoveCourseMembersResponse, error) {
	currentUserId := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload).GetUserId()
	membersToRemove := request.GetUserIds()
	courseId := request.GetCourseId()
	var removed []uint64
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId))
	for _, memberToRemove := range membersToRemove {
		if currentUserId == memberToRemove {
			continue
		}
		ml := l.With(zap.Uint64("userId", memberToRemove))
		err := a.userRepo.RemoveCourseMember(ctx, memberToRemove, courseId)
		if err != nil {
			ml.Error("RemoveCourseMembers.RemoveCourseMember", zap.Error(err))
		}
		err = a.courseRepo.RemoveUser(ctx, courseId, memberToRemove)
		if err != nil {
			ml.Error("RemoveCourseMembers.RemoveUser", zap.Error(err))
		}
		removed = append(removed, memberToRemove)
	}
	resp := &autograder_pb.RemoveCourseMembersResponse{Removed: removed}
	return resp, nil
}

func (a *AutograderService) GetCourseMembers(
	ctx context.Context, request *autograder_pb.GetCourseMembersRequest,
) (*autograder_pb.GetCourseMembersResponse, error) {
	l := ctxzap.Extract(ctx)
	courseId := request.GetCourseId()
	members, err := a.courseRepo.GetUsersByCourse(ctx, request.GetCourseId())
	if err != nil {
		l.Error("GetCourseMembers.GetUsersByCourse", zap.Uint64("courseId", courseId), zap.Error(err))
		return nil, status.Error(codes.Internal, "GET_USERS")
	}
	var respMembers []*autograder_pb.GetCourseMembersResponse_MemberInfo
	resp := &autograder_pb.GetCourseMembersResponse{}
	for _, member := range members {
		userId := member.GetUserId()
		user, err := a.userRepo.GetUserById(ctx, userId)
		if err != nil {
			l.Error("GetCourseMembers.GetUserById", zap.Uint64("userId", userId), zap.Error(err))
		}
		respMembers = append(
			respMembers, &autograder_pb.GetCourseMembersResponse_MemberInfo{
				Username:  user.GetUsername(),
				UserId:    userId,
				Role:      member.GetRole(),
				Email:     user.GetEmail(),
				Nickname:  user.GetNickname(),
				StudentId: user.GetStudentId(),
				GithubId:  user.GetGithubId(),
			},
		)
	}
	resp.Members = respMembers
	return resp, nil
}

func (a *AutograderService) InitDownload(
	ctx context.Context, request *autograder_pb.InitDownloadRequest,
) (*autograder_pb.InitDownloadResponse, error) {
	submission := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)
	if request.GetIsDirectory() {
		fn := fmt.Sprintf("submission_%d.zip", request.GetSubmissionId())
		payloadPB := &autograder_pb.DownloadTokenPayload{
			RealPath: submission.GetPath(), Filename: fn, IsDirectory: true, SubmissionId: request.GetSubmissionId(),
		}
		ss, err := a.signPayloadToken(DownloadJWTSignKey, payloadPB, time.Now().Add(1*time.Minute))
		if err != nil {
			return nil, status.Error(codes.Internal, "SIGN_JWT")
		}
		resp := &autograder_pb.InitDownloadResponse{Token: ss, Filename: fn}
		return resp, nil
	}
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	realpath := path.Join(submission.GetPath(), filename)
	if request.GetIsOutput() {
		realpath = path.Join(fmt.Sprintf("runs/submissions/%d/results/outputs", request.GetSubmissionId()), filename)
	}
	_, fn := path.Split(filename)
	file, err := a.ls.Open(ctx, realpath)
	if err != nil {
		return nil, status.Error(codes.Internal, "OPEN_FILE")
	}
	defer file.Close()
	size, err := a.ls.Size(ctx, realpath)
	if err != nil {
		return nil, status.Error(codes.Internal, "FILE_SIZE")
	}
	head := make([]byte, 512)
	file.Read(head)
	fileType := http.DetectContentType(head)
	fileTypePB := autograder_pb.DownloadFileType_Binary
	if fileType == "application/pdf" {
		fileTypePB = autograder_pb.DownloadFileType_PDF
	} else if strings.HasPrefix(fileType, "image/") {
		fileTypePB = autograder_pb.DownloadFileType_Image
	} else if strings.HasPrefix(fileType, "text/plain") {
		fileTypePB = autograder_pb.DownloadFileType_Text
	}
	payloadPB := &autograder_pb.DownloadTokenPayload{RealPath: realpath, Filename: fn}
	ss, err := a.signPayloadToken(DownloadJWTSignKey, payloadPB, time.Now().Add(1*time.Minute))
	if err != nil {
		return nil, status.Error(codes.Internal, "SIGN_JWT")
	}
	resp := &autograder_pb.InitDownloadResponse{FileType: fileTypePB, Token: ss, Filename: fn, Filesize: size}
	return resp, nil
}

func isFilenameInvalid(filename string) bool {
	return len(filename) == 0 || strings.Contains(filename, "..") || filepath.IsAbs(filename)
}

func (a *AutograderService) getManifestPath(manifestId uint64) string {
	return fmt.Sprintf("uploads/manifests/%d", manifestId)
}

func (a *AutograderService) DeleteFileInManifest(
	ctx context.Context, request *autograder_pb.DeleteFileInManifestRequest,
) (*autograder_pb.DeleteFileInManifestResponse, error) {
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	manifestId := request.GetManifestId()
	mu, err := a.manifestRepo.LockManifest(ctx, manifestId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_MANIFEST_ID")
	}
	_, _ = a.manifestRepo.DeleteFileInManifest(ctx, filename, manifestId)
	mu.Unlock()
	err = a.ls.Delete(ctx, filepath.Join(a.getManifestPath(manifestId), filename))
	if err != nil && err != os.ErrNotExist {
		return nil, status.Error(codes.InvalidArgument, "DELETE_FAILED")
	}
	return &autograder_pb.DeleteFileInManifestResponse{}, nil
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
		return nil, status.Error(codes.InvalidArgument, "NAME")
	}
	if len(assignment.Description) == 0 || len(assignment.Description) > 1024*1024 {
		return nil, status.Error(codes.InvalidArgument, "DESCRIPTION")
	}
	if assignment.GetDueDate().AsTime().Before(assignment.GetReleaseDate().AsTime()) {
		return nil, status.Error(codes.InvalidArgument, "DUE_DATE")
	}
	assignmentId, err := a.assignmentRepo.CreateAssignment(ctx, assignment)
	if err != nil {
		return nil, status.Error(codes.Internal, "CREATE_ASSIGNMENT")
	}
	resp.Assignment = assignment
	resp.AssignmentId = assignmentId
	err = a.courseRepo.AddAssignment(ctx, request.GetCourseId(), assignmentId)
	if err != nil {
		return nil, status.Error(codes.Internal, "ADD_ASSIGNMENT")
	}
	go a.pullImage(request.GetProgrammingConfig().GetImage())
	return resp, nil
}

func (a *AutograderService) CreateCourse(
	ctx context.Context, request *autograder_pb.CreateCourseRequest,
) (*autograder_pb.CreateCourseResponse, error) {
	l := ctxzap.Extract(ctx)
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	resp := &autograder_pb.CreateCourseResponse{}
	if len(request.GetName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NAME")
	}
	if len(request.GetShortName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "SHORT_NAME")
	}
	if len(request.GetDescription()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "description")
	}

	course := &model_pb.Course{
		Name:        request.GetName(),
		ShortName:   request.GetShortName(),
		Description: request.GetDescription(),
	}

	courseId, err := a.courseRepo.CreateCourse(ctx, course)
	if err != nil {
		l.Error("CreateCourse.CreateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "CREATE_COURSE")
	}

	member := &model_pb.CourseMember{
		UserId:   user.GetUserId(),
		CourseId: courseId,
		Role:     model_pb.CourseRole_Instructor,
	}

	err = a.courseRepo.AddUser(ctx, member)
	if err != nil {
		l.Error("CreateCourse.AddUser", zap.Error(err))
		return nil, status.Error(codes.Internal, "ADD_USER")
	}

	err = a.userRepo.AddCourse(ctx, member)
	if err != nil {
		l.Error("CreateCourse.AddCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "ADD_COURSE")
	}

	resp.CourseId = courseId
	resp.Course = course
	return resp, nil
}

func (a *AutograderService) HasLeaderboard(
	ctx context.Context, request *autograder_pb.HasLeaderboardRequest,
) (*autograder_pb.HasLeaderboardResponse, error) {
	resp := &autograder_pb.HasLeaderboardResponse{
		HasLeaderboard: a.leaderboardRepo.HasLeaderboard(ctx, request.GetAssignmentId()),
	}
	return resp, nil
}

func (a *AutograderService) GetLeaderboard(
	ctx context.Context, request *autograder_pb.GetLeaderboardRequest,
) (*autograder_pb.GetLeaderboardResponse, error) {
	entries, err := a.leaderboardRepo.GetLeaderboard(ctx, request.GetAssignmentId())
	anonymous := a.leaderboardRepo.GetLeaderboardAnonymous(ctx, request.GetAssignmentId())
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	full := member.GetRole() == model_pb.CourseRole_TA || member.GetRole() == model_pb.CourseRole_Instructor
	if anonymous && !full {
		for _, entry := range entries {
			if entry.UserId == user.UserId {
				continue
			}
			entry.UserId = 0
		}
		resp := &autograder_pb.GetLeaderboardResponse{
			Entries: entries, Anonymous: anonymous, Full: full, Role: member.GetRole(),
		}
		return resp, err
	}
	for _, entry := range entries {
		userId := entry.GetUserId()
		dbUser, err := a.userRepo.GetUserById(ctx, userId)
		if err != nil {
			continue
		}
		if !full && userId != user.UserId {
			entry.UserId = 0
		}
		entry.Nickname = dbUser.Nickname
		if full {
			entry.Username = dbUser.Username
			entry.StudentId = dbUser.StudentId
			entry.Email = dbUser.Email
		}
	}
	resp := &autograder_pb.GetLeaderboardResponse{
		Entries: entries, Anonymous: anonymous, Full: full, Role: member.GetRole(),
	}
	return resp, err
}

type fileTreeNode struct {
	name        string
	path        string
	isFile      bool
	childrenIdx map[string]int
	children    []*fileTreeNode
}

func addFileToNode(subpath string, node *fileTreeNode) {
	segments := strings.Split(subpath, "/")
	curPath := ""
	curr := node
	for i := 0; i < len(segments); i++ {
		isFile := i == len(segments)-1
		var next *fileTreeNode
		nextIdx, ok := curr.childrenIdx[segments[i]]
		curPath = path.Join(curPath, segments[i])
		if !ok {
			next = new(fileTreeNode)
			if curr.childrenIdx == nil {
				curr.childrenIdx = map[string]int{}
			}
			curr.children = append(curr.children, next)
			curr.childrenIdx[segments[i]] = len(curr.children) - 1
		} else {
			next = curr.children[nextIdx]
		}
		next.isFile = isFile
		next.name = segments[i]
		next.path = curPath
		curr = next
	}
}

type fileTreeNodeSlice []*fileTreeNode

func (fs fileTreeNodeSlice) Len() int {
	return len(fs)
}

func (fs fileTreeNodeSlice) Swap(i, j int) {
	temp := fs[i]
	fs[i] = fs[j]
	fs[j] = temp
}

func (fs fileTreeNodeSlice) Less(i int, j int) bool {
	if fs[i].isFile && !fs[j].isFile {
		return false
	}
	if !fs[i].isFile && fs[j].isFile {
		return true
	}
	return fs[i].name < fs[j].name
}

func convertTreeNode(node *fileTreeNode, pbNode *autograder_pb.FileTreeNode) {
	sort.Sort(fileTreeNodeSlice(node.children))
	for _, child := range node.children {
		if child.isFile {
			pbNode.Children = append(
				pbNode.Children,
				&autograder_pb.FileTreeNode{
					NodeType: autograder_pb.FileTreeNode_File, Name: child.name, Path: child.path,
				},
			)
		} else {
			dirPBNode := &autograder_pb.FileTreeNode{
				NodeType: autograder_pb.FileTreeNode_Folder, Name: child.name, Path: child.path,
			}
			convertTreeNode(child, dirPBNode)
			pbNode.Children = append(pbNode.Children, dirPBNode)
		}
	}
}

func (a *AutograderService) GetFilesInSubmission(
	ctx context.Context, request *autograder_pb.GetFilesInSubmissionRequest,
) (*autograder_pb.GetFilesInSubmissionResponse, error) {
	submission := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)

	root := &fileTreeNode{}
	for _, f := range submission.Files {
		addFileToNode(f, root)
	}
	pbRoot := &autograder_pb.FileTreeNode{}
	convertTreeNode(root, pbRoot)
	rootPB := &autograder_pb.GetFilesInSubmissionResponse{Roots: pbRoot.GetChildren()}
	return rootPB, nil
}

func (a *AutograderService) GetCourse(
	ctx context.Context, request *autograder_pb.GetCourseRequest,
) (*autograder_pb.GetCourseResponse, error) {
	course, err := a.courseRepo.GetCourse(ctx, request.GetCourseId())
	member := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember)
	if member.GetRole() != model_pb.CourseRole_TA && member.GetRole() != model_pb.CourseRole_Instructor {
		course.JoinCode = ""
	}
	resp := &autograder_pb.GetCourseResponse{Course: course, Role: member.GetRole()}
	return resp, err
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

func (a *AutograderService) GetSubmissionReport(
	ctx context.Context, request *autograder_pb.GetSubmissionReportRequest,
) (*autograder_pb.GetSubmissionReportResponse, error) {
	submissionId := request.GetSubmissionId()
	brief, err := a.submissionReportRepo.GetSubmissionBriefReport(ctx, submissionId)
	if brief == nil {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Queued {
		return nil, status.Error(codes.NotFound, "QUEUED")
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Running {
		return nil, status.Error(codes.NotFound, "RUNNING")
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Cancelled {
		return nil, status.Error(codes.NotFound, "CANCELLED")
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Cancelling {
		return nil, status.Error(codes.NotFound, "CANCELLING")
	}
	report, err := a.submissionReportRepo.GetSubmissionReport(ctx, submissionId)
	resp := &autograder_pb.GetSubmissionReportResponse{Report: report, Status: brief.GetStatus()}
	return resp, err
}

func (a *AutograderService) CreateManifest(
	ctx context.Context, request *autograder_pb.CreateManifestRequest,
) (*autograder_pb.CreateManifestResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	assignment := ctx.Value(assignmentCtxKey{}).(*model_pb.Assignment)
	id, err := a.manifestRepo.CreateManifest(
		nil, user.GetUserId(), request.GetAssignmentId(), assignment.GetUploadLimit(),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, "CREATE_MANIFEST")
	}
	resp := &autograder_pb.CreateManifestResponse{ManifestId: id}
	return resp, nil
}

func (a *AutograderService) CreateSubmission(
	ctx context.Context, request *autograder_pb.CreateSubmissionRequest,
) (*autograder_pb.CreateSubmissionResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	role := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember).Role
	assignment := ctx.Value(assignmentCtxKey{}).(*model_pb.Assignment)
	submissionLimit := assignment.GetSubmissionLimit()
	if submissionLimit != nil && role != model_pb.CourseRole_Instructor && role != model_pb.CourseRole_TA {
		submissionIds, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(
			ctx, user.GetUserId(), request.GetAssignmentId(),
		)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_SUBMISSIONS")
		}
		if submissionLimit.GetTotal() > 0 {
			if len(submissionIds) > int(submissionLimit.GetTotal()) {
				return nil, status.Error(codes.ResourceExhausted, "SUBMISSION_LIMIT")
			}
		}
		if submissionLimit.GetFrequency() > 0 && submissionLimit.GetPeriod() > 0 {
			windowCount := 0
			windowLimit := time.Now().Add(-time.Minute * time.Duration(submissionLimit.GetPeriod()))
			for _, subId := range submissionIds {
				sub, err := a.submissionRepo.GetSubmission(ctx, subId)
				if err != nil {
					return nil, status.Error(codes.Internal, "GET_SUBMISSION")
				}
				if sub.GetSubmittedAt().AsTime().After(windowLimit) {
					windowCount += 1
				}
			}
			if windowCount >= int(submissionLimit.GetFrequency()) {
				return nil, status.Error(codes.ResourceExhausted, "SUBMISSION_FREQUENCY")
			}
		}
	}
	manifestId := request.GetManifestId()
	assignmentId := request.GetAssignmentId()
	submitters := request.GetSubmitters()
	submissionPath := a.getManifestPath(manifestId)
	files, err := a.manifestRepo.GetFilesInManifest(nil, manifestId)
	if err != nil || len(files) == 0 {
		return nil, status.Error(codes.InvalidArgument, "MANIFEST_FILES")
	}
	err = a.manifestRepo.DeleteManifest(nil, manifestId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "DELETE_MANIFEST")
	}
	submission := &model_pb.Submission{
		AssignmentId:    assignmentId,
		SubmittedAt:     timestamppb.Now(),
		Submitters:      submitters,
		Path:            submissionPath,
		Files:           files,
		LeaderboardName: user.GetNickname(),
		UserId:          user.GetUserId(),
	}
	id, err := a.submissionRepo.CreateSubmission(ctx, submission)
	brief := &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Queued}
	err = a.submissionReportRepo.UpdateSubmissionBriefReport(ctx, id, brief)
	if err != nil {
		return nil, status.Error(codes.Internal, "UPDATE_BRIEF")
	}
	err = a.submissionReportRepo.MarkUnfinishedSubmission(ctx, id, assignmentId)
	if err != nil {
		return nil, status.Error(codes.Internal, "MARK_UNFINISHED")
	}
	err = a.manifestRepo.DeleteManifest(nil, manifestId)
	if err != nil {
		return nil, status.Error(codes.Internal, "DELETE_MANIFEST")
	}
	err = a.ls.Put(ctx, filepath.Join(submissionPath, ".submission"), strings.NewReader(fmt.Sprintf("%d", id)))
	if err != nil {
		return nil, status.Error(codes.Internal, "CREATE_MARKER")
	}
	resp := &autograder_pb.CreateSubmissionResponse{SubmissionId: id, Files: files}
	go a.runSubmission(context.Background(), id, assignmentId)
	return resp, nil
}

type ProtobufClaim struct {
	jwt.StandardClaims
	Payload string `json:"payload"`
}

func (a *AutograderService) signPayloadToken(key []byte, payload proto.Message, expireAt time.Time) (string, error) {
	raw, err := proto.Marshal(payload)
	if err != nil {
		return "", status.Error(codes.Internal, "PROTOBUF_MARSHAL")
	}
	now := time.Now()
	claims := ProtobufClaim{
		Payload:        base64.StdEncoding.EncodeToString(raw),
		StandardClaims: jwt.StandardClaims{IssuedAt: now.Unix(), ExpiresAt: expireAt.Unix()},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return "", status.Error(codes.Internal, "JWT_SIGN")
	}
	return ss, nil
}

func (a *AutograderService) InitUpload(
	ctx context.Context, request *autograder_pb.InitUploadRequest,
) (*autograder_pb.InitUploadResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	manifest, err := a.manifestRepo.GetManifest(ctx, request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_MANIFEST_ID")
	}
	if manifest.GetUserId() != user.GetUserId() {
		return nil, status.Error(codes.InvalidArgument, "DIFFERENT_USER")
	}
	mu, err := a.manifestRepo.LockManifest(ctx, request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "LOCK_MANIFEST")
	}
	total, err := a.manifestRepo.GetManifestFilesize(ctx, request.GetManifestId())
	originalFile, _ := a.manifestRepo.GetManifestFileMetadata(
		ctx, request.GetFilename(), request.GetManifestId(),
	)
	mu.Unlock()
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_SIZE")
	}
	if originalFile != nil {
		total -= originalFile.GetFilesize()
	}
	if request.GetFilesize() == 0 || total+request.GetFilesize() > manifest.GetUploadLimit() {
		return nil, status.Error(codes.ResourceExhausted, "NO_SPACE")
	}
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	key := UploadJWTSignKey

	payload := &autograder_pb.UploadTokenPayload{
		ManifestId:  request.ManifestId,
		Filename:    path.Clean(filename),
		Filesize:    request.GetFilesize(),
		UploadLimit: manifest.GetUploadLimit(),
	}
	ss, err := a.signPayloadToken(key, payload, time.Now().Add(1*time.Minute))
	if err != nil {
		return nil, err
	}
	resp := &autograder_pb.InitUploadResponse{Token: ss}
	return resp, nil
}

func (a *AutograderService) SubscribeSubmission(
	request *autograder_pb.SubscribeSubmissionRequest, server autograder_pb.AutograderService_SubscribeSubmissionServer,
) error {
	_, err := a.AuthFunc(server.Context(), request, "/AutograderService/SubscribeSubmission")
	if err != nil {
		return err
	}
	c := make(chan *grader_pb.GradeReport)
	id := request.GetSubmissionId()
	l := ctxzap.Extract(server.Context()).With(zap.Uint64("submissionId", id))
	a.subsMu.Lock()
	brief, err := a.submissionReportRepo.GetSubmissionBriefReport(server.Context(), id)
	if err != nil && err != pebble.ErrNotFound {
		l.Error("SubscribeSubmission.GetBrief", zap.Error(err))
		a.subsMu.Unlock()
		return status.Error(codes.Internal, "GET_BRIEF")
	}
	if err == nil &&
		brief.GetStatus() != model_pb.SubmissionStatus_Running &&
		brief.GetStatus() != model_pb.SubmissionStatus_Queued &&
		brief.GetStatus() != model_pb.SubmissionStatus_Cancelled {
		a.subsMu.Unlock()
		return server.Send(
			&autograder_pb.SubscribeSubmissionResponse{
				Score:    brief.GetScore(),
				MaxScore: brief.GetMaxScore(),
				Status:   brief.GetStatus(),
			},
		)
	}
	var idx int
	l.Debug("SubscribeSubmission.Begin")
	a.reportSubs[id] = append(a.reportSubs[id], c)
	idx = len(a.reportSubs[id]) - 1
	a.subsMu.Unlock()
	brief, _ = a.submissionReportRepo.GetSubmissionBriefReport(server.Context(), id)
	rank, total := a.graderHubSvc.GetPendingRank(id)
	err = server.Send(
		&autograder_pb.SubscribeSubmissionResponse{
			Score: brief.GetScore(), MaxScore: brief.GetMaxScore(), Status: brief.GetStatus(),
			PendingRank: &model_pb.PendingRank{Rank: uint64(rank), Total: uint64(total)},
		},
	)
	if err != nil {
		return err
	}
	for {
		select {
		case <-server.Context().Done():
			a.subsMu.Lock()
			if len(a.reportSubs[id]) > idx {
				a.reportSubs[id][idx] = nil
			}
			a.subsMu.Unlock()
			return nil
		case r := <-c:
			err := server.Send(
				&autograder_pb.SubscribeSubmissionResponse{
					Score:       r.GetBrief().GetScore(),
					MaxScore:    r.GetBrief().GetMaxScore(),
					Status:      r.GetBrief().GetStatus(),
					PendingRank: r.GetPendingRank(),
				},
			)
			if r.GetBrief().GetStatus() == model_pb.SubmissionStatus_Failed ||
				r.GetBrief().GetStatus() == model_pb.SubmissionStatus_Finished {
				return err
			}
		}
	}
}

func (a *AutograderService) getSubmissionHistory(
	ctx context.Context, userId, assignmentId uint64,
) ([]*autograder_pb.SubmissionInfo, error) {
	var submissions []*autograder_pb.SubmissionInfo
	subIds, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, userId, assignmentId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	sort.Sort(sort.Reverse(sortkeys.Uint64Slice(subIds)))
	for _, subId := range subIds {
		sub, err := a.submissionRepo.GetSubmission(ctx, subId)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_SUBMISSION")
		}
		report, err := a.submissionReportRepo.GetSubmissionBriefReport(ctx, subId)
		subStatus := model_pb.SubmissionStatus_Running
		score := uint64(0)
		maxScore := uint64(0)
		if err == nil {
			score = report.Score
			maxScore = report.MaxScore
			subStatus = report.GetStatus()
		}
		var submitters []*autograder_pb.SubmissionInfo_Submitter
		for _, uid := range sub.Submitters {
			user, err := a.userRepo.GetUserById(ctx, uid)
			if err != nil {
				return nil, status.Error(codes.Internal, "GET_USER")
			}
			oneSubmitter := &autograder_pb.SubmissionInfo_Submitter{
				UserId:    uid,
				Username:  user.Username,
				Nickname:  user.Nickname,
				StudentId: user.StudentId,
			}
			submitters = append(submitters, oneSubmitter)
		}
		ret := &autograder_pb.SubmissionInfo{
			SubmissionId: subId,
			SubmittedAt:  sub.SubmittedAt,
			Submitters:   submitters,
			Score:        score,
			MaxScore:     maxScore,
			Status:       subStatus,
		}
		submissions = append(submissions, ret)
	}
	return submissions, nil
}

func (a *AutograderService) GetSubmissionsInAssignment(
	ctx context.Context, request *autograder_pb.GetSubmissionsInAssignmentRequest,
) (*autograder_pb.GetSubmissionsInAssignmentResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	submissions, err := a.getSubmissionHistory(ctx, user.GetUserId(), request.GetAssignmentId())
	if err != nil {
		return nil, err
	}
	resp := &autograder_pb.GetSubmissionsInAssignmentResponse{Submissions: submissions}
	return resp, nil
}

func (a *AutograderService) GetAssignmentsInCourse(
	ctx context.Context, request *autograder_pb.GetAssignmentsInCourseRequest,
) (*autograder_pb.GetAssignmentsInCourseResponse, error) {
	var assignments []*autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	assignmentIds, err := a.courseRepo.GetAssignmentsByCourse(ctx, request.GetCourseId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_ASSIGNMENTS")
	}
	for _, asgnId := range assignmentIds {
		assignment, err := a.assignmentRepo.GetAssignment(ctx, asgnId)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_ASSIGNMENT")
		}
		subs, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, user.GetUserId(), asgnId)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_SUBMISSIONS")
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

func (a *AutograderService) GetCourseList(
	ctx context.Context, request *autograder_pb.GetCourseListRequest,
) (*autograder_pb.GetCourseListResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	var courses []*autograder_pb.GetCourseListResponse_CourseCardInfo
	courseMembers, err := a.userRepo.GetCoursesByUser(ctx, user.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_COURSES")
	}
	for _, member := range courseMembers {
		course, err := a.courseRepo.GetCourse(ctx, member.CourseId)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_COURSE")
		}
		ret := &autograder_pb.GetCourseListResponse_CourseCardInfo{
			Name:      course.Name,
			ShortName: course.ShortName,
			Role:      member.Role,
			CourseId:  member.CourseId,
		}
		courses = append(courses, ret)
	}
	response := &autograder_pb.GetCourseListResponse{Courses: courses}
	return response, nil
}

func (a *AutograderService) signLoginToken(
	ctx context.Context, userId uint64, username string, nickname string, isAdmin bool,
) error {
	payload := &autograder_pb.UserTokenPayload{
		UserId:   userId,
		Username: username,
		Nickname: nickname,
		IsAdmin:  isAdmin,
	}
	ss, err := a.signPayloadToken(UserJWTSignKey, payload, time.Now().Add(3*time.Hour))
	if err != nil {
		return err
	}
	refreshMD := metadata.Pairs("token", ss)
	err = grpc.SetHeader(ctx, refreshMD)
	if err != nil {
		return err
	}
	return nil
}

func (a *AutograderService) Login(
	ctx context.Context, request *autograder_pb.LoginRequest,
) (*autograder_pb.LoginResponse, error) {
	l := ctxzap.Extract(ctx)
	username := request.GetUsername()
	password := request.GetPassword()
	l.Debug("login", zap.String("username", username))
	var user *model_pb.User
	var id uint64
	var err error
	if strings.Contains(username, "@") {
		user, id, err = a.userRepo.GetUserByEmail(ctx, username)
	} else {
		user, id, err = a.userRepo.GetUserByUsername(ctx, username)
	}
	if user == nil || err != nil || bcrypt.CompareHashAndPassword(user.GetPassword(), []byte(password)) != nil {
		return nil, status.Error(codes.InvalidArgument, "WRONG_PASSWORD")
	}
	if err := a.signLoginToken(ctx, id, user.Username, user.Nickname, user.IsAdmin); err != nil {
		return nil, err
	}
	response := &autograder_pb.LoginResponse{
		UserId: id,
	}
	return response, nil
}

func (a *AutograderService) ActivateSubmission(
	ctx context.Context, request *autograder_pb.ActivateSubmissionRequest,
) (*autograder_pb.ActivateSubmissionResponse, error) {
	submission := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)
	report, err := a.submissionReportRepo.GetSubmissionReport(ctx, request.GetSubmissionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "GET_REPORT")
	}
	if len(report.Leaderboard) == 0 {
		return &autograder_pb.ActivateSubmissionResponse{Activated: false}, nil
	}
	err = a.leaderboardRepo.UpdateLeaderboardEntry(
		ctx, submission.GetAssignmentId(), submission.GetUserId(), &model_pb.LeaderboardEntry{
			SubmissionId: request.GetSubmissionId(),
			SubmittedAt:  submission.GetSubmittedAt(),
			Items:        report.Leaderboard,
			UserId:       submission.GetUserId(),
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, "UPDATE_ENTRY")
	}
	return &autograder_pb.ActivateSubmissionResponse{
		Activated: true,
	}, nil
}

func (a *AutograderService) runSubmission(ctx context.Context, submissionId uint64, assignmentId uint64) {
	logger := zap.L().With(zap.Uint64("submissionId", submissionId), zap.Uint64("assignmentId", assignmentId))
	assignment, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		logger.Error("RunSubmission.GetAssignment", zap.Error(err))
		return
	}
	submission, err := a.submissionRepo.GetSubmission(ctx, submissionId)
	if err != nil {
		logger.Error("RunSubmission.GetSubmission", zap.Error(err))
		return
	}
	err = a.submissionReportRepo.MarkUnfinishedSubmission(ctx, submissionId, assignmentId)
	config := assignment.ProgrammingConfig
	notifyC := make(chan *grader_pb.GradeReport)
	brief := &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Queued}
	err = a.submissionReportRepo.UpdateSubmissionBriefReport(ctx, submissionId, brief)
	go a.progGrader.GradeSubmission(context.Background(), submissionId, submission, config, notifyC)
	go func() {
		for r := range notifyC {
			a.subsMu.Lock()
			for _, sub := range a.reportSubs[submissionId] {
				if sub != nil {
					sub <- r
				}
			}
			if r.GetBrief().GetStatus() == model_pb.SubmissionStatus_Finished ||
				r.GetBrief().GetStatus() == model_pb.SubmissionStatus_Failed ||
				r.GetBrief().GetStatus() == model_pb.SubmissionStatus_Cancelled {
				logger.Debug("RunSubmission.Finished")
				delete(a.reportSubs, submissionId)
				a.subsMu.Unlock()
				return
			}
			a.subsMu.Unlock()
		}
	}()
}

func (a *AutograderService) runUnfinishedSubmissions() {
	ctx := context.Background()
	ids, err := a.submissionReportRepo.GetUnfinishedSubmissions(ctx)
	if err != nil {
		panic(err)
	}
	for _, id := range ids {
		asgnId := id.AssignmentId
		subId := id.SubmissionId
		zap.L().Info(
			"UnfinishedSubmission.Found", zap.Uint64("assignmentId", asgnId), zap.Uint64("submissionId", subId),
		)
		go a.runSubmission(ctx, subId, asgnId)
	}
}

func (a *AutograderService) parseTokenPayload(key []byte, tokenString string) ([]byte, error) {
	token, err := jwt.Parse(
		tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected singning method: %v", token.Header["alg"])
			}

			return key, nil
		},
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid || claims.Valid() != nil {
		return nil, errors.New("claim not valid")
	}
	payloadString, ok := claims["payload"].(string)
	if !ok {
		return nil, errors.New("payload not found")
	}

	payload, err := base64.StdEncoding.DecodeString(payloadString)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (a *AutograderService) HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	downloadTokenString := strings.TrimSpace(r.URL.Query().Get("token"))
	fn := chi.URLParam(r, "filename")
	payload, err := a.parseTokenPayload(DownloadJWTSignKey, downloadTokenString)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var payloadPB autograder_pb.DownloadTokenPayload
	err = proto.Unmarshal(payload, &payloadPB)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	logger := zap.L().With(zap.String("filename", payloadPB.Filename), zap.String("realPath", payloadPB.RealPath))
	if !payloadPB.GetIsDirectory() {
		if err != nil || fn != payloadPB.GetFilename() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Add("Content-disposition", "attachment; filename="+fn)
		http.ServeFile(w, r, payloadPB.GetRealPath())
	} else {
		submission, err := a.submissionRepo.GetSubmission(r.Context(), payloadPB.GetSubmissionId())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Add("Content-disposition", "attachment; filename="+fn)
		zw := zip.NewWriter(w)
		defer zw.Close()
		for _, f := range submission.Files {
			data, err := a.ls.Open(r.Context(), filepath.Join(submission.Path, f))
			if err != nil {
				logger.Error("FileDownload.GenerateZip.OpenLocal", zap.Error(err))
				return
			}
			zf, err := zw.Create(f)
			if err != nil {
				logger.Error("FileDownload.GenerateZip.CreateZipFile", zap.Error(err))
				return
			}
			_, err = io.Copy(zf, data)
			if err != nil {
				logger.Error("FileDownload.GenerateZip.IOCopy", zap.Error(err))
				return
			}
		}
	}
}

func (a *AutograderService) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	var err error
	normalizedContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-type")))
	uploadTokenString := strings.TrimSpace(r.Header.Get("Upload-token"))
	payload, err := a.parseTokenPayload(UploadJWTSignKey, uploadTokenString)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var payloadPB autograder_pb.UploadTokenPayload
	err = proto.Unmarshal(payload, &payloadPB)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(normalizedContentType, "multipart/form-data; boundary") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = r.ParseMultipartForm(MaxMultipartFormParseMemory)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	uploadFile, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if header.Size <= 0 {
		w.WriteHeader(http.StatusLengthRequired)
		return
	}
	fileHeader := make([]byte, 512)
	_, err = uploadFile.Read(fileHeader)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = uploadFile.Seek(0, 0)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// TODO: filter MIME
	mu, err := a.manifestRepo.LockManifest(r.Context(), payloadPB.GetManifestId())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	originalFile, err := a.manifestRepo.GetManifestFileMetadata(
		r.Context(), payloadPB.GetFilename(), payloadPB.GetManifestId(),
	)
	if originalFile != nil {
		_, err = a.manifestRepo.DeleteFileInManifest(r.Context(), payloadPB.GetFilename(), payloadPB.GetManifestId())
		if err != nil {
			zap.L().Error("FileUpload.CleanupOld", zap.Error(err))
		}
	}
	current, err := a.manifestRepo.GetManifestFilesize(r.Context(), payloadPB.GetManifestId())
	if err != nil {
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if current+uint64(header.Size) > payloadPB.GetUploadLimit() {
		mu.Unlock()
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	mu.Unlock()
	destPath := filepath.Join(a.getManifestPath(payloadPB.GetManifestId()), payloadPB.GetFilename())
	err = a.ls.Put(
		r.Context(),
		destPath,
		uploadFile,
	)
	if err != nil {
		zap.L().Error("FileUpload.PutFile", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	mu, err = a.manifestRepo.LockManifest(r.Context(), payloadPB.GetManifestId())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = a.manifestRepo.AddFileToManifest(
		r.Context(), payloadPB.GetFilename(), payloadPB.GetManifestId(),
		uint64(header.Size),
	)
	mu.Unlock()
	if err != nil {
		zap.L().Error("FileUpload.AddFileToManifest", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	return
}

func (a *AutograderService) getGithubEmails(ctx context.Context, token *oauth2.Token) ([]*github.UserEmail, error) {
	ghClient := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token)))
	emails, _, err := ghClient.Users.ListEmails(ctx, nil)
	if err != nil || len(emails) == 0 {
		return nil, status.Error(codes.Internal, "GET_GITHUB_EMAILS")
	}
	return emails, nil
}

func (a *AutograderService) getGithubUser(ctx context.Context, code string) (*github.User, *oauth2.Token, error) {
	token, err := a.githubOAuth2Config.Exchange(ctx, code)
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, "INVALID_CODE")
	}
	ghClient := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token)))
	ghUser, _, err := ghClient.Users.Get(ctx, "")
	if err != nil {
		return nil, nil, status.Error(codes.Internal, "GET_GITHUB_USER")
	}
	return ghUser, token, nil
}

func (a *AutograderService) GithubLogin(
	ctx context.Context, request *autograder_pb.GithubLoginRequest,
) (*autograder_pb.GithubLoginResponse, error) {
	l := ctxzap.Extract(ctx)
	ghUser, token, err := a.getGithubUser(ctx, request.Code)
	if err != nil {
		return nil, err
	}

	login := ghUser.GetLogin()
	l.Debug("GithubLogin", zap.String("githubId", login))

	user, userId, err := a.userRepo.GetUserByGithubId(ctx, login)
	if user != nil {
		if err := a.signLoginToken(ctx, userId, user.Username, user.Nickname, user.IsAdmin); err != nil {
			return nil, err
		}
		return &autograder_pb.GithubLoginResponse{UserId: userId}, nil
	}

	emails, err := a.getGithubEmails(ctx, token)
	if err != nil {
		return nil, err
	}
	email := emails[0].GetEmail()
	for _, e := range emails {
		if e.GetPrimary() {
			email = e.GetEmail()
			break
		}
	}

	user, userId, err = a.userRepo.GetUserByEmail(ctx, email)
	if user != nil {
		oldGithubId := user.GetGithubId()
		if oldGithubId != "" && oldGithubId != login {
			return nil, status.Error(codes.AlreadyExists, "EMAIL_DIFFERENT")
		}
		if err := a.userRepo.BindGithubId(ctx, userId, login); err != nil {
			return nil, status.Error(codes.Internal, "BIND_GITHUB_ID")
		}
		if err := a.signLoginToken(ctx, userId, user.Username, user.Nickname, user.IsAdmin); err != nil {
			return nil, err
		}
		return &autograder_pb.GithubLoginResponse{UserId: userId}, nil
	}

	user, userId, err = a.userRepo.GetUserByUsername(ctx, login)
	if user != nil {
		oldGithubId := user.GetGithubId()
		if oldGithubId != "" && oldGithubId != login {
			return nil, status.Error(codes.AlreadyExists, "USERNAME_DIFFERENT")
		}
		if err := a.userRepo.BindGithubId(ctx, userId, login); err != nil {
			return nil, status.Error(codes.Internal, "BIND_GITHUB_ID")
		}
		if err := a.signLoginToken(ctx, userId, user.Username, user.Nickname, user.IsAdmin); err != nil {
			return nil, err
		}
		return &autograder_pb.GithubLoginResponse{UserId: userId}, nil
	}

	userId, err = a.signUpNewUser(ctx, email, login, "")
	if err != nil {
		return nil, err
	}
	if err := a.userRepo.BindGithubId(ctx, userId, login); err != nil {
		return nil, status.Error(codes.Internal, "BIND_GITHUB_ID")
	}
	if err := a.signLoginToken(ctx, userId, login, login, false); err != nil {
		return nil, err
	}
	return &autograder_pb.GithubLoginResponse{UserId: userId}, nil
}

func (a *AutograderService) GetUser(
	ctx context.Context, request *autograder_pb.GetUserRequest,
) (*autograder_pb.GetUserResponse, error) {
	l := ctxzap.Extract(ctx)
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	l.Debug("GetUser", zap.String("username", user.GetUsername()))
	dbUser, err := a.userRepo.GetUserById(ctx, user.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USER")
	}
	dbUser.Password = nil
	return &autograder_pb.GetUserResponse{User: dbUser}, nil
}

func (a *AutograderService) UnbindGithub(
	ctx context.Context, request *autograder_pb.UnbindGithubRequest,
) (*autograder_pb.UnbindGithubResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	err := a.userRepo.UnbindGithubId(ctx, user.GetUserId())
	if err != nil {
		return &autograder_pb.UnbindGithubResponse{Success: false}, nil
	}
	return &autograder_pb.UnbindGithubResponse{Success: true}, nil
}

func (a *AutograderService) BindGithub(
	ctx context.Context, request *autograder_pb.BindGithubRequest,
) (*autograder_pb.BindGithubResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	ghUser, _, err := a.getGithubUser(ctx, request.Code)
	if err != nil {
		return nil, err
	}
	login := ghUser.GetLogin()
	if len(login) == 0 {
		return nil, status.Error(codes.InvalidArgument, "INVALID_GITHUB_LOGIN")
	}
	bindUserId, err := a.userRepo.GetUserIdByGithubId(ctx, login)
	if bindUserId != 0 {
		return nil, status.Error(codes.AlreadyExists, "ALREADY_IN_USE")
	}
	err = a.userRepo.BindGithubId(ctx, user.GetUserId(), login)
	if err != nil {
		return &autograder_pb.BindGithubResponse{Success: false}, nil
	}
	return &autograder_pb.BindGithubResponse{Success: true}, nil
}

func (a *AutograderService) UpdateUser(
	ctx context.Context, request *autograder_pb.UpdateUserRequest,
) (*autograder_pb.UpdateUserResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	dbUser, err := a.userRepo.GetUserById(ctx, user.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USER")
	}
	dbUser.Nickname = request.GetNickname()
	dbUser.StudentId = request.GetStudentId()
	if len(dbUser.Nickname) > 16 {
		return nil, status.Error(codes.InvalidArgument, "NICKNAME_TOO_LONG")
	}
	if len(dbUser.StudentId) > 16 {
		return nil, status.Error(codes.InvalidArgument, "STUDENT_ID_TOO_LONG")
	}
	err = a.userRepo.UpdateUser(ctx, user.GetUserId(), dbUser)
	if err != nil {
		return &autograder_pb.UpdateUserResponse{Success: false}, nil
	}
	return &autograder_pb.UpdateUserResponse{Success: true}, nil
}

func (a *AutograderService) UpdatePassword(
	ctx context.Context, request *autograder_pb.UpdatePasswordRequest,
) (*autograder_pb.UpdatePasswordResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	dbUser, err := a.userRepo.GetUserById(ctx, user.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USER")
	}
	if err := bcrypt.CompareHashAndPassword(dbUser.Password, []byte(request.OldPassword)); err != nil {
		return &autograder_pb.UpdatePasswordResponse{Success: false}, nil
	}
	dbUser.Password, err = bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Error(codes.Internal, "PASSWORD_HASH")
	}
	err = a.userRepo.UpdateUser(ctx, user.GetUserId(), dbUser)
	if err != nil {
		return &autograder_pb.UpdatePasswordResponse{Success: false}, nil
	}
	return &autograder_pb.UpdatePasswordResponse{Success: true}, nil
}

func (a *AutograderService) JoinCourse(
	ctx context.Context, request *autograder_pb.JoinCourseRequest,
) (*autograder_pb.JoinCourseResponse, error) {
	courseId := utils.Base30Decode(request.GetJoinCode())
	course, err := a.courseRepo.GetCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_COURSE_ID")
	}
	if !course.AllowsJoin || course.GetJoinCode() != request.GetJoinCode() {
		return nil, status.Error(codes.InvalidArgument, "INVALID_JOIN_CODE")
	}
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	userId := user.GetUserId()
	member := a.userRepo.GetCourseMember(ctx, userId, courseId)
	if member != nil {
		return nil, status.Error(codes.AlreadyExists, "ALREADY_IN_COURSE")
	}
	newMember := &model_pb.CourseMember{
		CourseId: courseId,
		UserId:   userId,
		Role:     model_pb.CourseRole_Student,
	}
	err = a.userRepo.AddCourse(ctx, newMember)
	if err != nil {
		return nil, status.Error(codes.Internal, "ADD_COURSE")
	}
	err = a.courseRepo.AddUser(ctx, newMember)
	if err != nil {
		return nil, status.Error(codes.Internal, "ADD_USER")
	}
	return &autograder_pb.JoinCourseResponse{CourseId: courseId}, nil
}

func (a *AutograderService) GenerateJoinCode(
	ctx context.Context, request *autograder_pb.GenerateJoinCodeRequest,
) (*autograder_pb.GenerateJoinCodeResponse, error) {
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", request.GetCourseId()))
	code := utils.Base30Encode(request.GetCourseId())
	course, err := a.courseRepo.GetCourse(ctx, request.GetCourseId())
	if err != nil {
		l.Error("GenerateJoinCode.GetCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "GET_COURSE")
	}
	course.JoinCode = code
	err = a.courseRepo.UpdateCourse(ctx, request.GetCourseId(), course)
	if err != nil {
		l.Error("GenerateJoinCode.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "UPDATE_COURSE")
	}
	return &autograder_pb.GenerateJoinCodeResponse{JoinCode: code}, nil
}

func (a *AutograderService) ChangeAllowsJoinCourse(
	ctx context.Context, request *autograder_pb.ChangeAllowsJoinCourseRequest,
) (*autograder_pb.ChangeAllowsJoinCourseResponse, error) {
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", request.GetCourseId()))
	course, err := a.courseRepo.GetCourse(ctx, request.GetCourseId())
	if err != nil {
		l.Error("GenerateJoinCode.GetCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "GET_COURSE")
	}
	course.AllowsJoin = request.GetAllowsJoin()
	err = a.courseRepo.UpdateCourse(ctx, request.GetCourseId(), course)
	if err != nil {
		l.Error("GenerateJoinCode.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "UPDATE_COURSE")
	}
	return &autograder_pb.ChangeAllowsJoinCourseResponse{AllowsJoin: course.AllowsJoin}, nil
}

func (a *AutograderService) InspectAllSubmissionsInAssignment(
	ctx context.Context, request *autograder_pb.InspectAllSubmissionsInAssignmentRequest,
) (*autograder_pb.InspectAllSubmissionsInAssignmentResponse, error) {
	assignmentId := request.GetAssignmentId()
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	members, err := a.courseRepo.GetUsersByCourse(ctx, courseId)
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId), zap.Uint64("assignmentId", assignmentId))
	if err != nil {
		l.Error("InspectAllSubmissionsInAssignment.GetUsers", zap.Error(err))
		return nil, status.Error(codes.Internal, "GET_USERS")
	}
	var userSubmissionInfo []*autograder_pb.InspectAllSubmissionsInAssignmentResponse_UserSubmissionInfo
	for _, member := range members {
		user, err := a.userRepo.GetUserById(ctx, member.GetUserId())
		if err != nil {
			l.Error("InspectAllSubmissionsInAssignment.GetUser", zap.Error(err))
			return nil, status.Error(codes.Internal, "GET_USER")
		}
		submissions, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, member.GetUserId(), assignmentId)
		if err != nil {
			l.Error("InspectAllSubmissionsInAssignment.GetSubmissions", zap.Error(err))
			return nil, status.Error(codes.Internal, "GET_SUBMISSIONS")
		}
		userSubmissionInfo = append(
			userSubmissionInfo, &autograder_pb.InspectAllSubmissionsInAssignmentResponse_UserSubmissionInfo{
				UserId:          member.GetUserId(),
				Username:        user.GetUsername(),
				Nickname:        user.GetNickname(),
				StudentId:       user.GetStudentId(),
				SubmissionCount: uint64(len(submissions)),
			},
		)
	}
	return &autograder_pb.InspectAllSubmissionsInAssignmentResponse{Entries: userSubmissionInfo}, nil
}

func (a *AutograderService) InspectUserSubmissionHistory(
	ctx context.Context, request *autograder_pb.InspectUserSubmissionHistoryRequest,
) (*autograder_pb.InspectUserSubmissionHistoryResponse, error) {
	assignmentId := request.GetAssignmentId()
	userId := request.GetUserId()
	submissions, err := a.getSubmissionHistory(ctx, userId, assignmentId)
	if err != nil {
		return nil, err
	}
	return &autograder_pb.InspectUserSubmissionHistoryResponse{Submissions: submissions}, nil
}

func (a *AutograderService) RegradeSubmission(
	ctx context.Context, request *autograder_pb.RegradeSubmissionRequest,
) (*autograder_pb.RegradeSubmissionResponse, error) {
	assignmentId := ctx.Value(submissionCtxKey{}).(*model_pb.Submission).AssignmentId
	go a.runSubmission(context.Background(), request.SubmissionId, assignmentId)
	return &autograder_pb.RegradeSubmissionResponse{}, nil
}

func (a *AutograderService) RegradeAssignment(
	ctx context.Context, request *autograder_pb.RegradeAssignmentRequest,
) (*autograder_pb.RegradeAssignmentResponse, error) {
	l := ctxzap.Extract(ctx)
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	members, err := a.courseRepo.GetUsersByCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USERS")
	}
	var submissionIds []uint64
	for _, member := range members {
		userSubmissions, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(
			ctx, member.UserId, request.AssignmentId,
		)
		if err != nil {
			l.Error("RegradeAssignment.GetSubmissions", zap.Error(err))
		}
		submissionIds = append(submissionIds, userSubmissions...)
	}
	for _, submissionId := range submissionIds {
		go a.runSubmission(context.Background(), submissionId, request.GetAssignmentId())
	}
	return &autograder_pb.RegradeAssignmentResponse{}, nil
}

func (a *AutograderService) ChangeLeaderboardAnonymous(
	ctx context.Context, request *autograder_pb.ChangeLeaderboardAnonymousRequest,
) (*autograder_pb.ChangeLeaderboardAnonymousResponse, error) {
	a.leaderboardRepo.SetLeaderboardAnonymous(ctx, request.GetAssignmentId(), request.GetAnonymous())
	return &autograder_pb.ChangeLeaderboardAnonymousResponse{Anonymous: request.Anonymous}, nil
}

func (a *AutograderService) ExportAssignmentGrades(
	ctx context.Context, request *autograder_pb.ExportAssignmentGradesRequest,
) (*autograder_pb.ExportAssignmentGradesResponse, error) {
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	assignmentId := request.GetAssignmentId()
	members, err := a.courseRepo.GetUsersByCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USERS")
	}
	var entries []*autograder_pb.ExportAssignmentGradesResponse_Entry
	for _, member := range members {
		userId := member.GetUserId()
		dbUser, _ := a.userRepo.GetUserById(ctx, userId)
		submissions, err := a.getSubmissionHistory(ctx, userId, assignmentId)
		if err != nil {
			continue
		}
		score := uint64(0)
		maxScore := uint64(0)
		for _, sub := range submissions {
			if sub.Score > score {
				score = sub.Score
				maxScore = sub.MaxScore
			}
		}
		entry := &autograder_pb.ExportAssignmentGradesResponse_Entry{
			UserId:          userId,
			Username:        dbUser.Username,
			StudentId:       dbUser.StudentId,
			Nickname:        dbUser.Nickname,
			SubmissionCount: uint64(len(submissions)),
			Score:           score,
			MaxScore:        maxScore,
		}
		entries = append(entries, entry)
	}
	return &autograder_pb.ExportAssignmentGradesResponse{Entries: entries}, nil
}

func (a *AutograderService) GetAllGraders(
	ctx context.Context, request *autograder_pb.GetAllGradersRequest,
) (*autograder_pb.GetAllGradersResponse, error) {
	return a.graderHubSvc.GetAllGraders(ctx)
}

func (a *AutograderService) CancelSubmission(
	ctx context.Context, request *autograder_pb.CancelSubmissionRequest,
) (*autograder_pb.CancelSubmissionResponse, error) {
	l := ctxzap.Extract(ctx).With(zap.Uint64("submissionId", request.GetSubmissionId()))
	l.Debug("CancelSubmission")
	err := a.graderHubSvc.CancelGrade(ctx, request.GetSubmissionId())
	if err != nil {
		l.Error("CancelSubmission.CancelGrade", zap.Error(err))
		return nil, status.Error(codes.Internal, "CANCEL_GRADE")
	}
	return &autograder_pb.CancelSubmissionResponse{}, nil
}

func (a *AutograderService) manifestGarbageCollect() {
	ctx := context.Background()
	ch := make(chan uint64)
	zap.L().Debug("Manifest.GC.Start")
	go a.manifestRepo.GarbageCollect(ctx, ch)
	for manifestId := range ch {
		manifestPath := a.getManifestPath(manifestId)
		err := a.ls.Delete(ctx, manifestPath)
		if err != nil {
			zap.L().Error("Manifest.GC.DeleteFile", zap.String("manifestPath", manifestPath), zap.Error(err))
		}
	}
}

func (a *AutograderService) PushFile(w http.ResponseWriter, r *http.Request) {
	uploadPath := r.URL.Path

	err := a.ls.Put(
		r.Context(),
		uploadPath,
		r.Body,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	return
}

func (a *AutograderService) DeleteLeaderboard(
	ctx context.Context, request *autograder_pb.DeleteLeaderboardRequest,
) (*autograder_pb.DeleteLeaderboardResponse, error) {
	_ = a.leaderboardRepo.DeleteLeaderboardEntry(ctx, request.GetAssignmentId(), request.GetUserId())
	return &autograder_pb.DeleteLeaderboardResponse{}, nil
}

func (a *AutograderService) GetAllUsers(
	ctx context.Context, request *autograder_pb.GetAllUsersRequest,
) (*autograder_pb.GetAllUsersResponse, error) {
	userIds, users, err := a.userRepo.GetAllUsers(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USERS")
	}
	var userInfos []*autograder_pb.GetAllUsersResponse_UserInfo
	for i := 0; i < len(userIds); i++ {
		users[i].Password = nil
		userInfos = append(
			userInfos, &autograder_pb.GetAllUsersResponse_UserInfo{
				UserId: userIds[i],
				User:   users[i],
			},
		)
	}
	return &autograder_pb.GetAllUsersResponse{Users: userInfos}, nil
}

func (a *AutograderService) SetAdmin(
	ctx context.Context, request *autograder_pb.SetAdminRequest,
) (*autograder_pb.SetAdminResponse, error) {
	curUser := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	if curUser.GetUserId() == request.GetUserId() || request.GetUserId() == 1 {
		return &autograder_pb.SetAdminResponse{}, nil
	}
	user, err := a.userRepo.GetUserById(ctx, request.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_USER")
	}
	user.IsAdmin = request.GetIsAdmin()
	err = a.userRepo.UpdateUser(ctx, request.GetUserId(), user)
	if err != nil {
		return nil, status.Error(codes.Internal, "UPDATE_USER")
	}
	return &autograder_pb.SetAdminResponse{}, nil
}

func (a *AutograderService) StreamLog(
	request *autograder_pb.WebStreamLogRequest,
	server autograder_pb.AutograderService_StreamLogServer,
) error {
	ctx := server.Context()
	ch, err := a.graderHubSvc.StreamLog(ctx, request.GetSubmissionId())
	if err != nil {
		return status.Error(codes.Internal, "STREAM_LOG")
	}
	for data := range ch {
		err := server.SendMsg(&autograder_pb.WebStreamLogResponse{Data: data})
		if err != nil {
			break
		}
	}
	return nil
}

func NewAutograderServiceServer(
	db *pebble.DB, ls *storage.LocalStorage, mailer mailer.Mailer, captchaVerifier *hcaptcha.Client,
	ghOauth2Config *oauth2.Config,
	srr repository.SubmissionReportRepository,
	graderHubSvc *grader_grpc.GraderHubService,
) *AutograderService {
	a := &AutograderService{
		manifestRepo:         repository.NewKVManifestRepository(db),
		userRepo:             repository.NewKVUserRepository(db),
		submissionRepo:       repository.NewKVSubmissionRepository(db),
		submissionReportRepo: srr,
		courseRepo:           repository.NewKVCourseRepository(db),
		assignmentRepo:       repository.NewKVAssignmentRepository(db),
		leaderboardRepo:      repository.NewKVLeaderboardRepository(db),
		verificationCodeRepo: repository.NewKVVerificationCodeRepository(db),
		progGrader:           grader.NewHubGrader(graderHubSvc),
		githubOAuth2Config:   ghOauth2Config,
		mailer:               mailer,
		captchaVerifier:      captchaVerifier,
		ls:                   ls,
		reportSubs:           make(map[uint64][]chan *grader_pb.GradeReport),
		subsMu:               &sync.Mutex{},
		graderHubSvc:         graderHubSvc,
	}
	a.initAuthFuncs()
	go a.runUnfinishedSubmissions()
	go a.manifestGarbageCollect()
	go a.verificationCodeRepo.GarbageCollect()
	return a
}
