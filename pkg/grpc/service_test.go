package grpc

import (
	"context"
	"testing"

	autograder_pb "autograder-server/pkg/api/proto"
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
)

type fakeMailer struct{}

func (fakeMailer) SendMail(from string, to []string, subject, content string) error { return nil }

// newTestService assembles an AutograderService over a throwaway on-disk store
// with the real repositories, exercising the dependency-injection constructor.
// It is what makes the service layer unit-testable: before newAutograderService
// existed, the constructor built everything from a *pebble.DB and started
// background goroutines, so no test could stand one up in isolation.
func newTestService(t *testing.T) *AutograderService {
	t.Helper()
	db, err := repository.OpenDB(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manifestRepo, err := repository.NewKVManifestRepository(db)
	if err != nil {
		t.Fatalf("manifest repo: %v", err)
	}
	userRepo, err := repository.NewKVUserRepository(db)
	if err != nil {
		t.Fatalf("user repo: %v", err)
	}
	submissionRepo, err := repository.NewKVSubmissionRepository(db)
	if err != nil {
		t.Fatalf("submission repo: %v", err)
	}
	courseRepo, err := repository.NewKVCourseRepository(db)
	if err != nil {
		t.Fatalf("course repo: %v", err)
	}
	assignmentRepo, err := repository.NewKVAssignmentRepository(db)
	if err != nil {
		t.Fatalf("assignment repo: %v", err)
	}
	return newAutograderService(autograderServiceDeps{
		manifestRepo:         manifestRepo,
		userRepo:             userRepo,
		submissionRepo:       submissionRepo,
		submissionReportRepo: repository.NewKVSubmissionReportRepository(db),
		courseRepo:           courseRepo,
		assignmentRepo:       assignmentRepo,
		leaderboardRepo:      repository.NewKVLeaderboardRepository(db),
		verificationCodeRepo: repository.NewKVVerificationCodeRepository(db),
		mailer:               fakeMailer{},
		userJWTSignKey:       []byte("test-session"),
		uploadJWTSignKey:     []byte("test-upload"),
		downloadJWTSignKey:   []byte("test-download"),
	})
}

// addMember makes userId a member of courseId with the given role, mirroring
// what CreateCourse/JoinCourse persist (membership lives on the user side,
// which is what GetCourseMember reads).
func addMember(t *testing.T, a *AutograderService, userId, courseId uint64, role model_pb.CourseRole) {
	t.Helper()
	member := &model_pb.CourseMember{UserId: userId, CourseId: courseId, Role: role}
	if err := a.userRepo.AddCourse(context.Background(), member); err != nil {
		t.Fatalf("add course member: %v", err)
	}
}

func TestCreateCourseMakesCallerInstructor(t *testing.T) {
	a := newTestService(t)
	ctx := context.WithValue(
		context.Background(), userInfoCtxKey{}, &autograder_pb.UserTokenPayload{UserId: 7},
	)
	resp, err := a.CreateCourse(ctx, &autograder_pb.CreateCourseRequest{
		Name: "Compilers", ShortName: "CS131", Description: "build a compiler",
	})
	if err != nil {
		t.Fatalf("CreateCourse: %v", err)
	}
	member := a.userRepo.GetCourseMember(context.Background(), 7, resp.GetCourseId())
	if member == nil {
		t.Fatal("creator was not enrolled in the course")
	}
	if member.GetRole() != model_pb.CourseRole_Instructor {
		t.Errorf("creator role = %v, want Instructor", member.GetRole())
	}
}

func TestValidateSubmitters(t *testing.T) {
	a := newTestService(t)
	const courseId = 1
	addMember(t, a, 10, courseId, model_pb.CourseRole_Student)
	addMember(t, a, 11, courseId, model_pb.CourseRole_Student)

	t.Run("empty defaults to caller", func(t *testing.T) {
		got, err := a.validateSubmitters(context.Background(), nil, 10, courseId, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0] != 10 {
			t.Errorf("got %v, want [10]", got)
		}
	})

	t.Run("dedups and keeps members", func(t *testing.T) {
		got, err := a.validateSubmitters(context.Background(), []uint64{10, 11, 10}, 10, courseId, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("got %v, want 2 distinct submitters", got)
		}
	})

	t.Run("rejects non-member", func(t *testing.T) {
		if _, err := a.validateSubmitters(context.Background(), []uint64{10, 99}, 10, courseId, false); err == nil {
			t.Error("expected error for a submitter who is not a course member")
		}
	})

	t.Run("non-staff caller must be a submitter", func(t *testing.T) {
		if _, err := a.validateSubmitters(context.Background(), []uint64{11}, 10, courseId, false); err == nil {
			t.Error("expected error when caller excludes themselves and is not staff")
		}
	})

	t.Run("staff may submit on behalf of others", func(t *testing.T) {
		if _, err := a.validateSubmitters(context.Background(), []uint64{11}, 10, courseId, true); err != nil {
			t.Errorf("staff submitting for another member should be allowed, got %v", err)
		}
	})
}
