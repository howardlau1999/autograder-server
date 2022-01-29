package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"encoding/binary"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
	"sync"
)

const BatchSize = 10

type Sequencer interface {
	GetNextId() (uint64, error)
}

type KVSequencer struct {
	db    *pebble.DB
	key   []byte
	next  uint64
	limit uint64
	mu    *sync.Mutex
}

func (kvseq *KVSequencer) GetNextId() (uint64, error) {
	kvseq.mu.Lock()
	for kvseq.next >= kvseq.limit {
		if err := kvseq.requestNextBatch(); err != nil {
			kvseq.mu.Unlock()
			return 0, err
		}
	}
	id := kvseq.next
	kvseq.next++
	kvseq.mu.Unlock()
	return id, nil
}

func (kvseq *KVSequencer) requestNextBatch() error {
	if kvseq.next < kvseq.limit {
		return nil
	}
	nextLimit := kvseq.limit + BatchSize
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, nextLimit)
	err := kvseq.db.Set(kvseq.key, raw, pebble.Sync)
	if err != nil {
		return err
	}
	kvseq.limit = nextLimit
	return nil
}

func NewKVSequencer(db *pebble.DB, key []byte) (Sequencer, error) {
	sequencer := &KVSequencer{db: db, key: key, mu: &sync.Mutex{}}
	idBytes, closer, err := db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			idBytes = make([]byte, 8)
			binary.BigEndian.PutUint64(idBytes, 1)
		} else {
			return nil, err
		}
	}
	if err == nil {
		closer.Close()
	}
	id := binary.BigEndian.Uint64(idBytes)
	limit := id
	sequencer.limit = limit
	sequencer.next = id
	return sequencer, sequencer.requestNextBatch()
}

func getNextId(db *pebble.DB, key []byte) (uint64, error) {
	one := &model_pb.Mergeable{MergeableOneof: &model_pb.Mergeable_Counter{Counter: 1}}
	raw, err := proto.Marshal(one)
	if err != nil {
		return 0, err
	}
	err = db.Merge(key, raw, pebble.Sync)
	if err != nil {
		return 0, err
	}
	raw, closer, err := db.Get(key)
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	mergeable := &model_pb.Mergeable{}
	err = proto.Unmarshal(raw, mergeable)
	if err != nil {
		return 0, err
	}
	return mergeable.GetCounter(), nil
}
