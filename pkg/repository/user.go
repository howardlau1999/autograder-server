package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	model_pb "autograder-server/pkg/model/proto"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type UserRepository interface {
	CreateUser(ctx context.Context, user *model_pb.User) (uint64, error)
	UpdateUser(ctx context.Context, id uint64, user *model_pb.User) error
	GetUserIdByUsername(ctx context.Context, username string) (uint64, error)
	GetUserByUsername(ctx context.Context, username string) (*model_pb.User, uint64, error)
	GetUserById(ctx context.Context, id uint64) (*model_pb.User, error)
	AddCourse(ctx context.Context, member *model_pb.CourseMember) error
	GetCoursesByUser(ctx context.Context, userId uint64) ([]*model_pb.CourseMember, error)
	GetCourseMember(ctx context.Context, userId uint64, courseId uint64) *model_pb.CourseMember
	RemoveCourseMember(ctx context.Context, userId uint64, courseId uint64) error
	GetUserByEmail(ctx context.Context, email string) (*model_pb.User, uint64, error)
	GetUserIdByEmail(ctx context.Context, email string) (uint64, error)
	BindGithubId(ctx context.Context, userId uint64, githubId string) error
	UnbindGithubId(ctx context.Context, userId uint64) error
	GetUserIdByGithubId(ctx context.Context, githubId string) (uint64, error)
	GetUserByGithubId(ctx context.Context, githubId string) (*model_pb.User, uint64, error)
	GetAllUsers(ctx context.Context) ([]uint64, []*model_pb.User, error)
}

type KVUserRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func (ur *KVUserRepository) GetAllUsers(ctx context.Context) ([]uint64, []*model_pb.User, error) {
	prefix := []byte("user:id:")
	prefixLen := len(prefix)
	var ids []uint64
	var users []*model_pb.User
	iter := ur.db.NewIter(PrefixIterOptions(prefix))
	for iter.First(); iter.Valid(); iter.Next() {
		idStr := iter.Key()[prefixLen:]
		id, _ := strconv.Atoi(string(idStr))
		user, err := ur.GetUserById(ctx, uint64(id))
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, uint64(id))
		users = append(users, user)
	}
	return ids, users, nil
}

func (ur *KVUserRepository) GetUserIdByUsername(ctx context.Context, username string) (uint64, error) {
	idBytes, closer, err := ur.db.Get(ur.getUsernameKey(username))
	if err != nil {
		return 0, err
	}
	closer.Close()
	id := binary.BigEndian.Uint64(idBytes)
	return id, nil
}

func (ur *KVUserRepository) RemoveCourseMember(ctx context.Context, userId uint64, courseId uint64) error {
	return ur.db.Delete(ur.getCourseKey(userId, courseId), pebble.Sync)
}

func (ur *KVUserRepository) GetCourseMember(
	ctx context.Context, userId uint64, courseId uint64,
) *model_pb.CourseMember {
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

func (ur *KVUserRepository) getUserIdKey(userId uint64) []byte {
	return []byte(fmt.Sprintf("user:id:%d", userId))
}

func (ur *KVUserRepository) getUsernameKey(name string) []byte {
	return []byte(fmt.Sprintf("user:name:%s", name))
}

func (ur *KVUserRepository) getGithubIdKey(githubId string) []byte {
	return []byte(fmt.Sprintf("user:github:%s", githubId))
}

func (ur *KVUserRepository) getEmailKey(email string) []byte {
	return []byte(fmt.Sprintf("user:email:%s", email))
}

func (ur *KVUserRepository) getCoursePrefix(courseId uint64) []byte {
	return append([]byte(fmt.Sprintf("user:courses:%d:", courseId)))
}

func (ur *KVUserRepository) getCourseKey(userId uint64, courseId uint64) []byte {
	return append(ur.getCoursePrefix(userId), Uint64ToBytes(courseId)...)
}

func (ur *KVUserRepository) GetUserIdByEmail(ctx context.Context, email string) (uint64, error) {
	idBytes, closer, err := ur.db.Get(ur.getEmailKey(email))
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	return binary.BigEndian.Uint64(idBytes), nil
}

func (ur *KVUserRepository) GetUserIdByGithubId(ctx context.Context, githubId string) (uint64, error) {
	idBytes, closer, err := ur.db.Get(ur.getGithubIdKey(githubId))
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	return binary.BigEndian.Uint64(idBytes), nil
}

func (ur *KVUserRepository) GetUserByGithubId(ctx context.Context, githubId string) (*model_pb.User, uint64, error) {
	userId, err := ur.GetUserIdByGithubId(ctx, githubId)
	if err != nil {
		return nil, 0, err
	}
	user, err := ur.GetUserById(ctx, userId)
	if err != nil {
		return nil, 0, err
	}
	return user, userId, nil
}

func (ur *KVUserRepository) GetUserByEmail(ctx context.Context, email string) (*model_pb.User, uint64, error) {
	userId, err := ur.GetUserIdByEmail(ctx, email)
	if err != nil {
		return nil, 0, err
	}
	user, err := ur.GetUserById(ctx, userId)
	return user, userId, err
}

func (ur *KVUserRepository) UnbindGithubId(ctx context.Context, userId uint64) error {
	user, err := ur.GetUserById(ctx, userId)
	if err != nil {
		return err
	}
	githubId := user.GetGithubId()
	user.GithubId = ""
	err = ur.UpdateUser(ctx, userId, user)
	if err != nil {
		return err
	}
	if len(githubId) > 0 {
		return ur.db.Delete(ur.getGithubIdKey(githubId), pebble.Sync)
	}
	return nil
}

func (ur *KVUserRepository) BindGithubId(ctx context.Context, userId uint64, githubId string) error {
	user, err := ur.GetUserById(ctx, userId)
	if err != nil {
		return err
	}
	user.GithubId = githubId
	err = ur.UpdateUser(ctx, userId, user)
	if err != nil {
		return err
	}
	return ur.db.Set(ur.getGithubIdKey(githubId), Uint64ToBytes(userId), pebble.Sync)
}

func (ur *KVUserRepository) CreateUser(ctx context.Context, user *model_pb.User) (uint64, error) {
	user.CreatedAt = timestamppb.Now()
	idBytes, closer, err := ur.db.Get(ur.getUsernameKey(user.Username))
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
	batch := ur.db.NewBatch()
	err = batch.Set(ur.getUserIdKey(id), raw, pebble.Sync)
	err = batch.Set(ur.getUsernameKey(user.Username), Uint64ToBytes(id), pebble.Sync)
	err = batch.Set(ur.getEmailKey(user.Email), Uint64ToBytes(id), pebble.Sync)
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
	id, err := ur.GetUserIdByUsername(ctx, username)
	if err != nil {
		return nil, 0, err
	}
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
