package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type SubmissionReportRepository interface {
	UpdateSubmissionReport(ctx context.Context, id uint64, submissionReport *model_pb.SubmissionReport) error
	GetSubmissionReport(ctx context.Context, id uint64) (*model_pb.SubmissionReport, error)
	DeleteSubmissionReport(ctx context.Context, id uint64) error
}

type KVSubmissionReportRepository struct {
	db *pebble.DB
}

func (sr *KVSubmissionReportRepository) DeleteSubmissionReport(ctx context.Context, id uint64) error {
	return sr.db.Delete(sr.getIdKey(id), pebble.Sync)
}

func (sr *KVSubmissionReportRepository) getIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("submission_report:id:%d", id))
}

func (sr *KVSubmissionReportRepository) UpdateSubmissionReport(ctx context.Context, id uint64, submissionReport *model_pb.SubmissionReport) error {
	raw, err := proto.Marshal(submissionReport)
	if err != nil {
		return err
	}
	err = sr.db.Set(sr.getIdKey(id), raw, pebble.Sync)
	if err != nil {
		return err
	}
	return nil
}

func (sr *KVSubmissionReportRepository) GetSubmissionReport(ctx context.Context, id uint64) (*model_pb.SubmissionReport, error) {
	raw, closer, err := sr.db.Get(sr.getIdKey(id))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	submissionReport := &model_pb.SubmissionReport{}
	err = proto.Unmarshal(raw, submissionReport)
	if err != nil {
		return nil, err
	}
	return submissionReport, nil
}

func NewKVSubmissionReportRepository(db *pebble.DB) SubmissionReportRepository {
	return &KVSubmissionReportRepository{db: db}
}
