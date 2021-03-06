package repository

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"sync"

	model_pb "autograder-server/pkg/model/proto"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

var ErrAlreadyExists = errors.New("already exists")

type GraderRepository interface {
	GetGraderByName(ctx context.Context, name string) (*model_pb.GraderStatusMetadata, uint64, error)
	GetGraderIdByName(ctx context.Context, name string) (uint64, error)
	GetGraderById(ctx context.Context, graderId uint64) (*model_pb.GraderStatusMetadata, error)
	ClaimSubmission(ctx context.Context, graderId uint64, submissionId uint64) error
	ReleaseSubmission(ctx context.Context, submissionId uint64) error
	GetGraderIdBySubmissionId(ctx context.Context, submissionId uint64) (uint64, error)
	GetGraderBySubmissionId(ctx context.Context, submissionId uint64) (*model_pb.GraderStatusMetadata, uint64, error)
	CreateGrader(ctx context.Context, name string, metadata *model_pb.GraderStatusMetadata) (uint64, error)
	UpdateGraderStatus(ctx context.Context, graderId uint64, status model_pb.GraderStatusMetadata_Status) error
	UpdateGrader(ctx context.Context, graderId uint64, grader *model_pb.GraderStatusMetadata) error
	DeleteGrader(ctx context.Context, graderId uint64) error
	GetAllGraders(ctx context.Context) ([]uint64, []*model_pb.GraderStatusMetadata, error)
	GetSubmissionsByGrader(ctx context.Context, graderId uint64) ([]uint64, error)
	PutMetadata(ctx context.Context, graderId uint64, key []byte, value []byte) error
	GetMetadata(ctx context.Context, graderId uint64, key []byte) ([]byte, error)
	DeleteMetadata(ctx context.Context, graderId uint64, key []byte) error
	GetAllMetadata(ctx context.Context, graderId uint64) ([][]byte, [][]byte, error)
	ClearRunning(ctx context.Context)
}

type KVGraderRepository struct {
	db  *pebble.DB
	seq Sequencer
	mu  map[uint64]*sync.Mutex
}

func (gr *KVGraderRepository) DeleteMetadata(ctx context.Context, graderId uint64, key []byte) error {
	return gr.db.Delete(gr.getMetadataKey(graderId, key), pebble.Sync)
}

func (gr *KVGraderRepository) PutMetadata(ctx context.Context, graderId uint64, key []byte, value []byte) error {
	return gr.db.Set(gr.getMetadataKey(graderId, key), value, pebble.Sync)
}

func (gr *KVGraderRepository) GetMetadata(ctx context.Context, graderId uint64, key []byte) ([]byte, error) {
	value, closer, err := gr.db.Get(gr.getMetadataKey(graderId, key))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return value, nil
}

func (gr *KVGraderRepository) GetAllMetadata(ctx context.Context, graderId uint64) ([][]byte, [][]byte, error) {
	prefix := gr.getMetadataPrefix(graderId)
	prefixLen := len(prefix)
	var keys [][]byte
	var values [][]byte
	iter := gr.db.NewIter(PrefixIterOptions(prefix))
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()[prefixLen:]
		value := iter.Value()
		keys = append(keys, key)
		values = append(values, value)
	}
	return keys, values, nil
}

func (gr *KVGraderRepository) getMetadataKey(graderId uint64, key []byte) []byte {
	return append(gr.getMetadataPrefix(graderId), key...)
}

func (gr *KVGraderRepository) getMetadataPrefix(graderId uint64) []byte {
	return []byte(fmt.Sprintf("grader:metadata:id:%d:", graderId))
}

func (gr *KVGraderRepository) ClearRunning(ctx context.Context) {
	prefix := []byte("running:")
	iter := gr.db.NewIter(PrefixIterOptions(prefix))
	for iter.First(); iter.Valid(); iter.Next() {
		gr.db.Delete(iter.Key(), pebble.Sync)
	}
}

func (gr *KVGraderRepository) GetSubmissionsByGrader(ctx context.Context, graderId uint64) ([]uint64, error) {
	prefix := gr.getGraderSubmissionPrefix(graderId)
	prefixLen := len(prefix)
	iter := gr.db.NewIter(PrefixIterOptions(prefix))
	var submissions []uint64
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		submissionIdStr := key[prefixLen:]
		submissionId, err := strconv.Atoi(string(submissionIdStr))
		if err != nil {
			continue
		}
		submissions = append(submissions, uint64(submissionId))
	}
	return submissions, nil
}

func (gr *KVGraderRepository) GetAllGraders(ctx context.Context) ([]uint64, []*model_pb.GraderStatusMetadata, error) {
	prefix := gr.getGraderIdPrefix()
	prefixLen := len(prefix)
	iter := gr.db.NewIter(PrefixIterOptions(prefix))
	var ids []uint64
	var graders []*model_pb.GraderStatusMetadata
	for iter.First(); iter.Valid(); iter.Next() {
		id := binary.BigEndian.Uint64(iter.Key()[prefixLen:])
		raw := iter.Value()
		grader := &model_pb.GraderStatusMetadata{}
		if err := proto.Unmarshal(raw, grader); err != nil {
			continue
		}
		ids = append(ids, id)
		graders = append(graders, grader)
	}
	return ids, graders, nil
}

func (gr *KVGraderRepository) UpdateGrader(
	ctx context.Context, graderId uint64, grader *model_pb.GraderStatusMetadata,
) error {
	raw, err := proto.Marshal(grader)
	if err != nil {
		return err
	}
	return gr.db.Set(gr.getGraderIdKey(graderId), raw, pebble.Sync)
}

func (gr *KVGraderRepository) GetGraderIdByName(ctx context.Context, name string) (uint64, error) {
	key := gr.getGraderNameKey(name)
	raw, closer, err := gr.db.Get(key)
	if err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint64(raw)
	closer.Close()
	return id, nil
}

func (gr *KVGraderRepository) getGraderNameKey(name string) []byte {
	return []byte(fmt.Sprintf("grader:name:%s", name))
}

func (gr *KVGraderRepository) getSubmissionGraderPrefix() []byte {
	return []byte("running:submission:grader:")
}

func (gr *KVGraderRepository) getGraderSubmissionPrefix(graderId uint64) []byte {
	return []byte(fmt.Sprintf("running:grader:submission:%d:", graderId))
}

func (gr *KVGraderRepository) getGraderSubmissionKey(graderId uint64, submissionId uint64) []byte {
	return []byte(fmt.Sprintf("running:grader:submission:%d:%d", graderId, submissionId))
}

func (gr *KVGraderRepository) getSubmissionGraderKey(submissionId uint64) []byte {
	return append(gr.getSubmissionGraderPrefix(), Uint64ToBytes(submissionId)...)
}

func (gr *KVGraderRepository) getGraderIdPrefix() []byte {
	return []byte("grader:id:")
}

func (gr *KVGraderRepository) getGraderIdKey(graderId uint64) []byte {
	return append(gr.getGraderIdPrefix(), Uint64ToBytes(graderId)...)
}

func (gr *KVGraderRepository) ReleaseSubmission(ctx context.Context, submissionId uint64) error {
	raw, closer, err := gr.db.Get(gr.getSubmissionGraderKey(submissionId))
	if err != nil {
		return nil
	}
	graderId := binary.BigEndian.Uint64(raw)
	closer.Close()
	gr.db.Delete(gr.getSubmissionGraderKey(submissionId), pebble.Sync)
	return gr.db.Delete(gr.getGraderSubmissionKey(graderId, submissionId), pebble.Sync)
}

func (gr *KVGraderRepository) GetGraderById(ctx context.Context, graderId uint64) (
	*model_pb.GraderStatusMetadata, error,
) {
	raw, closer, err := gr.db.Get(gr.getGraderIdKey(graderId))
	if err != nil {
		return nil, err
	}
	metadata := &model_pb.GraderStatusMetadata{}
	err = proto.Unmarshal(raw, metadata)
	closer.Close()
	if err != nil {
		return nil, err
	}
	return metadata, nil
}

func (gr *KVGraderRepository) ClaimSubmission(ctx context.Context, graderId uint64, submissionId uint64) error {
	submissionGraderKey := gr.getSubmissionGraderKey(submissionId)
	graderSubmissionKey := gr.getGraderSubmissionKey(graderId, submissionId)
	err := gr.db.Set(submissionGraderKey, Uint64ToBytes(graderId), pebble.Sync)
	if err != nil {
		return err
	}
	err = gr.db.Set(graderSubmissionKey, nil, pebble.Sync)
	return err
}

func (gr *KVGraderRepository) GetGraderIdBySubmissionId(ctx context.Context, submissionId uint64) (uint64, error) {
	raw, closer, err := gr.db.Get(gr.getSubmissionGraderKey(submissionId))
	if err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint64(raw)
	closer.Close()
	return id, nil
}

func (gr *KVGraderRepository) GetGraderBySubmissionId(
	ctx context.Context, submissionId uint64,
) (*model_pb.GraderStatusMetadata, uint64, error) {
	id, err := gr.GetGraderIdBySubmissionId(ctx, submissionId)
	if err != nil {
		return nil, 0, err
	}
	grader, err := gr.GetGraderById(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	return grader, id, nil
}

func (gr *KVGraderRepository) GetGraderByName(ctx context.Context, name string) (
	*model_pb.GraderStatusMetadata, uint64, error,
) {
	id, err := gr.GetGraderIdByName(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	grader, err := gr.GetGraderById(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	return grader, id, nil
}

func (gr *KVGraderRepository) CreateGrader(
	ctx context.Context, name string, metadata *model_pb.GraderStatusMetadata,
) (uint64, error) {
	id, err := gr.seq.GetNextId()
	if err != nil {
		return 0, err
	}
	err = gr.UpdateGrader(ctx, id, metadata)
	if err != nil {
		return 0, err
	}
	nameKey := gr.getGraderNameKey(name)
	err = gr.db.Set(nameKey, Uint64ToBytes(id), pebble.Sync)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (gr *KVGraderRepository) UpdateGraderStatus(
	ctx context.Context, graderId uint64, status model_pb.GraderStatusMetadata_Status,
) error {
	//TODO implement me
	panic("implement me")
}

func (gr *KVGraderRepository) DeleteGrader(ctx context.Context, graderId uint64) error {
	return gr.db.Delete(gr.getGraderIdKey(graderId), pebble.Sync)
}

func NewKVGraderRepository(db *pebble.DB) GraderRepository {
	seq, err := NewKVSequencer(db, []byte("grader:next_id"))
	if err != nil {
		panic(err)
	}
	return &KVGraderRepository{db: db, seq: seq}
}
