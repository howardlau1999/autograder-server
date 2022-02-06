package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"fmt"
	"github.com/cockroachdb/pebble"
)

type LeaderboardRepository interface {
	UpdateLeaderboardEntry(ctx context.Context, assignmentId, userId uint64, entry *model_pb.LeaderboardEntry) error
	GetLeaderboard(ctx context.Context, assignmentId uint64) ([]*model_pb.LeaderboardEntry, error)
}

type KVLeaderboardRepository struct {
	db *pebble.DB
}

func (lr *KVLeaderboardRepository) GetLeaderboard(ctx context.Context, assignmentId uint64) ([]*model_pb.LeaderboardEntry, error) {
	//TODO implement me
	panic("implement me")
}

func (lr *KVLeaderboardRepository) getAssignmentPrefix(assignmentId uint64) []byte {
	return []byte(fmt.Sprintf("leaderboard:%d:", assignmentId))
}

func (lr *KVLeaderboardRepository) getAssignmentUserKey(assignmentId uint64, userId uint64) []byte {
	return append(lr.getAssignmentPrefix(assignmentId), Uint64ToBytes(userId)...)
}

func (lr *KVLeaderboardRepository) UpdateLeaderboardEntry(ctx context.Context, assignmentId, userId uint64, entry *model_pb.LeaderboardEntry) error {
	//TODO implement me
	panic("implement me")
}

func NewKVLeaderboardRepository(db *pebble.DB) LeaderboardRepository {
	return &KVLeaderboardRepository{db: db}
}
