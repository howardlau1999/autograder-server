package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

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
