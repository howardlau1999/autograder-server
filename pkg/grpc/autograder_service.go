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
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"os"
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
	progGrader           grader.ProgrammingGrader
	reportSubs           map[uint64][]chan *model_pb.SubmissionReport
	subsMu               *sync.Mutex
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
		p := filepath.Join(relpath, dir.Name())
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
		AssignmentId:    assignmentId,
		SubmittedAt:     timestamppb.Now(),
		Submitters:      submitters,
		Path:            path,
		Files:           files,
		LeaderboardName: request.GetLeaderboardName(),
		UserId:          request.GetUserId(),
	}
	id, err := a.submissionRepo.CreateSubmission(ctx, submission)
	brief := &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Running}
	_ = a.submissionReportRepo.UpdateSubmissionBriefReport(ctx, id, brief)
	_ = a.submissionReportRepo.MarkUnfinishedSubmission(ctx, id, assignmentId)
	resp := &autograder_pb.CreateSubmissionResponse{SubmissionId: id, Files: files}
	go a.runSubmission(context.Background(), id, assignmentId)
	return resp, nil
}

type ProtobufClaim struct {
	jwt.StandardClaims
	Payload string `json:"payload"`
}

func (a *AutograderService) signPayloadToken(key []byte, payload proto.Message) (string, error) {
	raw, err := proto.Marshal(payload)
	if err != nil {
		return "", status.Error(codes.Internal, "PROTOBUF_MARSHAL")
	}
	now := time.Now()
	expireAt := now.Add(1 * time.Minute)
	claims := ProtobufClaim{Payload: base64.StdEncoding.EncodeToString(raw), StandardClaims: jwt.StandardClaims{IssuedAt: now.Unix(), ExpiresAt: expireAt.Unix()}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	ss, err := token.SignedString(key)
	if err != nil {
		return "", status.Error(codes.Internal, "JWT_SIGN")
	}
	return ss, nil
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
	ss, err := a.signPayloadToken(key, payload)
	if err != nil {
		return nil, err
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

func (a *AutograderService) SubscribeSubmission(request *autograder_pb.SubscribeSubmissionRequest, server autograder_pb.AutograderService_SubscribeSubmissionServer) error {
	grpclog.Errorf("Subscribe %d", request.GetSubmissionId())
	defer grpclog.Errorf("Unsubscribe %d", request.GetSubmissionId())
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
			CourseId:  member.CourseId,
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
		leaderboardRepo:      repository.NewKVLeaderboardRepository(db),
		reportSubs:           make(map[uint64][]chan *model_pb.SubmissionReport),
		subsMu:               &sync.Mutex{},
	}
}
