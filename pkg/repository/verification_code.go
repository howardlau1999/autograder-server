package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"crypto/subtle"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

type VerificationCodeRepository interface {
	Issue(ctx context.Context, typ string, key string, code string, expireAt time.Time) error
	Validate(ctx context.Context, typ string, key string, code string) (bool, error)
}

type KVVerificationCodeRepository struct {
	db *pebble.DB
}

func (vr *KVVerificationCodeRepository) Issue(ctx context.Context, typ string, key string, code string, expireAt time.Time) error {
	metadata := &model_pb.VerificationCodeMetadata{Code: code, IssuedAt: timestamppb.Now(), ExpiredAt: timestamppb.New(expireAt)}
	raw, err := proto.Marshal(metadata)
	if err != nil {
		return err
	}
	return vr.db.Set(vr.getKey(typ, key), raw, pebble.Sync)
}

func (vr *KVVerificationCodeRepository) Validate(ctx context.Context, typ string, key string, code string) (bool, error) {
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
