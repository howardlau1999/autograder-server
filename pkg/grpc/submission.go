package grpc

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/logging"
	model_pb "autograder-server/pkg/model/proto"

	"github.com/cockroachdb/pebble/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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

func (a *AutograderService) GetSubmissionReport(
	ctx context.Context, request *autograder_pb.GetSubmissionReportRequest,
) (*autograder_pb.GetSubmissionReportResponse, error) {
	submissionId := request.GetSubmissionId()
	brief, _ := a.submissionReportRepo.GetSubmissionBriefReport(ctx, submissionId)
	if brief == nil {
		return nil, status.Error(codes.NotFound, CodeNotFound)
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Queued {
		return nil, status.Error(codes.NotFound, CodeQueued)
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Running {
		return nil, status.Error(codes.NotFound, CodeRunning)
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Cancelled {
		return nil, status.Error(codes.NotFound, CodeCancelled)
	}
	if brief.GetStatus() == model_pb.SubmissionStatus_Cancelling {
		return nil, status.Error(codes.NotFound, CodeCancelling)
	}
	report, err := a.submissionReportRepo.GetSubmissionReport(ctx, submissionId)
	if err != nil {
		return nil, status.Error(codes.NotFound, CodeReportNotFound)
	}
	resp := &autograder_pb.GetSubmissionReportResponse{Report: report, Status: brief.GetStatus()}
	return resp, nil
}

func (a *AutograderService) validateSubmitters(
	ctx context.Context, requested []uint64, callerId, courseId uint64, staff bool,
) ([]uint64, error) {
	if len(requested) == 0 {
		requested = []uint64{callerId}
	}
	seen := make(map[uint64]bool, len(requested))
	submitters := make([]uint64, 0, len(requested))
	selfIncluded := false
	for _, sid := range requested {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		if sid == callerId {
			selfIncluded = true
		}
		if a.userRepo.GetCourseMember(ctx, sid, courseId) == nil {
			return nil, status.Error(codes.InvalidArgument, CodeSubmitterNotInCourse)
		}
		submitters = append(submitters, sid)
	}
	if !selfIncluded && !staff {
		return nil, status.Error(codes.PermissionDenied, CodeNotASubmitter)
	}
	return submitters, nil
}

// checkSubmissionLimit enforces the assignment's total and sliding-window
// submission limits for one submitter. The check runs before the new
// submission is created, so reaching the limit (>=) already means rejection.

func (a *AutograderService) checkSubmissionLimit(
	ctx context.Context, userId, assignmentId uint64, limit *model_pb.SubmissionLimitConfig,
) error {
	submissionIds, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, userId, assignmentId)
	if err != nil {
		return status.Error(codes.Internal, CodeGetSubmissions)
	}
	if limit.GetTotal() > 0 && uint64(len(submissionIds)) >= limit.GetTotal() {
		return status.Error(codes.ResourceExhausted, CodeSubmissionLimit)
	}
	if limit.GetFrequency() > 0 && limit.GetPeriod() > 0 {
		windowCount := uint64(0)
		windowStart := time.Now().Add(-time.Minute * time.Duration(limit.GetPeriod()))
		for _, subId := range submissionIds {
			sub, err := a.submissionRepo.GetSubmission(ctx, subId)
			if err != nil {
				return status.Error(codes.Internal, CodeGetSubmission)
			}
			if sub.GetSubmittedAt().AsTime().After(windowStart) {
				windowCount++
			}
		}
		if windowCount >= limit.GetFrequency() {
			return status.Error(codes.ResourceExhausted, CodeSubmissionFrequency)
		}
	}
	return nil
}

func (a *AutograderService) CreateSubmission(
	ctx context.Context, request *autograder_pb.CreateSubmissionRequest,
) (*autograder_pb.CreateSubmissionResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	role := ctx.Value(courseMemberCtxKey{}).(*model_pb.CourseMember).Role
	assignment := ctx.Value(assignmentCtxKey{}).(*model_pb.Assignment)
	manifestId := request.GetManifestId()
	assignmentId := request.GetAssignmentId()
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	staff := role == model_pb.CourseRole_Instructor || role == model_pb.CourseRole_TA

	submitters, err := a.validateSubmitters(ctx, request.GetSubmitters(), user.GetUserId(), courseId, staff)
	if err != nil {
		return nil, err
	}
	// Limits are enforced per submitter over the validated set, so a group
	// cannot bypass them by rotating who is listed first.
	if limit := assignment.GetSubmissionLimit(); limit != nil && !staff {
		for _, sid := range submitters {
			if err := a.checkSubmissionLimit(ctx, sid, assignmentId, limit); err != nil {
				return nil, err
			}
		}
	}
	submissionPath := a.getManifestPath(manifestId)
	files, err := a.manifestRepo.GetFilesInManifest(ctx, manifestId)
	if err != nil || len(files) == 0 {
		return nil, status.Error(codes.InvalidArgument, CodeManifestFiles)
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
	if err != nil {
		return nil, status.Error(codes.Internal, CodeCreateSubmission)
	}
	// Persist the queued brief before returning so a client that subscribes
	// the moment this call returns sees "queued" rather than "not found";
	// runSubmission (async) takes care of the unfinished marker.
	brief := &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Queued}
	if err = a.submissionReportRepo.UpdateSubmissionBriefReport(ctx, id, brief); err != nil {
		return nil, status.Error(codes.Internal, CodeUpdateBrief)
	}
	// From here on the submission exists and will be graded. The remaining
	// steps are cleanup; failing them must not report an error to the client,
	// which would suggest the submission was not accepted.
	l := logging.Extract(ctx).With(zap.Uint64("submissionId", id), zap.Uint64("manifestId", manifestId))
	if err = a.manifestRepo.DeleteManifest(ctx, manifestId); err != nil {
		l.Error("CreateSubmission.DeleteManifest", zap.Error(err))
	}
	err = a.ls.Put(ctx, filepath.Join(submissionPath, ".submission"), strings.NewReader(fmt.Sprintf("%d", id)))
	if err != nil {
		l.Error("CreateSubmission.CreateMarker", zap.Error(err))
	}
	resp := &autograder_pb.CreateSubmissionResponse{SubmissionId: id, Files: files}
	go a.runSubmission(context.Background(), id, assignmentId)
	return resp, nil
}

func (a *AutograderService) SubscribeSubmission(
	request *autograder_pb.SubscribeSubmissionRequest, server autograder_pb.AutograderService_SubscribeSubmissionServer,
) error {
	ctx, err := a.AuthFunc(server.Context(), request, autograder_pb.AutograderService_SubscribeSubmission_FullMethodName)
	if err != nil {
		return err
	}
	id := request.GetSubmissionId()
	l := logging.Extract(ctx).With(zap.Uint64("submissionId", id))
	// Subscribe before reading the brief so a status change between the read
	// and the subscription is still delivered.
	mailbox := a.graderHubSvc.Subscribe(id)
	defer a.graderHubSvc.Unsubscribe(id, mailbox)
	brief, err := a.submissionReportRepo.GetSubmissionBriefReport(ctx, id)
	if err != nil && err != pebble.ErrNotFound {
		l.Error("SubscribeSubmission.GetBrief", zap.Error(err))
		return status.Error(codes.Internal, CodeGetBrief)
	}
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
	if err == nil && isTerminalStatus(brief.GetStatus()) {
		return nil
	}
	l.Debug("SubscribeSubmission.Begin")
	for {
		select {
		case <-ctx.Done():
			return nil
		case r, ok := <-mailbox.Chan():
			if !ok {
				// The hub closed the subscription: the submission finished and
				// its final brief is already persisted. Send that as the last
				// message so the client always observes the terminal state.
				final, err := a.submissionReportRepo.GetSubmissionBriefReport(ctx, id)
				if err != nil {
					return nil
				}
				return server.Send(
					&autograder_pb.SubscribeSubmissionResponse{
						Score: final.GetScore(), MaxScore: final.GetMaxScore(), Status: final.GetStatus(),
					},
				)
			}
			if r.GetBrief() == nil && r.GetPendingRank() == nil {
				continue
			}
			resp := &autograder_pb.SubscribeSubmissionResponse{PendingRank: r.GetPendingRank()}
			if r.GetBrief() != nil {
				resp.Score = r.GetBrief().GetScore()
				resp.MaxScore = r.GetBrief().GetMaxScore()
				resp.Status = r.GetBrief().GetStatus()
			} else {
				// A rank-only update carries no brief; the submission is by
				// definition still queued.
				resp.Status = model_pb.SubmissionStatus_Queued
			}
			if err := server.Send(resp); err != nil {
				return err
			}
			if r.GetBrief() != nil && isTerminalStatus(r.GetBrief().GetStatus()) {
				return nil
			}
		}
	}
}

func isTerminalStatus(s model_pb.SubmissionStatus) bool {
	return s == model_pb.SubmissionStatus_Finished ||
		s == model_pb.SubmissionStatus_Failed ||
		s == model_pb.SubmissionStatus_Cancelled
}

func (a *AutograderService) getSubmissionHistory(
	ctx context.Context, userId, assignmentId uint64,
) ([]*autograder_pb.SubmissionInfo, error) {
	var submissions []*autograder_pb.SubmissionInfo
	subIds, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, userId, assignmentId)
	if err != nil {
		return nil, status.Error(codes.NotFound, CodeNotFound)
	}
	// Newest first.
	slices.SortFunc(subIds, func(a, b uint64) int { return cmp.Compare(b, a) })
	for _, subId := range subIds {
		sub, err := a.submissionRepo.GetSubmission(ctx, subId)
		if err != nil {
			return nil, status.Error(codes.Internal, CodeGetSubmission)
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
				return nil, status.Error(codes.Internal, CodeGetUser)
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

func (a *AutograderService) ActivateSubmission(
	ctx context.Context, request *autograder_pb.ActivateSubmissionRequest,
) (*autograder_pb.ActivateSubmissionResponse, error) {
	submission := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)
	report, err := a.submissionReportRepo.GetSubmissionReport(ctx, request.GetSubmissionId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, CodeGetReport)
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
		return nil, status.Error(codes.Internal, CodeUpdateEntry)
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
	if err = a.submissionReportRepo.MarkUnfinishedSubmission(ctx, submissionId, assignmentId); err != nil {
		logger.Error("RunSubmission.MarkUnfinishedSubmission", zap.Error(err))
	}
	brief := &model_pb.SubmissionBriefReport{Status: model_pb.SubmissionStatus_Queued}
	if err = a.submissionReportRepo.UpdateSubmissionBriefReport(ctx, submissionId, brief); err != nil {
		logger.Error("RunSubmission.UpdateSubmissionBriefReport", zap.Error(err))
	}
	// Progress is delivered to clients by the hub's own subscription registry
	// (see SubscribeSubmission), so no notification channel is needed here.
	a.progGrader.GradeSubmission(context.Background(), submissionId, submission, assignment.ProgrammingConfig, nil)
}

func (a *AutograderService) runUnfinishedSubmissions() {
	ctx := a.gcCtx
	ids, err := a.submissionReportRepo.GetUnfinishedSubmissions(ctx)
	if err != nil {
		// This runs in a background goroutine with no recover above it, so a
		// panic would take the whole server down. Log and skip recovery of
		// in-flight submissions instead.
		zap.L().Error("RunUnfinishedSubmissions.Get", zap.Error(err))
		return
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

func (a *AutograderService) InspectAllSubmissionsInAssignment(
	ctx context.Context, request *autograder_pb.InspectAllSubmissionsInAssignmentRequest,
) (*autograder_pb.InspectAllSubmissionsInAssignmentResponse, error) {
	assignmentId := request.GetAssignmentId()
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	members, err := a.courseRepo.GetUsersByCourse(ctx, courseId)
	l := logging.Extract(ctx).With(zap.Uint64("courseId", courseId), zap.Uint64("assignmentId", assignmentId))
	if err != nil {
		l.Error("InspectAllSubmissionsInAssignment.GetUsers", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeGetUsers)
	}
	var userSubmissionInfo []*autograder_pb.InspectAllSubmissionsInAssignmentResponse_UserSubmissionInfo
	for _, member := range members {
		user, err := a.userRepo.GetUserById(ctx, member.GetUserId())
		if err != nil {
			l.Error("InspectAllSubmissionsInAssignment.GetUser", zap.Error(err))
			return nil, status.Error(codes.Internal, CodeGetUser)
		}
		submissions, err := a.submissionRepo.GetSubmissionsByUserAndAssignment(ctx, member.GetUserId(), assignmentId)
		if err != nil {
			l.Error("InspectAllSubmissionsInAssignment.GetSubmissions", zap.Error(err))
			return nil, status.Error(codes.Internal, CodeGetSubmissions)
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

func (a *AutograderService) regradeSubmission(submissionId uint64, assignmentId uint64) {
	b, _ := a.submissionReportRepo.GetSubmissionBriefReport(context.Background(), submissionId)
	if b != nil && (b.GetStatus() == model_pb.SubmissionStatus_Queued || b.GetStatus() == model_pb.SubmissionStatus_Running) {
		_ = a.graderHubSvc.CancelGrade(context.Background(), submissionId)
	}
	a.runSubmission(context.Background(), submissionId, assignmentId)
}

func (a *AutograderService) RegradeSubmission(
	ctx context.Context, request *autograder_pb.RegradeSubmissionRequest,
) (*autograder_pb.RegradeSubmissionResponse, error) {
	assignmentId := ctx.Value(submissionCtxKey{}).(*model_pb.Submission).AssignmentId
	go a.regradeSubmission(request.SubmissionId, assignmentId)
	return &autograder_pb.RegradeSubmissionResponse{}, nil
}

func (a *AutograderService) RegradeAssignment(
	ctx context.Context, request *autograder_pb.RegradeAssignmentRequest,
) (*autograder_pb.RegradeAssignmentResponse, error) {
	l := logging.Extract(ctx)
	courseId := ctx.Value(courseIdCtxKey{}).(uint64)
	members, err := a.courseRepo.GetUsersByCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetUsers)
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
		go a.regradeSubmission(submissionId, request.GetAssignmentId())
	}
	return &autograder_pb.RegradeAssignmentResponse{}, nil
}

func (a *AutograderService) CancelSubmission(
	ctx context.Context, request *autograder_pb.CancelSubmissionRequest,
) (*autograder_pb.CancelSubmissionResponse, error) {
	l := logging.Extract(ctx).With(zap.Uint64("submissionId", request.GetSubmissionId()))
	l.Debug("CancelSubmission")
	err := a.graderHubSvc.CancelGrade(ctx, request.GetSubmissionId())
	if err != nil {
		l.Error("CancelSubmission.CancelGrade", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeCancelGrade)
	}
	return &autograder_pb.CancelSubmissionResponse{}, nil
}
