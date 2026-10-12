package grpc

import (
	"context"

	autograder_pb "autograder-server/pkg/api/proto"
	"autograder-server/pkg/logging"

	"github.com/google/go-github/v86/github"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (a *AutograderService) getGithubEmails(ctx context.Context, token *oauth2.Token) ([]*github.UserEmail, error) {
	ghClient := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token)))
	emails, _, err := ghClient.Users.ListEmails(ctx, nil)
	if err != nil || len(emails) == 0 {
		return nil, status.Error(codes.Internal, CodeGetGithubEmails)
	}
	return emails, nil
}

func (a *AutograderService) getGithubUser(ctx context.Context, code string) (*github.User, *oauth2.Token, error) {
	token, err := a.githubOAuth2Config.Exchange(ctx, code)
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, CodeInvalidCode)
	}
	ghClient := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(token)))
	ghUser, _, err := ghClient.Users.Get(ctx, "")
	if err != nil {
		return nil, nil, status.Error(codes.Internal, CodeGetGithubUser)
	}
	return ghUser, token, nil
}

func (a *AutograderService) GithubLogin(
	ctx context.Context, request *autograder_pb.GithubLoginRequest,
) (*autograder_pb.GithubLoginResponse, error) {
	l := logging.Extract(ctx)
	ghUser, token, err := a.getGithubUser(ctx, request.Code)
	if err != nil {
		return nil, err
	}

	login := ghUser.GetLogin()
	l.Debug("GithubLogin", zap.String("githubId", login))

	user, userId, _ := a.userRepo.GetUserByGithubId(ctx, login)
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
	// Only trust verified emails for account matching, preferring the primary
	// one. An unverified GitHub email must never be used to match or bind an
	// existing account.
	email := ""
	for _, e := range emails {
		if !e.GetVerified() {
			continue
		}
		if email == "" {
			email = e.GetEmail()
		}
		if e.GetPrimary() {
			email = e.GetEmail()
			break
		}
	}
	if email == "" {
		return nil, status.Error(codes.FailedPrecondition, CodeNoVerifiedEmail)
	}

	user, userId, _ = a.userRepo.GetUserByEmail(ctx, email)
	if user != nil {
		oldGithubId := user.GetGithubId()
		if oldGithubId != "" && oldGithubId != login {
			return nil, status.Error(codes.AlreadyExists, CodeEmailDifferent)
		}
		if err := a.userRepo.BindGithubId(ctx, userId, login); err != nil {
			return nil, status.Error(codes.Internal, CodeBindGithubId)
		}
		if err := a.signLoginToken(ctx, userId, user.Username, user.Nickname, user.IsAdmin); err != nil {
			return nil, err
		}
		return &autograder_pb.GithubLoginResponse{UserId: userId}, nil
	}

	// Deliberately no match on username == GitHub login: anyone can register a
	// GitHub account under a free login name, so binding (and logging in to)
	// a local account on that basis would let them take it over. A user whose
	// GitHub login collides with an existing local username gets AlreadyExists
	// from signUpNewUser below and has to bind GitHub from their account page.
	userId, err = a.signUpNewUser(ctx, email, login, "")
	if err != nil {
		return nil, err
	}
	if err := a.userRepo.BindGithubId(ctx, userId, login); err != nil {
		return nil, status.Error(codes.Internal, CodeBindGithubId)
	}
	if err := a.signLoginToken(ctx, userId, login, login, false); err != nil {
		return nil, err
	}
	return &autograder_pb.GithubLoginResponse{UserId: userId}, nil
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
		return nil, status.Error(codes.InvalidArgument, CodeInvalidGithubLogin)
	}
	bindUserId, _ := a.userRepo.GetUserIdByGithubId(ctx, login)
	if bindUserId != 0 {
		return nil, status.Error(codes.AlreadyExists, CodeAlreadyInUse)
	}
	err = a.userRepo.BindGithubId(ctx, user.GetUserId(), login)
	if err != nil {
		return &autograder_pb.BindGithubResponse{Success: false}, nil
	}
	return &autograder_pb.BindGithubResponse{Success: true}, nil
}
