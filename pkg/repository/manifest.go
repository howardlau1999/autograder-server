package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	model_pb "autograder-server/pkg/model/proto"
	"github.com/cockroachdb/pebble"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ManifestRepository interface {
	CreateManifest(ctx context.Context, userId, assignmentId uint64) (uint64, error)
	DeleteFileInManifest(ctx context.Context, filename string, id uint64) (uint64, error)
	AddFileToManifest(ctx context.Context, filename string, id uint64) (uint64, error)
	GetFilesInManifest(ctx context.Context, id uint64) ([]string, error)
	DeleteManifest(ctx context.Context, id uint64) error
	GarbageCollect(ctx context.Context, expired chan uint64)
	GetManifest(ctx context.Context, manifestId uint64) (*model_pb.ManifestMetadata, error)
}

type KVManifestRepository struct {
	db  *pebble.DB
	seq Sequencer
}

const ManifestExpireTimeout = 10 * time.Minute

func (mr *KVManifestRepository) GetManifest(ctx context.Context, manifestId uint64) (
	*model_pb.ManifestMetadata, error,
) {
	raw, closer, err := mr.db.Get(mr.getMetadataKey(manifestId))
	if err != nil {
		return nil, err
	}
	manifest := &model_pb.ManifestMetadata{}
	err = proto.Unmarshal(raw, manifest)
	closer.Close()
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (mr *KVManifestRepository) GarbageCollect(ctx context.Context, expired chan uint64) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		prefix := mr.getMetadataPrefix()
		prefixLen := len(prefix)
		iter := mr.db.NewIter(PrefixIterOptions(prefix))
		for iter.First(); iter.Valid(); iter.Next() {
			manifestId := binary.BigEndian.Uint64(iter.Key()[prefixLen:])
			logger := zap.L().With(zap.Uint64("manifestId", manifestId))
			metadata, err := mr.GetManifestMetadata(ctx, manifestId)
			if err != nil {
				logger.Error("Manifest.GetMetadata", zap.Error(err))
				continue
			}
			now := time.Now()
			if now.After(metadata.CreatedAt.AsTime().Add(ManifestExpireTimeout)) {
				logger.Debug("Manifest.Expired", zap.Time("createdAt", metadata.CreatedAt.AsTime()))
				err = mr.DeleteManifest(ctx, manifestId)
				if err != nil {
					logger.Error("Manifest.Expired.Delete", zap.Error(err))
				}
				expired <- manifestId
			}
		}
	}
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

func (mr *KVManifestRepository) CreateManifest(ctx context.Context, userId, assignmentId uint64) (uint64, error) {
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

func (mr *KVManifestRepository) DeleteFileInManifest(ctx context.Context, filename string, id uint64) (uint64, error) {
	err := mr.db.Delete(mr.getFileKey(id, filename), pebble.Sync)
	return 0, err
}

func (mr *KVManifestRepository) GetFilesInManifest(ctx context.Context, id uint64) ([]string, error) {
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

func (mr *KVManifestRepository) GetManifestMetadata(ctx context.Context, id uint64) (
	*model_pb.ManifestMetadata, error,
) {
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

func (mr *KVManifestRepository) AddFileToManifest(ctx context.Context, filename string, id uint64) (uint64, error) {
	fileKey := mr.getFileKey(id, filename)
	err := mr.db.Set(fileKey, Uint64ToBytes(id), pebble.Sync)
	if err != nil {
		return 0, nil
	}
	return 0, nil
}

func (mr *KVManifestRepository) DeleteManifest(ctx context.Context, id uint64) error {
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
