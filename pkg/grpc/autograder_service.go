package grpc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/grader"
	grader_grpc "autograder-server/pkg/grader/grpc"
	"autograder-server/pkg/mailer"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"autograder-server/pkg/storage"

	"github.com/cockroachdb/pebble/v2"
	"github.com/kataras/hcaptcha"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	authFuncs            map[string][]MethodAuthFunc
	githubOAuth2Config   *oauth2.Config
	graderHubSvc         *grader_grpc.GraderHubService
	userJWTSignKey       []byte
	uploadJWTSignKey     []byte
	downloadJWTSignKey   []byte

	// gcCtx governs the background goroutines started by the constructor
	// (unfinished-submission replay, manifest GC); Close cancels it.
	gcCtx    context.Context
	gcCancel context.CancelFunc
}

var ResetCodeMax = 900000

const MaxMultipartFormParseMemory = 10 * 1024 * 1024

// Validation limits and token lifetimes shared by the service methods.

const (
	MinPasswordLength          = 8
	MaxNicknameLength          = 16
	MaxStudentIdLength         = 16
	UserTokenValidDuration     = 3 * time.Hour
	TransferTokenValidDuration = 1 * time.Minute
	MIMESniffLength            = 512
)

const PasswordResetSubject = "Autograder 密码重置验证码"

const PasswordResetTemplate = "您的密码重置验证码为：%s，10 分钟内有效。\nAutograder"

const PasswordResetRepoType = "password_reset"

const SignUpSubject = "Autograder 注册验证码"

const SignUpTemplate = "您的注册验证码为：%s，10 分钟内有效。\nAutograder"

const SignUpRepoType = "sign_up"

// errIncorrectPassword aborts an atomic user update (without writing) when the
// supplied old password does not match the stored hash.

var errIncorrectPassword = errors.New("incorrect password")

var UsernameRegExp = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{1,40}$")

var EmailCodeValidDuration = 10 * time.Minute

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
		return nil, status.Error(codes.Internal, CodeGetUsers)
	}
	var entries []*autograder_pb.ExportAssignmentGradesResponse_Entry
	for _, member := range members {
		userId := member.GetUserId()
		dbUser, _ := a.userRepo.GetUserById(ctx, userId)
		submissions, err := a.getSubmissionHistory(ctx, userId, assignmentId)
		if err != nil {
			zap.L().Error(
				"ExportAssignmentGrades.GetSubmissionHistory", zap.Uint64("userId", userId),
				zap.Uint64("assignmentId", assignmentId), zap.Error(err),
			)
			continue
		}

		for _, sub := range submissions {
			report, _ := a.submissionReportRepo.GetSubmissionReport(ctx, sub.GetSubmissionId())
			entry := &autograder_pb.ExportAssignmentGradesResponse_Entry{
				UserId:          userId,
				Username:        dbUser.Username,
				StudentId:       dbUser.StudentId,
				Nickname:        dbUser.Nickname,
				SubmissionCount: uint64(len(submissions)),
				Score:           sub.GetScore(),
				MaxScore:        sub.GetMaxScore(),
				SubmissionId:    sub.GetSubmissionId(),
				Leaderboard:     report.GetLeaderboard(),
				SubmitAt:        sub.GetSubmittedAt(),
			}
			entries = append(entries, entry)
		}
	}
	return &autograder_pb.ExportAssignmentGradesResponse{Entries: entries}, nil
}

func (a *AutograderService) DeleteLeaderboard(
	ctx context.Context, request *autograder_pb.DeleteLeaderboardRequest,
) (*autograder_pb.DeleteLeaderboardResponse, error) {
	_ = a.leaderboardRepo.DeleteLeaderboardEntry(ctx, request.GetAssignmentId(), request.GetUserId())
	return &autograder_pb.DeleteLeaderboardResponse{}, nil
}

type autograderServiceDeps struct {
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
	githubOAuth2Config   *oauth2.Config
	graderHubSvc         *grader_grpc.GraderHubService
	userJWTSignKey       []byte
	uploadJWTSignKey     []byte
	downloadJWTSignKey   []byte
}

// newAutograderService assembles the service from injected dependencies and
// registers the auth table, but starts no background goroutines, so a test can
// build it over in-memory repositories without the GC/replay loops running.
// Production wiring and those goroutines live in NewAutograderServiceServer.

func newAutograderService(deps autograderServiceDeps) *AutograderService {
	gcCtx, gcCancel := context.WithCancel(context.Background())
	a := &AutograderService{
		manifestRepo:         deps.manifestRepo,
		userRepo:             deps.userRepo,
		submissionRepo:       deps.submissionRepo,
		submissionReportRepo: deps.submissionReportRepo,
		courseRepo:           deps.courseRepo,
		assignmentRepo:       deps.assignmentRepo,
		leaderboardRepo:      deps.leaderboardRepo,
		verificationCodeRepo: deps.verificationCodeRepo,
		progGrader:           deps.progGrader,
		githubOAuth2Config:   deps.githubOAuth2Config,
		mailer:               deps.mailer,
		captchaVerifier:      deps.captchaVerifier,
		ls:                   deps.ls,
		graderHubSvc:         deps.graderHubSvc,
		userJWTSignKey:       deps.userJWTSignKey,
		uploadJWTSignKey:     deps.uploadJWTSignKey,
		downloadJWTSignKey:   deps.downloadJWTSignKey,
		gcCtx:                gcCtx,
		gcCancel:             gcCancel,
	}
	a.initAuthFuncs()
	return a
}

func NewAutograderServiceServer(
	db *pebble.DB, ls *storage.LocalStorage, mailer mailer.Mailer, captchaVerifier *hcaptcha.Client,
	ghOauth2Config *oauth2.Config,
	srr repository.SubmissionReportRepository,
	graderHubSvc *grader_grpc.GraderHubService,
) (*AutograderService, error) {
	manifestRepo, err := repository.NewKVManifestRepository(db)
	if err != nil {
		return nil, fmt.Errorf("manifest repository: %w", err)
	}
	userRepo, err := repository.NewKVUserRepository(db)
	if err != nil {
		return nil, fmt.Errorf("user repository: %w", err)
	}
	submissionRepo, err := repository.NewKVSubmissionRepository(db)
	if err != nil {
		return nil, fmt.Errorf("submission repository: %w", err)
	}
	courseRepo, err := repository.NewKVCourseRepository(db)
	if err != nil {
		return nil, fmt.Errorf("course repository: %w", err)
	}
	assignmentRepo, err := repository.NewKVAssignmentRepository(db)
	if err != nil {
		return nil, fmt.Errorf("assignment repository: %w", err)
	}
	a := newAutograderService(autograderServiceDeps{
		manifestRepo:         manifestRepo,
		userRepo:             userRepo,
		submissionRepo:       submissionRepo,
		submissionReportRepo: srr,
		courseRepo:           courseRepo,
		assignmentRepo:       assignmentRepo,
		leaderboardRepo:      repository.NewKVLeaderboardRepository(db),
		verificationCodeRepo: repository.NewKVVerificationCodeRepository(db),
		progGrader:           grader.NewHubGrader(graderHubSvc),
		githubOAuth2Config:   ghOauth2Config,
		mailer:               mailer,
		captchaVerifier:      captchaVerifier,
		ls:                   ls,
		graderHubSvc:         graderHubSvc,
		userJWTSignKey:       []byte(viper.GetString("token.secret.session")),
		uploadJWTSignKey:     []byte(viper.GetString("token.secret.upload")),
		downloadJWTSignKey:   []byte(viper.GetString("token.secret.download")),
	})
	go a.runUnfinishedSubmissions()
	go a.manifestGarbageCollect()
	go a.verificationCodeRepo.GarbageCollect(a.gcCtx)
	return a, nil
}

// Close stops the background goroutines started by the constructor. It does
// not close the underlying database.

func (a *AutograderService) Close() {
	a.gcCancel()
}
