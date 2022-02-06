package repository

import (
	model_pb "autograder-server/pkg/model/proto"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

type UnfinishedSubmission struct {
	SubmissionId uint64
	AssignmentId uint64
}

type SubmissionReportRepository interface {
	UpdateSubmissionReport(ctx context.Context, id uint64, submissionReport *model_pb.SubmissionReport) error
	GetSubmissionReport(ctx context.Context, id uint64) (*model_pb.SubmissionReport, error)
	UpdateSubmissionBriefReport(ctx context.Context, id uint64, submissionBriefReport *model_pb.SubmissionBriefReport) error
	GetSubmissionBriefReport(ctx context.Context, id uint64) (*model_pb.SubmissionBriefReport, error)
	DeleteSubmissionReport(ctx context.Context, id uint64) error
	MarkUnfinishedSubmission(ctx context.Context, id uint64, assignmentId uint64) error
	GetUnfinishedSubmissions(ctx context.Context) ([]UnfinishedSubmission, error)
	DeleteUnfinishedSubmission(ctx context.Context, id uint64) error
}

type KVSubmissionReportRepository struct {
	db *pebble.DB
}

func (sr *KVSubmissionReportRepository) MarkUnfinishedSubmission(ctx context.Context, id uint64, assignmentId uint64) error {
	return sr.db.Set(sr.getUnfinishedKey(id), Uint64ToBytes(assignmentId), pebble.Sync)
}

func (sr *KVSubmissionReportRepository) DeleteUnfinishedSubmission(ctx context.Context, id uint64) error {
	return sr.db.Delete(sr.getUnfinishedKey(id), pebble.Sync)
}

func (sr *KVSubmissionReportRepository) GetUnfinishedSubmissions(ctx context.Context) ([]UnfinishedSubmission, error) {
	prefix := sr.getUnfinishedPrefix()
	prefixLen := len(prefix)
	iter := sr.db.NewIter(PrefixIterOptions(prefix))
	var ids []UnfinishedSubmission
	for iter.First(); iter.Valid(); iter.Next() {
		ids = append(ids, UnfinishedSubmission{
			SubmissionId: binary.BigEndian.Uint64(iter.Key()[prefixLen:]),
			AssignmentId: binary.BigEndian.Uint64(iter.Value()),
		})
	}
	iter.Close()
	return ids, nil
}

func (sr *KVSubmissionReportRepository) getUnfinishedPrefix() []byte {
	return []byte("submission:unfinished:")
}

func (sr *KVSubmissionReportRepository) getUnfinishedKey(id uint64) []byte {
	return append(sr.getUnfinishedPrefix(), Uint64ToBytes(id)...)
}

func (sr *KVSubmissionReportRepository) UpdateSubmissionBriefReport(ctx context.Context, id uint64, submissionBriefReport *model_pb.SubmissionBriefReport) error {
	raw, err := proto.Marshal(submissionBriefReport)
	if err != nil {
		return err
	}
	err = sr.db.Set(sr.getBriefIdKey(id), raw, pebble.Sync)
	if err != nil {
		return err
	}
	return nil
}

func (sr *KVSubmissionReportRepository) GetSubmissionBriefReport(ctx context.Context, id uint64) (*model_pb.SubmissionBriefReport, error) {
	raw, closer, err := sr.db.Get(sr.getBriefIdKey(id))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	submissionBriefReport := &model_pb.SubmissionBriefReport{}
	err = proto.Unmarshal(raw, submissionBriefReport)
	if err != nil {
		return nil, err
	}
	return submissionBriefReport, nil
}

func (sr *KVSubmissionReportRepository) DeleteSubmissionReport(ctx context.Context, id uint64) error {
	return sr.db.Delete(sr.getIdKey(id), pebble.Sync)
}

func (sr *KVSubmissionReportRepository) getIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("submission_report:id:%d", id))
}

func (sr *KVSubmissionReportRepository) getBriefIdKey(id uint64) []byte {
	return []byte(fmt.Sprintf("submission_report:brief:%d", id))
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
