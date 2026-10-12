package grpc

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/mail"
	"strings"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/logging"
	model_pb "autograder-server/pkg/model/proto"

	"github.com/avast/retry-go/v4"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

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
		return status.Error(codes.InvalidArgument, CodeCode)
	}
	return nil
}

func (a *AutograderService) ResetPassword(
	ctx context.Context, request *autograder_pb.ResetPasswordRequest,
) (*autograder_pb.ResetPasswordResponse, error) {
	l := logging.Extract(ctx)
	email := request.GetEmail()
	code := request.GetCode()
	l.Debug("ResetPassword", zap.String("email", email), zap.String("code", code))
	if err := a.validateEmailCode(ctx, PasswordResetRepoType, email, code); err != nil {
		return nil, err
	}
	_, userId, err := a.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		l.Error("ResetPassword.GetUser", zap.String("email", email), zap.Error(err))
		return nil, status.Error(codes.NotFound, CodeEmail)
	}
	if len(request.GetPassword()) < MinPasswordLength {
		return nil, status.Error(codes.InvalidArgument, CodePassword)
	}
	err = a.userRepo.UpdateUserAtomic(ctx, userId, func(user *model_pb.User) error {
		hashed, err := bcrypt.GenerateFromPassword([]byte(request.GetPassword()), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		user.Password = hashed
		return nil
	})
	if err != nil {
		l.Error("ResetPassword.UpdateUser", zap.String("email", email), zap.Error(err))
		return nil, status.Error(codes.Internal, CodeInternalError)
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
	l := logging.Extract(ctx).With(zap.String("email", email))
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
		return status.Error(codes.InvalidArgument, CodeUsername)
	}
	if !isMailValid(email) {
		return status.Error(codes.InvalidArgument, CodeEmail)
	}
	userId, _ := a.userRepo.GetUserIdByEmail(ctx, email)
	if userId != 0 {
		return status.Error(codes.AlreadyExists, CodeEmail)
	}
	userId, _ = a.userRepo.GetUserIdByUsername(ctx, username)
	if userId != 0 {
		return status.Error(codes.AlreadyExists, CodeUsername)
	}
	return nil
}

func (a *AutograderService) signUpNewUser(ctx context.Context, email, username string, password string) (
	uint64, error,
) {
	l := logging.Extract(ctx)
	if err := a.validateUsernameAndEmail(ctx, username, email); err != nil {
		return 0, err
	}
	var passwordHash []byte
	var err error
	if len(password) > 0 {
		passwordHash, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			l.Error("SignUp", zap.Error(err))
			return 0, status.Error(codes.Internal, CodePasswordHash)
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
		return 0, status.Error(codes.Internal, CodeCreateUser)
	}
	return userId, nil
}

func (a *AutograderService) SignUp(
	ctx context.Context, request *autograder_pb.SignUpRequest,
) (*autograder_pb.SignUpResponse, error) {
	if err := a.validateEmailCode(ctx, SignUpRepoType, request.GetEmail(), request.GetCode()); err != nil {
		return nil, err
	}
	if len(request.GetPassword()) < MinPasswordLength {
		return nil, status.Error(codes.InvalidArgument, CodePassword)
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
	l := logging.Extract(ctx)
	email := request.GetEmail()
	userId, _ := a.userRepo.GetUserIdByEmail(ctx, email)
	if userId == 0 {
		return nil, nil
	}
	l.Debug("RequestPasswordReset", zap.String("email", email))
	a.requestEmailCode(ctx, PasswordResetSubject, email, PasswordResetRepoType, PasswordResetTemplate)
	return &autograder_pb.RequestPasswordResetResponse{}, nil
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
	ss, err := a.signPayloadToken(a.userJWTSignKey, payload, time.Now().Add(UserTokenValidDuration))
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
	l := logging.Extract(ctx)
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
		return nil, status.Error(codes.InvalidArgument, CodeWrongPassword)
	}
	if err := a.signLoginToken(ctx, id, user.Username, user.Nickname, user.IsAdmin); err != nil {
		return nil, err
	}
	response := &autograder_pb.LoginResponse{
		UserId: id,
	}
	return response, nil
}

func (a *AutograderService) GetUser(
	ctx context.Context, request *autograder_pb.GetUserRequest,
) (*autograder_pb.GetUserResponse, error) {
	l := logging.Extract(ctx)
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	l.Debug("GetUser", zap.String("username", user.GetUsername()))
	dbUser, err := a.userRepo.GetUserById(ctx, user.GetUserId())
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetUser)
	}
	dbUser.Password = nil
	return &autograder_pb.GetUserResponse{User: dbUser}, nil
}

func (a *AutograderService) UpdateUser(
	ctx context.Context, request *autograder_pb.UpdateUserRequest,
) (*autograder_pb.UpdateUserResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	if len(request.GetNickname()) > MaxNicknameLength {
		return nil, status.Error(codes.InvalidArgument, CodeNicknameTooLong)
	}
	if len(request.GetStudentId()) > MaxStudentIdLength {
		return nil, status.Error(codes.InvalidArgument, CodeStudentIdTooLong)
	}
	err := a.userRepo.UpdateUserAtomic(ctx, user.GetUserId(), func(dbUser *model_pb.User) error {
		dbUser.Nickname = request.GetNickname()
		dbUser.StudentId = request.GetStudentId()
		return nil
	})
	if err != nil {
		return &autograder_pb.UpdateUserResponse{Success: false}, nil
	}
	return &autograder_pb.UpdateUserResponse{Success: true}, nil
}

func (a *AutograderService) UpdatePassword(
	ctx context.Context, request *autograder_pb.UpdatePasswordRequest,
) (*autograder_pb.UpdatePasswordResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	err := a.userRepo.UpdateUserAtomic(ctx, user.GetUserId(), func(dbUser *model_pb.User) error {
		if err := bcrypt.CompareHashAndPassword(dbUser.Password, []byte(request.OldPassword)); err != nil {
			return errIncorrectPassword
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		dbUser.Password = hashed
		return nil
	})
	if errors.Is(err, errIncorrectPassword) {
		return &autograder_pb.UpdatePasswordResponse{Success: false}, nil
	}
	if err != nil {
		l := logging.Extract(ctx)
		l.Error("UpdatePassword.UpdateUser", zap.Error(err))
		return nil, status.Error(codes.Internal, CodeInternalError)
	}
	return &autograder_pb.UpdatePasswordResponse{Success: true}, nil
}

func (a *AutograderService) GetAllUsers(
	ctx context.Context, request *autograder_pb.GetAllUsersRequest,
) (*autograder_pb.GetAllUsersResponse, error) {
	userIds, users, err := a.userRepo.GetAllUsers(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetUsers)
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
	// Guard against an admin demoting themselves and against modifying the
	// bootstrap root user (id 1). Report it explicitly instead of silently
	// returning success so callers are not misled.
	if curUser.GetUserId() == request.GetUserId() {
		return nil, status.Error(codes.InvalidArgument, CodeCannotModifySelf)
	}
	if request.GetUserId() == 1 {
		return nil, status.Error(codes.InvalidArgument, CodeCannotModifyRoot)
	}
	err := a.userRepo.UpdateUserAtomic(ctx, request.GetUserId(), func(user *model_pb.User) error {
		user.IsAdmin = request.GetIsAdmin()
		return nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, CodeUpdateUser)
	}
	return &autograder_pb.SetAdminResponse{}, nil
}
