package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type CourseRepository interface {
	CreateCourse(ctx context.Context, course *model_pb.Course) (uint64, error)
	GetCourse(ctx context.Context, id uint64) (*model_pb.Course, error)
	AddUser(ctx context.Context, member *model_pb.CourseMember) error
	RemoveUser(ctx context.Context, courseId uint64, userId uint64) error
	AddAssignment(ctx context.Context, courseId uint64, assignmentId uint64) error
	GetUsersByCourse(ctx context.Context, courseId uint64) ([]*model_pb.CourseMember, error)
	GetAssignmentsByCourse(ctx context.Context, courseId uint64) ([]uint64, error)
	UpdateCourse(ctx context.Context, courseId uint64, course *model_pb.Course) error
}

type KVCourseRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func (cr *KVCourseRepository) RemoveUser(ctx context.Context, courseId uint64, userId uint64) error {
	return cr.db.Delete(cr.getUserKey(courseId, userId), pebble.Sync)
}

func (cr *KVCourseRepository) AddAssignment(ctx context.Context, courseId uint64, assignmentId uint64) error {
	return cr.db.Set(cr.getAssignmentKey(courseId, assignmentId), nil, pebble.Sync)
}

func (cr *KVCourseRepository) GetAssignmentsByCourse(ctx context.Context, courseId uint64) ([]uint64, error) {
	prefix := cr.getAssignmentPrefix(courseId)
	assignments := ScanIds(cr.db, prefix)
	return assignments, nil
}

func (cr *KVCourseRepository) GetUsersByCourse(ctx context.Context, courseId uint64) ([]*model_pb.CourseMember, error) {
	prefix := cr.getUserPrefix(courseId)
	iter := cr.db.NewIter(PrefixIterOptions(prefix))
	var members []*model_pb.CourseMember
	for iter.First(); iter.Valid(); iter.Next() {
		member := &model_pb.CourseMember{}
		err := proto.Unmarshal(iter.Value(), member)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	iter.Close()
	return members, nil
}

func (cr *KVCourseRepository) AddUser(ctx context.Context, member *model_pb.CourseMember) error {
	raw, err := proto.Marshal(member)
	if err != nil {
		return err
	}
	return cr.db.Set(cr.getUserKey(member.GetCourseId(), member.GetUserId()), raw, pebble.Sync)
}

func (cr *KVCourseRepository) getIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("course:id:%d", id))
}

func (cr *KVCourseRepository) getUserPrefix(courseId uint64) []byte {
	return []byte(fmt.Sprintf("course:users:%d:", courseId))
}

func (cr *KVCourseRepository) getUserKey(courseId uint64, userId uint64) []byte {
	return append(cr.getUserPrefix(courseId), Uint64ToBytes(userId)...)
}

func (cr *KVCourseRepository) getAssignmentPrefix(courseId uint64) []byte {
	return []byte(fmt.Sprintf("course:assignments:%d:", courseId))
}

func (cr *KVCourseRepository) getAssignmentKey(courseId uint64, assignmentId uint64) []byte {
	return append(cr.getAssignmentPrefix(courseId), Uint64ToBytes(assignmentId)...)
}

func (cr *KVCourseRepository) UpdateCourse(ctx context.Context, courseId uint64, course *model_pb.Course) error {
	raw, err := proto.Marshal(course)
	if err != nil {
		return err
	}
	return cr.db.Set(cr.getIdKey(courseId), raw, pebble.Sync)
}

func (cr *KVCourseRepository) CreateCourse(ctx context.Context, course *model_pb.Course) (uint64, error) {
	id, err := cr.seq.GetNextId()
	if err != nil {
		return 0, err
	}
	err = cr.UpdateCourse(ctx, id, course)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (cr *KVCourseRepository) GetCourse(ctx context.Context, id uint64) (*model_pb.Course, error) {
	raw, closer, err := cr.db.Get(cr.getIdKey(id))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	course := &model_pb.Course{}
	err = proto.Unmarshal(raw, course)
	if err != nil {
		return nil, err
	}
	return course, nil
}

func NewKVCourseRepository(db *pebble.DB) CourseRepository {
	seq, err := NewKVSequencer(db, []byte("course:next_id"))
	if err != nil {
		panic(err)
	}
	return &KVCourseRepository{db: db, seq: seq}
}
