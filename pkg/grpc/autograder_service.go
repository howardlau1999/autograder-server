package grpc

import (
	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/grader"
	"autograder-server/pkg/mailer"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"autograder-server/pkg/storage"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/avast/retry-go"
	"github.com/cockroachdb/pebble"
	"github.com/go-chi/chi"
	"github.com/gogo/protobuf/sortkeys"
	"github.com/golang-jwt/jwt"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap/ctxzap"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"math/rand"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
	reportSubs           map[uint64][]chan *model_pb.SubmissionReport
	subsMu               *sync.Mutex
	authFuncs            map[string][]MethodAuthFunc
}

var DownloadJWTSignKey = []byte("download-token-sign-key")
var UploadJWTSignKey = []byte("upload-token-sign-key")
var UserJWTSignKey = []byte("user-token-sign-key")
var ResetCodeMax = 900000

func (a *AutograderService) sendPasswordResetCode(to string, code string) error {
	return a.mailer.SendMail(
		os.Getenv("SMTP_FROM"),
		[]string{to},
		"Autograder 密码重置验证码",
		fmt.Sprintf("您的密码重置验证码为：%s，10 分钟内有效", code),
	)
}

func (a *AutograderService) ResetPassword(ctx context.Context, request *autograder_pb.ResetPasswordRequest) (*autograder_pb.ResetPasswordResponse, error) {
	l := ctxzap.Extract(ctx)
	email := request.GetEmail()
	code := request.GetCode()
	l.Debug("ResetPassword", zap.String("email", email), zap.String("code", code))
	valid, _ := a.verificationCodeRepo.Validate("password_reset", email, code)
	if !valid {
		return nil, status.Error(codes.InvalidArgument, "INVALID_CODE")
	}
	user, userId, err := a.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		l.Error("ResetPassword.GetUser", zap.String("email", email), zap.Error(err))
		return nil, status.Error(codes.Internal, "INTERNAL_ERROR")
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
func (a *AutograderService) RequestPasswordReset(ctx context.Context, request *autograder_pb.RequestPasswordResetRequest) (*autograder_pb.RequestPasswordResetResponse, error) {
	l := ctxzap.Extract(ctx)
	email := request.GetEmail()
	userId, _ := a.userRepo.GetUserIdByEmail(ctx, email)
	if userId == 0 {
		return nil, nil
	}
	l.Debug("RequestPasswordReset", zap.String("email", email))
	codeInt := rand.Intn(ResetCodeMax)
	codeInt += 100000
	code := fmt.Sprintf("%d", codeInt)
	l.Debug("RequestPasswordReset.CodeGenerated", zap.String("code", code))
	a.verificationCodeRepo.Issue("password_reset", email, code, time.Now().Add(10*time.Minute))
	go func() {
		err := retry.Do(func() error {
			err := a.sendPasswordResetCode(email, code)
			if err == nil {
				l.Debug("RequestPasswordReset.MailSent")
			}
			return err
		}, retry.Attempts(3))
		if err != nil {
			l.Error("RequestPasswordReset.SendMail", zap.Error(err))
		}
	}()
	return &autograder_pb.RequestPasswordResetResponse{}, nil
}

func (a *AutograderService) UpdateCourseMember(ctx context.Context, request *autograder_pb.UpdateCourseMemberRequest) (*autograder_pb.UpdateCourseMemberResponse, error) {
	courseId := request.GetCourseId()
	member := request.GetMember()
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId), zap.Uint64("userId", member.GetUserId()))
	l.Debug("UpdateCourseMember")
	dbMember := a.userRepo.GetCourseMember(ctx, courseId, member.GetUserId())
	if dbMember == nil {
		return nil, status.Error(codes.InvalidArgument, "MEMBER_NOT_FOUND")
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

func (a *AutograderService) UpdateCourse(ctx context.Context, request *autograder_pb.UpdateCourseRequest) (*autograder_pb.UpdateCourseResponse, error) {
	courseId := request.GetCourseId()
	course := request.GetCourse()
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId))
	l.Debug("UpdateCourse")
	_, err := a.courseRepo.GetCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_COURSE_ID")
	}
	err = a.courseRepo.UpdateCourse(ctx, courseId, course)
	if err != nil {
		l.Error("UpdateCourse.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "UPDATE_COURSE")
	}
	return &autograder_pb.UpdateCourseResponse{}, nil
}

func (a *AutograderService) UpdateAssignment(ctx context.Context, request *autograder_pb.UpdateAssignmentRequest) (*autograder_pb.UpdateAssignmentResponse, error) {
	assignmentId := request.GetAssignmentId()
	assignment := request.GetAssignment()
	l := ctxzap.Extract(ctx).With(zap.Uint64("assignmentId", assignmentId))
	l.Debug("UpdateAssignment")
	_, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_COURSE_ID")
	}
	err = a.assignmentRepo.UpdateAssignment(ctx, assignmentId, assignment)
	if err != nil {
		l.Error("UpdateAssignment.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, "UPDATE_COURSE")
	}
	return &autograder_pb.UpdateAssignmentResponse{}, nil
}

func (a *AutograderService) AddCourseMembers(ctx context.Context, request *autograder_pb.AddCourseMembersRequest) (*autograder_pb.AddCourseMembersResponse, error) {
	courseId := request.GetCourseId()
	membersToAdd := request.GetMembers()
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId))
	var added []*model_pb.CourseMember
	for _, memberToAdd := range membersToAdd {
		var err error
		userId, _ := a.userRepo.GetUserIdByEmail(ctx, memberToAdd.GetEmail())
		if userId == 0 {
			newUser := &model_pb.User{Email: memberToAdd.GetEmail(), Username: memberToAdd.GetName()}
			userId, err = a.userRepo.CreateUser(ctx, newUser)
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

func (a *AutograderService) RemoveCourseMembers(ctx context.Context, request *autograder_pb.RemoveCourseMembersRequest) (*autograder_pb.RemoveCourseMembersResponse, error) {
	membersToRemove := request.GetUserIds()
	courseId := request.GetCourseId()
	var removed []uint64
	l := ctxzap.Extract(ctx).With(zap.Uint64("courseId", courseId))
	for _, memberToRemove := range membersToRemove {
		ml := l.With(zap.Uint64("userId", memberToRemove))
		err := a.userRepo.RemoveCourseMember(ctx, courseId, memberToRemove)
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

func (a *AutograderService) GetCourseMembers(ctx context.Context, request *autograder_pb.GetCourseMembersRequest) (*autograder_pb.GetCourseMembersResponse, error) {
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
		respMembers = append(respMembers, &autograder_pb.GetCourseMembersResponse_MemberInfo{
			Username: user.GetUsername(),
			UserId:   userId,
			Role:     member.GetRole(),
			Email:    user.GetEmail(),
			Nickname: user.GetNickname(),
		})
	}
	resp.Members = respMembers
	return resp, nil
}

func (a *AutograderService) InitDownload(ctx context.Context, request *autograder_pb.InitDownloadRequest) (*autograder_pb.InitDownloadResponse, error) {
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	submissionId := request.GetSubmissionId()
	sub, err := a.submissionRepo.GetSubmission(ctx, submissionId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SUBMISSION_ID")
	}
	realpath := filepath.Join(sub.GetPath(), filename)
	_, fn := path.Split(filename)
	file, err := a.ls.Open(ctx, realpath)
	if err != nil {
		return nil, status.Error(codes.Internal, "OPEN_FILE")
	}
	defer file.Close()
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
	resp := &autograder_pb.InitDownloadResponse{FileType: fileTypePB, Token: ss, Filename: fn}
	return resp, nil

}

func isFilenameInvalid(filename string) bool {
	return len(filename) == 0 || strings.Contains(filename, "..") || filepath.IsAbs(filename)
}

func (a *AutograderService) getManifestPath(manifestId uint64) string {
	return fmt.Sprintf("uploads/manifests/%d", manifestId)
}

func (a *AutograderService) DeleteFileInManifest(ctx context.Context, request *autograder_pb.DeleteFileInManifestRequest) (*autograder_pb.DeleteFileInManifestResponse, error) {
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	manifestId := request.GetManifestId()
	err := a.ls.Delete(ctx, filepath.Join(a.getManifestPath(manifestId), filename))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "DELETE_FAILED")
	}
	return &autograder_pb.DeleteFileInManifestResponse{}, nil
}

func (a *AutograderService) CreateAssignment(ctx context.Context, request *autograder_pb.CreateAssignmentRequest) (*autograder_pb.CreateAssignmentResponse, error) {
	resp := &autograder_pb.CreateAssignmentResponse{}
	assignment := &model_pb.Assignment{}
	assignment.Name = request.GetName()
	assignment.ReleaseDate = request.GetReleaseDate()
	assignment.DueDate = request.GetDueDate()
	assignment.AssignmentType = request.GetAssignmentType()
	assignment.Description = request.GetDescription()
	assignment.CourseId = request.GetCourseId()
	assignment.ProgrammingConfig = request.GetProgrammingConfig()
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
	return resp, nil
}

func (a *AutograderService) CreateCourse(ctx context.Context, request *autograder_pb.CreateCourseRequest) (*autograder_pb.CreateCourseResponse, error) {
	resp := &autograder_pb.CreateCourseResponse{}
	course := &model_pb.Course{Name: request.GetName(), ShortName: request.GetShortName(), Description: request.GetDescription()}
	courseId, err := a.courseRepo.CreateCourse(ctx, course)
	if err != nil {
		grpclog.Errorf("failed to create course: %v", err)
		return nil, status.Error(codes.Internal, "CREATE_COURSE")
	}
	member := &model_pb.CourseMember{UserId: request.GetUserId(), CourseId: courseId, Role: model_pb.CourseRole_Instructor}
	err = a.courseRepo.AddUser(ctx, member)
	if err != nil {
		grpclog.Errorf("failed to add user to course: %v", err)
		return nil, status.Error(codes.Internal, "ADD_USER")
	}
	err = a.userRepo.AddCourse(ctx, member)
	if err != nil {
		grpclog.Errorf("failed to add course to user: %v", err)
		return nil, status.Error(codes.Internal, "ADD_COURSE")
	}
	return resp, nil
}

func (a *AutograderService) HasLeaderboard(ctx context.Context, request *autograder_pb.HasLeaderboardRequest) (*autograder_pb.HasLeaderboardResponse, error) {
	resp := &autograder_pb.HasLeaderboardResponse{
		HasLeaderboard: a.leaderboardRepo.HasLeaderboard(ctx, request.GetAssignmentId()),
	}
	return resp, nil
}

func (a *AutograderService) GetLeaderboard(ctx context.Context, request *autograder_pb.GetLeaderboardRequest) (*autograder_pb.GetLeaderboardResponse, error) {
	entries, err := a.leaderboardRepo.GetLeaderboard(ctx, request.GetAssignmentId())
	resp := &autograder_pb.GetLeaderboardResponse{Entries: entries}
	return resp, err
}

func walkDir(dirpath string, relpath string, node *autograder_pb.FileTreeNode) error {
	f, err := os.Open(dirpath)
	if err != nil {
		return err
	}
	defer f.Close()
	dirs, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		nodeType := autograder_pb.FileTreeNode_File
		if dir.IsDir() {
			nodeType = autograder_pb.FileTreeNode_Folder
		}
		p := path.Join(relpath, dir.Name())
		node.Children = append(node.Children, &autograder_pb.FileTreeNode{Name: dir.Name(), NodeType: nodeType, Path: p})
		child := node.Children[len(node.Children)-1]
		if dir.IsDir() {
			err = walkDir(filepath.Join(dirpath, dir.Name()), p, child)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *AutograderService) GetFilesInSubmission(ctx context.Context, request *autograder_pb.GetFilesInSubmissionRequest) (*autograder_pb.GetFilesInSubmissionResponse, error) {
	submission, err := a.submissionRepo.GetSubmission(ctx, request.GetSubmissionId())
	if err != nil {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	subDir := submission.GetPath()
	root := &autograder_pb.FileTreeNode{}
	err = walkDir(subDir, "", root)
	if err != nil {
		grpclog.Errorf("failed to list files in %s: %v", subDir, err)
		return nil, status.Error(codes.Internal, "WALK_DIR")
	}
	rootPB := &autograder_pb.GetFilesInSubmissionResponse{Roots: root.GetChildren()}
	return rootPB, nil
}

func (a *AutograderService) GetCourse(ctx context.Context, request *autograder_pb.GetCourseRequest) (*autograder_pb.GetCourseResponse, error) {
	course, err := a.courseRepo.GetCourse(ctx, request.GetCourseId())
	resp := &autograder_pb.GetCourseResponse{Course: course}
	return resp, err
}

func (a *AutograderService) GetAssignment(ctx context.Context, request *autograder_pb.GetAssignmentRequest) (*autograder_pb.GetAssignmentResponse, error) {
	assignment, err := a.assignmentRepo.GetAssignment(ctx, request.GetAssignmentId())
	resp := &autograder_pb.GetAssignmentResponse{Assignment: assignment}
	return resp, err
}

func (a *AutograderService) GetSubmissionReport(ctx context.Context, request *autograder_pb.GetSubmissionReportRequest) (*autograder_pb.GetSubmissionReportResponse, error) {
	submissionId := request.GetSubmissionId()
	brief, err := a.submissionReportRepo.GetSubmissionBriefReport(ctx, submissionId)
	if brief == nil {
		return nil, status.Error(codes.NotFound, "NOT_FOUND")
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Running {
		return nil, status.Error(codes.NotFound, "RUNNING")
	}
	report, err := a.submissionReportRepo.GetSubmissionReport(ctx, submissionId)
	resp := &autograder_pb.GetSubmissionReportResponse{Report: report, Status: brief.GetStatus()}
	return resp, err
}

func (a *AutograderService) CreateManifest(ctx context.Context, request *autograder_pb.CreateManifestRequest) (*autograder_pb.CreateManifestResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	id, err := a.manifestRepo.CreateManifest(user.GetUserId(), request.GetAssignmentId())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create manifest")
	}
	resp := &autograder_pb.CreateManifestResponse{ManifestId: id}
	return resp, nil
}

func (a *AutograderService) CreateSubmission(ctx context.Context, request *autograder_pb.CreateSubmissionRequest) (*autograder_pb.CreateSubmissionResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	manifestId := request.GetManifestId()
	assignmentId := request.GetAssignmentId()
	submitters := request.GetSubmitters()
	submissionPath := a.getManifestPath(manifestId)
	files, err := a.manifestRepo.GetFilesInManifest(manifestId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "MANIFEST_FILES")
	}
	err = a.manifestRepo.DeleteManifest(manifestId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "DELETE_MANIFEST")
	}
	submission := &model_pb.Submission{
		AssignmentId:    assignmentId,
		SubmittedAt:     timestamppb.Now(),
		Submitters:      submitters,
		Path:            submissionPath,
		Files:           files,
		LeaderboardName: request.GetLeaderboardName(),
		UserId:          user.GetUserId(),
	}
	id, err := a.submissionRepo.CreateSubmission(ctx, submission)
	brief := &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Running}
	err = a.submissionReportRepo.UpdateSubmissionBriefReport(ctx, id, brief)
	if err != nil {
		return nil, status.Error(codes.Internal, "UPDATE_BRIEF")
	}
	err = a.submissionReportRepo.MarkUnfinishedSubmission(ctx, id, assignmentId)
	if err != nil {
		return nil, status.Error(codes.Internal, "MARK_UNFINISHED")
	}
	err = a.manifestRepo.DeleteManifest(manifestId)
	if err != nil {
		return nil, status.Error(codes.Internal, "DELETE_MANIFEST")
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
	claims := ProtobufClaim{Payload: base64.StdEncoding.EncodeToString(raw), StandardClaims: jwt.StandardClaims{IssuedAt: now.Unix(), ExpiresAt: expireAt.Unix()}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return "", status.Error(codes.Internal, "JWT_SIGN")
	}
	return ss, nil
}

func (a *AutograderService) InitUpload(ctx context.Context, request *autograder_pb.InitUploadRequest) (*autograder_pb.InitUploadResponse, error) {
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {

		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	_, err := a.manifestRepo.AddFileToManifest(filename, request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to add file to manifest")
	}
	key := UploadJWTSignKey

	payload := &autograder_pb.UploadTokenPayload{
		ManifestId: request.ManifestId,
		Filename:   path.Clean(filename),
	}
	ss, err := a.signPayloadToken(key, payload, time.Now().Add(1*time.Minute))
	if err != nil {
		return nil, err
	}
	resp := &autograder_pb.InitUploadResponse{Token: ss}
	return resp, nil
}

func (a *AutograderService) SubscribeSubmission(request *autograder_pb.SubscribeSubmissionRequest, server autograder_pb.AutograderService_SubscribeSubmissionServer) error {
	c := make(chan *model_pb.SubmissionReport)
	id := request.GetSubmissionId()
	var idx int
	a.subsMu.Lock()
	a.reportSubs[id] = append(a.reportSubs[id], c)
	idx = len(a.reportSubs) - 1
	a.subsMu.Unlock()
	select {
	case <-server.Context().Done():
		a.subsMu.Lock()
		if len(a.reportSubs[id]) > idx {
			a.reportSubs[id][idx] = nil
		}
		a.subsMu.Unlock()
		return nil
	case r := <-c:
		return server.Send(&autograder_pb.SubscribeSubmissionResponse{
			Score:    r.GetScore(),
			MaxScore: r.GetMaxScore(),
			Status:   model_pb.SubmissionStatus_Finished,
		})
	}
}

func (a *AutograderService) GetSubmissionsInAssignment(ctx context.Context, request *autograder_pb.GetSubmissionsInAssignmentRequest) (*autograder_pb.GetSubmissionsInAssignmentResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	var submissions []*autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo
	subIds, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, user.GetUserId(), request.GetAssignmentId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "NOT_FOUND")
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
		var submitters []*autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo_Submitter
		for _, uid := range sub.Submitters {
			user, err := a.userRepo.GetUserById(ctx, uid)
			if err != nil {
				return nil, status.Error(codes.Internal, "GET_USER")
			}
			oneSubmitter := &autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo_Submitter{
				UserId:   uid,
				Username: user.Username,
			}
			submitters = append(submitters, oneSubmitter)
		}
		ret := &autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo{
			SubmissionId: subId,
			SubmittedAt:  sub.SubmittedAt,
			Submitters:   submitters,
			Score:        score,
			MaxScore:     maxScore,
			Status:       subStatus,
		}
		submissions = append(submissions, ret)
	}
	resp := &autograder_pb.GetSubmissionsInAssignmentResponse{Submissions: submissions}
	return resp, nil
}

func (a *AutograderService) GetAssignmentsInCourse(ctx context.Context, request *autograder_pb.GetAssignmentsInCourseRequest) (*autograder_pb.GetAssignmentsInCourseResponse, error) {
	var assignments []*autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo
	assignmentIds, err := a.courseRepo.GetAssignmentsByCourse(ctx, request.GetCourseId())
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_ASSIGNMENTS")
	}
	for _, asgnId := range assignmentIds {
		assignment, err := a.assignmentRepo.GetAssignment(ctx, asgnId)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_ASSIGNMENT")
		}
		ret := &autograder_pb.GetAssignmentsInCourseResponse_CourseAssignmentInfo{
			AssignmentId: asgnId,
			Name:         assignment.Name,
			ReleaseDate:  assignment.ReleaseDate,
			DueDate:      assignment.DueDate,
			Submitted:    true,
		}
		assignments = append(assignments, ret)
	}
	response := &autograder_pb.GetAssignmentsInCourseResponse{
		Assignments: assignments,
	}
	return response, nil
}

func (a *AutograderService) GetCourseList(ctx context.Context, request *autograder_pb.GetCourseListRequest) (*autograder_pb.GetCourseListResponse, error) {
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

func (a *AutograderService) Login(ctx context.Context, request *autograder_pb.LoginRequest) (*autograder_pb.LoginResponse, error) {
	l := ctxzap.Extract(ctx)
	username := request.GetUsername()
	password := request.GetPassword()
	l.Debug("login", zap.String("username", username))
	user, id, err := a.userRepo.GetUserByUsername(ctx, username)
	if user == nil || err != nil {
		return nil, status.Error(codes.InvalidArgument, "WRONG_PASSWORD")
	}
	if bcrypt.CompareHashAndPassword(user.GetPassword(), []byte(password)) != nil {
		return nil, status.Error(codes.InvalidArgument, "WRONG_PASSWORD")
	}
	payload := &autograder_pb.UserTokenPayload{
		UserId:   id,
		Username: username,
	}
	ss, err := a.signPayloadToken(UserJWTSignKey, payload, time.Now().Add(3*time.Hour))
	if err != nil {
		return nil, err
	}
	refreshMD := metadata.Pairs("token", ss)
	err = grpc.SetHeader(ctx, refreshMD)
	if err != nil {
		return nil, err
	}
	response := &autograder_pb.LoginResponse{
		UserId: id,
	}
	return response, nil
}

func (a *AutograderService) runSubmission(ctx context.Context, submissionId uint64, assignmentId uint64) {
	assignment, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		grpclog.Errorf("failed to get assignment %d: %v", assignmentId, err)
		return
	}
	submission, err := a.submissionRepo.GetSubmission(ctx, submissionId)
	if err != nil {
		grpclog.Errorf("failed to get submission %d: %v", submissionId, err)
		return
	}
	config := assignment.ProgrammingConfig
	notifyC := make(chan *model_pb.SubmissionReport)
	go a.progGrader.GradeSubmission(submissionId, submission, config, notifyC)
	go func() {
		r := <-notifyC
		a.subsMu.Lock()
		subs := a.reportSubs[submissionId]
		delete(a.reportSubs, submissionId)
		a.subsMu.Unlock()
		for _, sub := range subs {
			if sub != nil {
				sub <- r
			}
		}
		if len(r.Leaderboard) > 0 {
			if err := a.leaderboardRepo.UpdateLeaderboardEntry(ctx, assignmentId, submission.GetUserId(), &model_pb.LeaderboardEntry{
				SubmissionId: submissionId,
				Nickname:     submission.GetLeaderboardName(),
				Items:        r.Leaderboard,
			}); err != nil {
				grpclog.Errorf("failed to update leaderboard: %v", err)
			}
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
		grpclog.Errorf("found unfinished submission %d assignment %d", subId, asgnId)
		go a.runSubmission(ctx, subId, asgnId)
	}
}

func parseTokenPayload(key []byte, tokenString string) ([]byte, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected singning method: %v", token.Header["alg"])
		}

		return key, nil
	})
	if err != nil {
		grpclog.Errorf("failed to parse: %v", err)
		return nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid || claims.Valid() != nil {
		grpclog.Errorf("not valid")
		return nil, err
	}
	payloadString, ok := claims["payload"].(string)
	if !ok {
		grpclog.Errorf("no payload")
		return nil, err
	}

	payload, err := base64.StdEncoding.DecodeString(payloadString)
	if err != nil {
		grpclog.Errorf("base64: %v", err)
		return nil, err
	}
	return payload, nil
}

func (a *AutograderService) HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	downloadTokenString := strings.TrimSpace(r.URL.Query().Get("token"))
	fn := chi.URLParam(r, "filename")
	payload, err := parseTokenPayload(DownloadJWTSignKey, downloadTokenString)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var payloadPB autograder_pb.DownloadTokenPayload
	err = proto.Unmarshal(payload, &payloadPB)
	if err != nil || fn != payloadPB.GetFilename() {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, payloadPB.GetRealPath())
}

func (a *AutograderService) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	var err error
	normalizedContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-type")))
	uploadTokenString := strings.TrimSpace(r.Header.Get("Upload-token"))
	payload, err := parseTokenPayload(UploadJWTSignKey, uploadTokenString)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var payloadPB autograder_pb.UploadTokenPayload
	err = proto.Unmarshal(payload, &payloadPB)
	if err != nil {
		grpclog.Errorf("proto: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(normalizedContentType, "multipart/form-data; boundary") {
		grpclog.Errorf("malformed form")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = r.ParseMultipartForm(10 * 1024 * 1024)
	if err != nil {
		grpclog.Errorf("Parse upload multipart form error: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	uploadFile, _, err := r.FormFile("file")
	if err != nil {
		grpclog.Errorf("Get form file error: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	fileHeader := make([]byte, 512)
	_, err = uploadFile.Read(fileHeader)
	_, err = uploadFile.Seek(0, 0)
	_ = http.DetectContentType(fileHeader)
	destPath := filepath.Join(a.getManifestPath(payloadPB.GetManifestId()), payloadPB.Filename)
	err = a.ls.Put(
		r.Context(),
		destPath,
		uploadFile,
	)
	if err != nil {
		grpclog.Errorf("failed to put file: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	return
}

func NewAutograderServiceServer(ls *storage.LocalStorage, mailer mailer.Mailer) *AutograderService {
	db, err := pebble.Open("db", &pebble.Options{Merger: repository.NewKVMerger()})
	if err != nil {
		panic(err)
	}
	srr := repository.NewKVSubmissionReportRepository(db)
	a := &AutograderService{
		manifestRepo:         repository.NewKVManifestRepository(db),
		userRepo:             repository.NewKVUserRepository(db),
		submissionRepo:       repository.NewKVSubmissionRepository(db),
		submissionReportRepo: srr,
		courseRepo:           repository.NewKVCourseRepository(db),
		assignmentRepo:       repository.NewKVAssignmentRepository(db),
		leaderboardRepo:      repository.NewKVLeaderboardRepository(db),
		verificationCodeRepo: repository.NewKVVerificationCodeRepository(db),
		progGrader:           grader.NewDockerProgrammingGrader(srr),
		mailer:               mailer,
		ls:                   ls,
		reportSubs:           make(map[uint64][]chan *model_pb.SubmissionReport),
		subsMu:               &sync.Mutex{},
	}
	a.initAuthFuncs()
	return a
}
