package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
	"io"
)

type KVMerge struct {
	buffer *model_pb.Mergeable
}

func (k *KVMerge) MergeNewer(value []byte) error {
	mergeable := &model_pb.Mergeable{}
	if err := proto.Unmarshal(value, mergeable); err != nil {
		return err
	}
	switch x := k.buffer.MergeableOneof.(type) {
	case *model_pb.Mergeable_Strings:
		x.Strings.List = append(x.Strings.List, mergeable.GetStrings().List...)
	case *model_pb.Mergeable_Counter:
		x.Counter += mergeable.GetCounter()
	case *model_pb.Mergeable_Ids:
		x.Ids.List = append(x.Ids.List, mergeable.GetIds().List...)
	case nil:
		k.buffer = mergeable
	}
	return nil
}

func (k *KVMerge) MergeOlder(value []byte) error {
	mergeable := &model_pb.Mergeable{}
	if err := proto.Unmarshal(value, mergeable); err != nil {
		return err
	}
	switch x := k.buffer.MergeableOneof.(type) {
	case *model_pb.Mergeable_Strings:
		x.Strings.List = append(mergeable.GetStrings().List, x.Strings.List...)
	case *model_pb.Mergeable_Counter:
		x.Counter += mergeable.GetCounter()
	case *model_pb.Mergeable_Ids:
		x.Ids.List = append(mergeable.GetIds().List, x.Ids.List...)
	case nil:
		k.buffer = mergeable
	}
	return nil
}

func (k *KVMerge) Finish(includesBase bool) ([]byte, io.Closer, error) {
	raw, err := proto.Marshal(k.buffer)
	if err != nil {
		return nil, nil, err
	}
	return raw, io.NopCloser(nil), nil
}

func NewKVMerge(key, value []byte) (pebble.ValueMerger, error) {
	valueMgr := &KVMerge{buffer: &model_pb.Mergeable{}}
	if len(value) > 0 {
		if err := proto.Unmarshal(value, valueMgr.buffer); err != nil {
			return nil, err
		}
	}
	return valueMgr, nil
}

func NewKVMerger() *pebble.Merger {
	return &pebble.Merger{
		Name:  "autograder-kv-merger",
		Merge: NewKVMerge,
	}
}
