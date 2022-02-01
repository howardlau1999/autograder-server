package grpc

import (
	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/grader"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/cockroachdb/pebble"
	"github.com/gogo/protobuf/sortkeys"
	"github.com/golang-jwt/jwt"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"path/filepath"
	"sort"
	"strings"
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
	progGrader           grader.ProgrammingGrader
}

func (a *AutograderService) GetSubmissionReport(ctx context.Context, request *autograder_pb.GetSubmissionReportRequest) (*autograder_pb.GetSubmissionReportResponse, error) {
	submissionId := request.GetSubmissionId()
	report, err := a.submissionReportRepo.GetSubmissionReport(ctx, submissionId)
	resp := &autograder_pb.GetSubmissionReportResponse{Report: report}
	return resp, err
}

func (a *AutograderService) GetFile(request *autograder_pb.GetFileRequest, server autograder_pb.AutograderService_GetFileServer) error {
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
	manifestId := request.GetManifestId()
	assignmentId := request.GetAssignmentId()
	submitters := request.GetSubmitters()
	path := fmt.Sprintf("uploads/manifests/%d", manifestId)
	files, err := a.manifestRepo.GetFilesInManifest(manifestId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "MANIFEST_FILES")
	}
	err = a.manifestRepo.DeleteManifest(manifestId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "DELETE_MANIFEST")
	}
	submission := &model_pb.Submission{
		AssignmentId: assignmentId,
		SubmittedAt:  timestamppb.Now(),
		Submitters:   submitters,
		Path:         path,
		Files:        files,
	}
	id, err := a.submissionRepo.CreateSubmission(ctx, submission)
	resp := &autograder_pb.CreateSubmissionResponse{SubmissionId: id, Files: files}
	assignment, err := a.assignmentRepo.GetAssignment(ctx, assignmentId)
	if err != nil {
		return nil, status.Error(codes.Internal, "GET_ASSIGNMENT")
	}
	config := assignment.ProgrammingConfig
	go a.progGrader.GradeSubmission(id, submission, config)
	return resp, nil
}

type ProtobufClaim struct {
	jwt.StandardClaims
	Payload string `json:"payload"`
}

func (a *AutograderService) InitUpload(ctx context.Context, request *autograder_pb.InitUploadRequest) (*autograder_pb.InitUploadResponse, error) {
	_, err := a.manifestRepo.AddFileToManifest(request.GetFilename(), request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to add file to manifest")
	}
	key := []byte("upload-token-sign-secret")
	filename := request.GetFilename()
	if strings.Contains(filename, "..") || filepath.IsAbs(filename) {
		return nil, status.Error(codes.InvalidArgument, "INVALID_FILENAME")
	}
	payload := &autograder_pb.UploadTokenPayload{
		ManifestId: request.ManifestId,
		Filename:   filepath.Clean(filename),
	}
	raw, err := proto.Marshal(payload)
	if err != nil {
		return nil, status.Error(codes.Internal, "PROTOBUF_MARSHAL")
	}
	now := time.Now()
	expireAt := now.Add(1 * time.Minute)
	claims := ProtobufClaim{Payload: base64.StdEncoding.EncodeToString(raw), StandardClaims: jwt.StandardClaims{IssuedAt: now.Unix(), ExpiresAt: expireAt.Unix()}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return nil, status.Error(codes.Internal, "JWT_SIGN")
	}
	resp := &autograder_pb.InitUploadResponse{Token: ss}
	return resp, nil
}

func (a *AutograderService) StreamSubmissionLog(request *autograder_pb.StreamSubmissionLogRequest, server autograder_pb.AutograderService_StreamSubmissionLogServer) error {
	return nil
}

func (a *AutograderService) SubscribeSubmissions(request *autograder_pb.SubscribeSubmissionsRequest, server autograder_pb.AutograderService_SubscribeSubmissionsServer) error {
	return nil
}

func (a *AutograderService) GetSubmissionsInAssignment(ctx context.Context, request *autograder_pb.GetSubmissionsInAssignmentRequest) (*autograder_pb.GetSubmissionsInAssignmentResponse, error) {
	var submissions []*autograder_pb.GetSubmissionsInAssignmentResponse_SubmissionInfo
	subIds, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, request.GetUserId(), request.GetAssignmentId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "NOT_FOUND")
	}
	sort.Sort(sort.Reverse(sortkeys.Uint64Slice(subIds)))
	for _, subId := range subIds {
		sub, err := a.submissionRepo.GetSubmission(ctx, subId)
		if err != nil {
			return nil, status.Error(codes.Internal, "GET_SUBMISSION")
		}
		report, err := a.submissionReportRepo.GetSubmissionReport(ctx, subId)
		subStatus := autograder_pb.SubmissionStatus_Running
		score := uint64(0)
		maxScore := uint64(0)
		if err == nil {
			score = report.Score
			maxScore = report.MaxScore
			subStatus = autograder_pb.SubmissionStatus_Finished
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
	var courses []*autograder_pb.GetCourseListResponse_CourseCardInfo
	courseMembers, err := a.userRepo.GetCoursesByUser(ctx, request.GetUserId())
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
		}
		courses = append(courses, ret)
	}
	response := &autograder_pb.GetCourseListResponse{Courses: courses}
	return response, nil
}

func (a *AutograderService) Login(ctx context.Context, request *autograder_pb.LoginRequest) (*autograder_pb.LoginResponse, error) {
	username := request.GetUsername()
	password := request.GetPassword()
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
	key := []byte("user-token-sign-key")
	raw, err := proto.Marshal(payload)
	if err != nil {
		return nil, status.Error(codes.Internal, "PROTOBUF_MARSHAL")
	}
	now := time.Now()
	expireAt := now.Add(1 * time.Minute)
	claims := ProtobufClaim{Payload: base64.StdEncoding.EncodeToString(raw), StandardClaims: jwt.StandardClaims{IssuedAt: now.Unix(), ExpiresAt: expireAt.Unix()}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return nil, status.Error(codes.Internal, "JWT_SIGN")
	}
	response := &autograder_pb.LoginResponse{
		UserId: id,
		Token:  ss,
	}
	return response, nil
}

func NewAutograderServiceServer() autograder_pb.AutograderServiceServer {
	db, err := pebble.Open("db", &pebble.Options{Merger: repository.NewKVMerger()})
	if err != nil {
		panic(err)
	}
	srr := repository.NewKVSubmissionReportRepository(db)
	return &AutograderService{
		manifestRepo:         repository.NewKVManifestRepository(db),
		userRepo:             repository.NewKVUserRepository(db),
		submissionRepo:       repository.NewKVSubmissionRepository(db),
		submissionReportRepo: srr,
		courseRepo:           repository.NewKVCourseRepository(db),
		assignmentRepo:       repository.NewKVAssignmentRepository(db),
		progGrader:           grader.NewDockerProgrammingGrader(srr),
	}
}
