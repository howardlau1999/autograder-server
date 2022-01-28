package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ManifestRepository interface {
	CreateManifest(userId, assignmentId uint64) (uint64, error)
	DeleteFileInManifest(filename string, id uint64) (uint64, error)
	AddFileToManifest(filename string, id uint64) (uint64, error)
	GetFilesInManifest(id uint64) ([]string, error)
	DeleteManifest(id uint64) error
}

type KVManifestRepository struct {
	db *pebble.DB
}

func (mr *KVManifestRepository) getMetadataKey(id uint64) []byte {
	return []byte(fmt.Sprintf("manifest:metadata:%d", id))
}

func (mr *KVManifestRepository) getNextFileIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("manifest:next_file_id:%d", id))
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
	id, err := getNextId(mr.db, []byte("manifest:next_id"))
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
	iter := mr.db.NewIter(prefixIterOptions(prefix))
	var files []string
	for iter.First(); iter.Valid(); iter.Next() {
		files = append(files, string(iter.Key()[len(prefix):]))
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return files, nil
}

func (mr *KVManifestRepository) AddFileToManifest(filename string, id uint64) (uint64, error) {
	createdTsKey := mr.getMetadataKey(id)
	_, closer, err := mr.db.Get(createdTsKey)
	if err != nil {
		return 0, err
	}
	err = closer.Close()
	if err != nil {
		return 0, err
	}
	fileKey := mr.getFileKey(id, filename)
	_, closer, err = mr.db.Get(fileKey)
	if err != nil && err != pebble.ErrNotFound {
		return 0, err
	}
	if err == nil {
		closer.Close()
	}
	err = mr.db.Set(fileKey, nil, pebble.Sync)
	if err != nil {
		return 0, nil
	}
	return 0, nil
}

func (mr *KVManifestRepository) DeleteManifest(id uint64) error {
	prefix := mr.getFilesPrefix(id)

	err := mr.db.DeleteRange(prefix, keyUpperBound(prefix), pebble.Sync)
	if err != nil {
		return err
	}
	err = mr.db.Delete(mr.getNextFileIdKey(id), pebble.Sync)
	if err != nil {
		return err
	}
	err = mr.db.Delete(mr.getMetadataKey(id), pebble.Sync)
	return err
}

func NewKVManifestRepository(db *pebble.DB) ManifestRepository {
	return &KVManifestRepository{db: db}
}
