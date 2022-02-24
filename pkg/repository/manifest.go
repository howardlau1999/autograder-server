package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

type ManifestRepository interface {
	CreateManifest(userId, assignmentId uint64) (uint64, error)
	DeleteFileInManifest(filename string, id uint64) (uint64, error)
	AddFileToManifest(filename string, id uint64) (uint64, error)
	GetFilesInManifest(id uint64) ([]string, error)
	DeleteManifest(id uint64) error
}

type KVManifestRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func (mr *KVManifestRepository) gc(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {

		prefix := mr.getMetadataPrefix()
		prefixLen := len(prefix)
		iter := mr.db.NewIter(PrefixIterOptions(prefix))
		for iter.First(); iter.Valid(); iter.Next() {
			id := binary.BigEndian.Uint64(iter.Key()[prefixLen:])
			metadata, err := mr.GetManifestMetadata(id)
			if err != nil {
				continue
			}
			now := time.Now()
			if now.After(metadata.CreatedAt.AsTime().Add(10 * time.Minute)) {
				mr.DeleteManifest(id)
			}
		}
	}
	ticker.Stop()
}

func (mr *KVManifestRepository) getMetadataPrefix() []byte {
	return []byte("manifest:metadata:")
}

func (mr *KVManifestRepository) getMetadataKey(id uint64) []byte {
	return append(mr.getMetadataPrefix(), Uint64ToBytes(id)...)
}

func (mr *KVManifestRepository) getFileKey(id uint64, filename string) []byte {
	return []byte(fmt.Sprintf("manifest:files:%d:%s", id, filename))
}

func (mr *KVManifestRepository) getFilesPrefix(id uint64) []byte {
	return []byte(fmt.Sprintf("manifest:files:%d:", id))
}

func (mr *KVManifestRepository) CreateManifest(userId, assignmentId uint64) (uint64, error) {
	metadata := &model_pb.ManifestMetadata{
		CreatedAt:    timestamppb.Now(),
		UserId:       userId,
		AssignmentId: assignmentId,
	}
	raw, err := proto.Marshal(metadata)
	if err != nil {
		return 0, err
	}
	id, err := mr.seq.GetNextId()
	if err != nil {
		return 0, err
	}
	err = mr.db.Set(mr.getMetadataKey(id), raw, pebble.Sync)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (mr *KVManifestRepository) DeleteFileInManifest(filename string, id uint64) (uint64, error) {
	err := mr.db.Delete(mr.getFileKey(id, filename), pebble.Sync)
	return 0, err
}

func (mr *KVManifestRepository) GetFilesInManifest(id uint64) ([]string, error) {
	prefix := mr.getFilesPrefix(id)
	iter := mr.db.NewIter(PrefixIterOptions(prefix))
	var files []string
	for iter.First(); iter.Valid(); iter.Next() {
		files = append(files, string(iter.Key()[len(prefix):]))
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return files, nil
}

func (mr *KVManifestRepository) GetManifestMetadata(id uint64) (*model_pb.ManifestMetadata, error) {
	metadataKey := mr.getMetadataKey(id)
	raw, closer, err := mr.db.Get(metadataKey)
	if err != nil {
		return nil, err
	}
	err = closer.Close()
	if err != nil {
		return nil, err
	}
	metadata := &model_pb.ManifestMetadata{}
	err = proto.Unmarshal(raw, metadata)
	if err != nil {
		return nil, err
	}
	return metadata, nil
}

func (mr *KVManifestRepository) AddFileToManifest(filename string, id uint64) (uint64, error) {
	fileKey := mr.getFileKey(id, filename)
	err := mr.db.Set(fileKey, Uint64ToBytes(id), pebble.Sync)
	if err != nil {
		return 0, nil
	}
	return 0, nil
}

func (mr *KVManifestRepository) DeleteManifest(id uint64) error {
	prefix := mr.getFilesPrefix(id)

	err := mr.db.DeleteRange(prefix, KeyUpperBound(prefix), pebble.Sync)
	if err != nil {
		return err
	}
	err = mr.db.Delete(mr.getMetadataKey(id), pebble.Sync)
	return err
}

func NewKVManifestRepository(db *pebble.DB) ManifestRepository {
	seq, _ := NewKVSequencer(db, []byte("manifest:next_id"))
	return &KVManifestRepository{db: db, seq: seq}
}
