package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type AssignmentRepository interface {
	CreateAssignment(ctx context.Context, assignment *model_pb.Assignment) (uint64, error)
	GetAssignment(ctx context.Context, id uint64) (*model_pb.Assignment, error)
	UpdateAssignment(ctx context.Context, id uint64, assignment *model_pb.Assignment) error
}

type KVAssignmentRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func (ar *KVAssignmentRepository) UpdateAssignment(ctx context.Context, id uint64, assignment *model_pb.Assignment) error {
	raw, err := proto.Marshal(assignment)
	if err != nil {
		return err
	}
	err = ar.db.Set(ar.getIdKey(id), raw, pebble.Sync)
	if err != nil {
		return err
	}
	return nil
}

func (ar *KVAssignmentRepository) getIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("assignment:id:%d", id))
}

func (ar *KVAssignmentRepository) CreateAssignment(ctx context.Context, assignment *model_pb.Assignment) (uint64, error) {
	id, err := ar.seq.GetNextId()
	if err != nil {
		return 0, err
	}
	err = ar.UpdateAssignment(ctx, id, assignment)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (ar *KVAssignmentRepository) GetAssignment(ctx context.Context, id uint64) (*model_pb.Assignment, error) {
	raw, closer, err := ar.db.Get(ar.getIdKey(id))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	assignment := &model_pb.Assignment{}
	err = proto.Unmarshal(raw, assignment)
	if err != nil {
		return nil, err
	}
	return assignment, nil
}

func NewKVAssignmentRepository(db *pebble.DB) AssignmentRepository {
	seq, err := NewKVSequencer(db, []byte("assignment:next_id"))
	if err != nil {
		panic(err)
	}
	return &KVAssignmentRepository{db: db, seq: seq}
}
