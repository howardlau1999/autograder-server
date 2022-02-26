package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type LeaderboardRepository interface {
	UpdateLeaderboardEntry(ctx context.Context, assignmentId, userId uint64, entry *model_pb.LeaderboardEntry) error
	GetLeaderboardEntry(ctx context.Context, assignmentId, userId uint64) (*model_pb.LeaderboardEntry, error)
	GetLeaderboard(ctx context.Context, assignmentId uint64) ([]*model_pb.LeaderboardEntry, error)
	HasLeaderboard(ctx context.Context, assignmentId uint64) bool
	SetLeaderboardAnonymous(ctx context.Context, assignmentId uint64, anonymous bool) error
	GetLeaderboardAnonymous(ctx context.Context, assignmentId uint64) bool
}

type KVLeaderboardRepository struct {
	db *pebble.DB
}

func (lr *KVLeaderboardRepository) SetLeaderboardAnonymous(ctx context.Context, assignmentId uint64, anonymous bool) error {
	if anonymous {
		return lr.db.Set(lr.getAnonymousMarker(assignmentId), nil, pebble.Sync)
	}
	return lr.db.Delete(lr.getAnonymousMarker(assignmentId), pebble.Sync)
}

func (lr *KVLeaderboardRepository) GetLeaderboardAnonymous(ctx context.Context, assignmentId uint64) bool {
	_, closer, err := lr.db.Get(lr.getAnonymousMarker(assignmentId))
	if err != nil {
		return false
	}
	closer.Close()
	return true
}

func (lr *KVLeaderboardRepository) GetLeaderboard(ctx context.Context, assignmentId uint64) ([]*model_pb.LeaderboardEntry, error) {
	prefix := lr.getAssignmentPrefix(assignmentId)
	iter := lr.db.NewIter(PrefixIterOptions(prefix))
	var entries []*model_pb.LeaderboardEntry
	for iter.First(); iter.Valid(); iter.Next() {
		raw := iter.Value()
		entry := &model_pb.LeaderboardEntry{}
		err := proto.Unmarshal(raw, entry)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (lr *KVLeaderboardRepository) getAnonymousMarker(assignmentId uint64) []byte {
	return []byte(fmt.Sprintf("leaderboard:anonymous:%d", assignmentId))
}

func (lr *KVLeaderboardRepository) getMarkerId(assignmentId uint64) []byte {
	return []byte(fmt.Sprintf("leaderboard:marker:%d", assignmentId))
}

func (lr *KVLeaderboardRepository) getAssignmentPrefix(assignmentId uint64) []byte {
	return []byte(fmt.Sprintf("leaderboard:assignment:%d:", assignmentId))
}

func (lr *KVLeaderboardRepository) getAssignmentUserKey(assignmentId uint64, userId uint64) []byte {
	return append(lr.getAssignmentPrefix(assignmentId), Uint64ToBytes(userId)...)
}

func (lr *KVLeaderboardRepository) UpdateLeaderboardEntry(ctx context.Context, assignmentId, userId uint64, entry *model_pb.LeaderboardEntry) error {
	key := lr.getAssignmentUserKey(assignmentId, userId)
	raw, err := proto.Marshal(entry)
	if err != nil {
		return err
	}

	err = lr.db.Set(key, raw, pebble.Sync)
	if err != nil {
		return err
	}

	return lr.db.Set(lr.getMarkerId(assignmentId), nil, pebble.Sync)
}

func (lr *KVLeaderboardRepository) GetLeaderboardEntry(ctx context.Context, assignmentId, userId uint64) (*model_pb.LeaderboardEntry, error) {
	key := lr.getAssignmentUserKey(assignmentId, userId)
	raw, closer, err := lr.db.Get(key)
	if err != nil {
		return nil, err
	}
	entry := &model_pb.LeaderboardEntry{}
	err = proto.Unmarshal(raw, entry)
	closer.Close()
	if err != nil {
		return nil, err
	}

	return entry, nil
}

func (lr *KVLeaderboardRepository) HasLeaderboard(ctx context.Context, assignmentId uint64) bool {
	_, closer, err := lr.db.Get(lr.getMarkerId(assignmentId))
	if err != nil {
		return false
	}
	closer.Close()
	return true
}

func NewKVLeaderboardRepository(db *pebble.DB) LeaderboardRepository {
	return &KVLeaderboardRepository{db: db}
}
