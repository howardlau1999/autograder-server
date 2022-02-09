package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type UserRepository interface {
	CreateUser(ctx context.Context, user *model_pb.User) (uint64, error)
	UpdateUser(ctx context.Context, id uint64, user *model_pb.User) error
	GetUserByUsername(ctx context.Context, username string) (*model_pb.User, uint64, error)
	GetUserById(ctx context.Context, id uint64) (*model_pb.User, error)
	AddCourse(ctx context.Context, member *model_pb.CourseMember) error
	GetCoursesByUser(ctx context.Context, userId uint64) ([]*model_pb.CourseMember, error)
	GetCourseMember(ctx context.Context, userId uint64, courseId uint64) *model_pb.CourseMember
}

type KVUserRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func (ur *KVUserRepository) GetCourseMember(ctx context.Context, userId uint64, courseId uint64) *model_pb.CourseMember {
	key := ur.getCourseKey(userId, courseId)
	raw, closer, err := ur.db.Get(key)
	if err != nil {
		return nil
	}
	defer closer.Close()
	member := &model_pb.CourseMember{}
	err = proto.Unmarshal(raw, member)
	if err != nil {
		return nil
	}
	return member
}

func (ur *KVUserRepository) GetCoursesByUser(ctx context.Context, userId uint64) ([]*model_pb.CourseMember, error) {
	var courses []*model_pb.CourseMember
	prefix := ur.getCoursePrefix(userId)
	iter := ur.db.NewIter(PrefixIterOptions(prefix))
	for iter.First(); iter.Valid(); iter.Next() {
		course := &model_pb.CourseMember{}
		err := proto.Unmarshal(iter.Value(), course)
		if err != nil {
			return nil, err
		}
		courses = append(courses, course)
	}
	iter.Close()
	return courses, nil
}

func (ur *KVUserRepository) AddCourse(ctx context.Context, member *model_pb.CourseMember) error {
	raw, err := proto.Marshal(member)
	if err != nil {
		return err
	}
	return ur.db.Set(ur.getCourseKey(member.GetUserId(), member.GetCourseId()), raw, pebble.Sync)
}

func (ur *KVUserRepository) getUserIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("user:id:%d", id))
}

func (ur *KVUserRepository) getUserNameKey(name string) []byte {
	return []byte(fmt.Sprintf("user:name:%s", name))
}

func (ur *KVUserRepository) getCoursePrefix(id uint64) []byte {
	return append([]byte(fmt.Sprintf("user:courses:%d:", id)))
}

func (ur *KVUserRepository) getCourseKey(uid uint64, cid uint64) []byte {
	return append(ur.getCoursePrefix(uid), Uint64ToBytes(cid)...)
}

func (ur *KVUserRepository) CreateUser(ctx context.Context, user *model_pb.User) (uint64, error) {
	idBytes, closer, err := ur.db.Get(ur.getUserNameKey(user.Username))
	if err != pebble.ErrNotFound {
		if err == nil {
			closer.Close()
		}
		return binary.BigEndian.Uint64(idBytes), nil
	}
	id, err := ur.seq.GetNextId()
	if err != nil {
		return 0, err
	}
	raw, err := proto.Marshal(user)
	if err != nil {
		return 0, err
	}
	idBytes = make([]byte, 8)
	binary.BigEndian.PutUint64(idBytes, id)
	batch := ur.db.NewBatch()
	err = batch.Set(ur.getUserIdKey(id), raw, pebble.Sync)
	err = batch.Set(ur.getUserNameKey(user.Username), idBytes, pebble.Sync)
	err = batch.Commit(pebble.Sync)
	return id, nil
}

func (ur *KVUserRepository) UpdateUser(ctx context.Context, id uint64, user *model_pb.User) error {
	_, err := ur.GetUserById(ctx, id)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(user)
	if err != nil {
		return err
	}
	err = ur.db.Set(ur.getUserIdKey(id), raw, pebble.Sync)
	return err
}

func (ur *KVUserRepository) GetUserByUsername(ctx context.Context, username string) (*model_pb.User, uint64, error) {
	idBytes, closer, err := ur.db.Get(ur.getUserNameKey(username))
	if err != nil {
		return nil, 0, err
	}
	closer.Close()
	id := binary.BigEndian.Uint64(idBytes)
	user, err := ur.GetUserById(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	return user, id, nil
}

func (ur *KVUserRepository) GetUserById(ctx context.Context, id uint64) (*model_pb.User, error) {
	raw, closer, err := ur.db.Get(ur.getUserIdKey(id))
	if err != nil {
		return nil, err
	}
	closer.Close()
	user := &model_pb.User{}
	err = proto.Unmarshal(raw, user)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func NewKVUserRepository(db *pebble.DB) UserRepository {
	seq, _ := NewKVSequencer(db, []byte("user:next_id"))
	return &KVUserRepository{db: db, seq: seq}
}
