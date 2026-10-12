package grpc

import (
	"context"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/logging"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/utils"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (a *AutograderService) CanWriteCourse(
	ctx context.Context, request *autograder_pb.CanWriteCourseRequest,
) (*autograder_pb.CanWriteCourseResponse, error) {
	return &autograder_pb.CanWriteCourseResponse{WritePermission: true}, nil
}

func (a *AutograderService) UpdateCourseMember(
	ctx context.Context, request *autograder_pb.UpdateCourseMemberRequest,
) (*autograder_pb.UpdateCourseMemberResponse, error) {
	courseId := request.GetCourseId()
	member := request.GetMember()
	l := logging.Extract(ctx).With(zap.Uint64("courseId", courseId), zap.Uint64("userId", member.GetUserId()))
	l.Debug("UpdateCourseMember")
	dbMember := a.userRepo.GetCourseMember(ctx, member.GetUserId(), courseId)
	if dbMember == nil {
		return nil, status.Error(codes.NotFound, CodeMemberNotFound)
	}
	dbMember.Role = member.GetRole()
	err := a.userRepo.AddCourse(ctx, dbMember)
	if err != nil {
		l.Error("UpdateCourseMember.AddCourse", zap.Error(err))
		return nil, status.Error(codes.InvalidArgument, CodeAddCourse)
	}
	err = a.courseRepo.AddUser(ctx, dbMember)
	if err != nil {
		l.Error("UpdateCourseMember.AddUser", zap.Error(err))
		return nil, status.Error(codes.InvalidArgument, CodeAddUser)
	}
	return &autograder_pb.UpdateCourseMemberResponse{}, nil
}

func (a *AutograderService) UpdateCourse(
	ctx context.Context, request *autograder_pb.UpdateCourseRequest,
) (*autograder_pb.UpdateCourseResponse, error) {
	courseId := request.GetCourseId()

	l := logging.Extract(ctx).With(zap.Uint64("courseId", courseId))
	l.Debug("UpdateCourse")
	course, err := a.courseRepo.GetCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.NotFound, CodeInvalidCourseId)
	}
	course.Description = request.GetDescription()
	course.Name = request.GetName()
	course.ShortName = request.GetShortName()
	err = a.courseRepo.UpdateCourse(ctx, courseId, course)
	if err != nil {
		l.Error("UpdateCourse.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeUpdateCourse)
	}
	return &autograder_pb.UpdateCourseResponse{}, nil
}

func (a *AutograderService) AddCourseMembers(
	ctx context.Context, request *autograder_pb.AddCourseMembersRequest,
) (*autograder_pb.AddCourseMembersResponse, error) {
	courseId := request.GetCourseId()
	membersToAdd := request.GetMembers()
	l := logging.Extract(ctx).With(zap.Uint64("courseId", courseId))
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
			existId, _ := a.userRepo.GetUserIdByUsername(ctx, newUsername)
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
	l := logging.Extract(ctx).With(zap.Uint64("courseId", courseId))
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
	l := logging.Extract(ctx)
	courseId := request.GetCourseId()
	members, err := a.courseRepo.GetUsersByCourse(ctx, request.GetCourseId())
	if err != nil {
		l.Error("GetCourseMembers.GetUsersByCourse", zap.Uint64("courseId", courseId), zap.Error(err))
		return nil, status.Error(codes.Internal, CodeGetUsers)
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

func (a *AutograderService) CreateCourse(
	ctx context.Context, request *autograder_pb.CreateCourseRequest,
) (*autograder_pb.CreateCourseResponse, error) {
	l := logging.Extract(ctx)
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	resp := &autograder_pb.CreateCourseResponse{}
	if len(request.GetName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, CodeName)
	}
	if len(request.GetShortName()) == 0 {
		return nil, status.Error(codes.InvalidArgument, CodeShortName)
	}
	if len(request.GetDescription()) == 0 {
		return nil, status.Error(codes.InvalidArgument, CodeDescription)
	}

	course := &model_pb.Course{
		Name:        request.GetName(),
		ShortName:   request.GetShortName(),
		Description: request.GetDescription(),
	}

	courseId, err := a.courseRepo.CreateCourse(ctx, course)
	if err != nil {
		l.Error("CreateCourse.CreateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeCreateCourse)
	}

	member := &model_pb.CourseMember{
		UserId:   user.GetUserId(),
		CourseId: courseId,
		Role:     model_pb.CourseRole_Instructor,
	}

	err = a.courseRepo.AddUser(ctx, member)
	if err != nil {
		l.Error("CreateCourse.AddUser", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeAddUser)
	}

	err = a.userRepo.AddCourse(ctx, member)
	if err != nil {
		l.Error("CreateCourse.AddCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeAddCourse)
	}

	resp.CourseId = courseId
	resp.Course = course
	return resp, nil
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

func (a *AutograderService) GetCourseList(
	ctx context.Context, request *autograder_pb.GetCourseListRequest,
) (*autograder_pb.GetCourseListResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	var courses []*autograder_pb.GetCourseListResponse_CourseCardInfo
	courseMembers, err := a.userRepo.GetCoursesByUser(ctx, user.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetCourses)
	}
	for _, member := range courseMembers {
		course, err := a.courseRepo.GetCourse(ctx, member.CourseId)
		if err != nil {
			return nil, status.Error(codes.Internal, CodeGetCourse)
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

func (a *AutograderService) JoinCourse(
	ctx context.Context, request *autograder_pb.JoinCourseRequest,
) (*autograder_pb.JoinCourseResponse, error) {
	joinCode := request.GetJoinCode()
	// Resolve the course via the join-code index; fall back to the legacy
	// Base30 decoding for codes generated before the index existed.
	courseId, err := a.courseRepo.GetCourseIdByJoinCode(ctx, joinCode)
	if err != nil {
		courseId = utils.Base30Decode(joinCode)
	}
	course, err := a.courseRepo.GetCourse(ctx, courseId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidCourseId)
	}
	if !course.AllowsJoin || course.GetJoinCode() != joinCode {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidJoinCode)
	}
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	userId := user.GetUserId()
	member := a.userRepo.GetCourseMember(ctx, userId, courseId)
	if member != nil {
		return nil, status.Error(codes.AlreadyExists, CodeAlreadyInCourse)
	}
	newMember := &model_pb.CourseMember{
		CourseId: courseId,
		UserId:   userId,
		Role:     model_pb.CourseRole_Student,
	}
	err = a.userRepo.AddCourse(ctx, newMember)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeAddCourse)
	}
	err = a.courseRepo.AddUser(ctx, newMember)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeAddUser)
	}
	return &autograder_pb.JoinCourseResponse{CourseId: courseId}, nil
}

func (a *AutograderService) GenerateJoinCode(
	ctx context.Context, request *autograder_pb.GenerateJoinCodeRequest,
) (*autograder_pb.GenerateJoinCodeResponse, error) {
	l := logging.Extract(ctx).With(zap.Uint64("courseId", request.GetCourseId()))
	code := utils.Base30Encode(request.GetCourseId())
	course, err := a.courseRepo.GetCourse(ctx, request.GetCourseId())
	if err != nil {
		l.Error("GenerateJoinCode.GetCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeGetCourse)
	}
	// Drop the previous code's index entry before rebinding the new one.
	if old := course.GetJoinCode(); old != "" && old != code {
		_ = a.courseRepo.UnbindJoinCode(ctx, old)
	}
	course.JoinCode = code
	err = a.courseRepo.UpdateCourse(ctx, request.GetCourseId(), course)
	if err != nil {
		l.Error("GenerateJoinCode.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeUpdateCourse)
	}
	if err := a.courseRepo.BindJoinCode(ctx, code, request.GetCourseId()); err != nil {
		l.Error("GenerateJoinCode.BindJoinCode", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeUpdateCourse)
	}
	return &autograder_pb.GenerateJoinCodeResponse{JoinCode: code}, nil
}

func (a *AutograderService) ChangeAllowsJoinCourse(
	ctx context.Context, request *autograder_pb.ChangeAllowsJoinCourseRequest,
) (*autograder_pb.ChangeAllowsJoinCourseResponse, error) {
	l := logging.Extract(ctx).With(zap.Uint64("courseId", request.GetCourseId()))
	course, err := a.courseRepo.GetCourse(ctx, request.GetCourseId())
	if err != nil {
		l.Error("GenerateJoinCode.GetCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeGetCourse)
	}
	course.AllowsJoin = request.GetAllowsJoin()
	err = a.courseRepo.UpdateCourse(ctx, request.GetCourseId(), course)
	if err != nil {
		l.Error("GenerateJoinCode.UpdateCourse", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeUpdateCourse)
	}
	return &autograder_pb.ChangeAllowsJoinCourseResponse{AllowsJoin: course.AllowsJoin}, nil
}

func (a *AutograderService) GetAllCourses(
	ctx context.Context, request *autograder_pb.GetAllCoursesRequest,
) (*autograder_pb.GetAllCoursesResponse, error) {
	courses, ids, err := a.courseRepo.GetAllCourses(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetAllCourses)
	}
	var courseInfos []*autograder_pb.GetAllCoursesResponse_CourseInfo
	for i := 0; i < len(courses); i++ {
		courseInfos = append(
			courseInfos, &autograder_pb.GetAllCoursesResponse_CourseInfo{
				CourseId: ids[i],
				Course:   courses[i],
			},
		)
	}
	return &autograder_pb.GetAllCoursesResponse{Courses: courseInfos}, nil
}

// autograderServiceDeps bundles the collaborators of AutograderService so the
// service can be assembled from injected (and, in tests, faked or in-memory)
// dependencies. NewAutograderServiceServer wires the production
// implementations; newAutograderService does the assembly.
