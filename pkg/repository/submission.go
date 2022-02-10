package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type SubmissionRepository interface {
	CreateSubmission(ctx context.Context, submission *model_pb.Submission) (uint64, error)
	GetSubmission(ctx context.Context, id uint64) (*model_pb.Submission, error)
	GetSubmissionsByUserAndAssignment(ctx context.Context, userId, assignmentId uint64) ([]uint64, error)
	DeleteSubmissionsByUserAndAssignment(ctx context.Context, userId, assignmentId uint64) error
}

type KVSubmissionRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func (sr *KVSubmissionRepository) DeleteSubmissionsByUserAndAssignment(ctx context.Context, userId, assignmentId uint64) error {
	prefix := sr.getUserAssignmentPrefix(userId, assignmentId)
	upper := KeyUpperBound(prefix)
	return sr.db.DeleteRange(prefix, upper, pebble.Sync)
}

func (sr *KVSubmissionRepository) GetSubmissionsByUserAndAssignment(ctx context.Context, userId, assignmentId uint64) ([]uint64, error) {
	prefix := sr.getUserAssignmentPrefix(userId, assignmentId)
	prefixLen := len(prefix)
	var submissions []uint64
	iter := sr.db.NewIter(PrefixIterOptions(prefix))
	for iter.First(); iter.Valid(); iter.Next() {
		submissions = append(submissions, binary.BigEndian.Uint64(iter.Key()[prefixLen:]))
	}
	iter.Close()
	return submissions, nil
}

func (sr *KVSubmissionRepository) getIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("submission:id:%d", id))
}

func (sr *KVSubmissionRepository) getUserAssignmentPrefix(userId, assignmentId uint64) []byte {
	return []byte(fmt.Sprintf("submission:user:assignment:%d:%d:", userId, assignmentId))
}

func (sr *KVSubmissionRepository) getUserAssignmentKey(userId, assignmentId, submissionId uint64) []byte {
	return append(sr.getUserAssignmentPrefix(userId, assignmentId), Uint64ToBytes(submissionId)...)
}

func (sr *KVSubmissionRepository) CreateSubmission(ctx context.Context, submission *model_pb.Submission) (uint64, error) {
	raw, err := proto.Marshal(submission)
	if err != nil {
		return 0, err
	}
	id, err := sr.seq.GetNextId()
	if err != nil {
		return 0, err
	}
	batch := sr.db.NewBatch()
	err = batch.Set(sr.getIdKey(id), raw, pebble.Sync)
	if err != nil {
		return 0, err
	}
	for _, userId := range submission.Submitters {
		err = batch.Set(sr.getUserAssignmentKey(userId, submission.GetAssignmentId(), id), nil, pebble.Sync)
		if err != nil {
			return 0, err
		}
	}
	return id, batch.Commit(pebble.Sync)
}

func (sr *KVSubmissionRepository) GetSubmission(ctx context.Context, id uint64) (*model_pb.Submission, error) {
	raw, closer, err := sr.db.Get(sr.getIdKey(id))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	submission := &model_pb.Submission{}
	err = proto.Unmarshal(raw, submission)
	if err != nil {
		return nil, err
	}
	return submission, nil
}

func NewKVSubmissionRepository(db *pebble.DB) SubmissionRepository {
	seq, err := NewKVSequencer(db, []byte("submission:next_id"))
	if err != nil {
		panic(err)
	}
	return &KVSubmissionRepository{db: db, seq: seq}
}
