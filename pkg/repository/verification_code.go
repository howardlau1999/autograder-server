package repository

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"

	model_pb "autograder-server/pkg/model/proto"
	"github.com/cockroachdb/pebble"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type VerificationCodeRepository interface {
	Issue(ctx context.Context, typ string, key string, code string, expireAt time.Time) error
	Validate(ctx context.Context, typ string, key string, code string) (bool, error)
	GarbageCollect()
}

type KVVerificationCodeRepository struct {
	db *pebble.DB
}

func (vr *KVVerificationCodeRepository) GarbageCollect() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		prefix := []byte("verification_code:")
		iter := vr.db.NewIter(PrefixIterOptions(prefix))
		for iter.First(); iter.Valid(); iter.Next() {
			metadata := &model_pb.VerificationCodeMetadata{}
			err := proto.Unmarshal(iter.Value(), metadata)
			if err != nil {
				zap.L().Error("VerificationCode.GC.Unmarshal", zap.ByteString("key", iter.Key()), zap.Error(err))
				continue
			}
			if time.Now().After(metadata.GetExpiredAt().AsTime()) {
				zap.L().Debug("VerificationCode.GC.Expired", zap.ByteString("key", iter.Key()))
				err = vr.db.Delete(iter.Key(), pebble.Sync)
				if err != nil {
					zap.L().Error("VerificationCode.GC.Delete", zap.ByteString("key", iter.Key()), zap.Error(err))
				}
			}
		}
	}
}

func (vr *KVVerificationCodeRepository) Issue(
	ctx context.Context, typ string, key string, code string, expireAt time.Time,
) error {
	metadata := &model_pb.VerificationCodeMetadata{
		Code: code, IssuedAt: timestamppb.Now(), ExpiredAt: timestamppb.New(expireAt),
	}
	raw, err := proto.Marshal(metadata)
	if err != nil {
		return err
	}
	return vr.db.Set(vr.getKey(typ, key), raw, pebble.Sync)
}

func (vr *KVVerificationCodeRepository) Validate(ctx context.Context, typ string, key string, code string) (
	bool, error,
) {
	k := vr.getKey(typ, key)
	raw, closer, err := vr.db.Get(k)
	if err != nil {
		return false, err
	}
	metadata := &model_pb.VerificationCodeMetadata{}
	err = proto.Unmarshal(raw, metadata)
	closer.Close()
	if err != nil {
		return false, err
	}
	now := time.Now()
	expireAt := metadata.GetExpiredAt().AsTime()
	if now.After(expireAt) {
		vr.db.Delete(k, pebble.Sync)
		return false, nil
	}
	if subtle.ConstantTimeCompare([]byte(metadata.Code), []byte(code)) == 0 {
		return false, nil
	}
	vr.db.Delete(k, pebble.Sync)
	return true, nil

}

func (vr *KVVerificationCodeRepository) getPrefix(typ string) []byte {
	return []byte(fmt.Sprintf("verification_code:%s:", typ))
}

func (vr *KVVerificationCodeRepository) getKey(typ string, key string) []byte {
	return append(vr.getPrefix(typ), []byte(key)...)
}

func NewKVVerificationCodeRepository(db *pebble.DB) VerificationCodeRepository {
	return &KVVerificationCodeRepository{db: db}
}
