package repository

import "github.com/cockroachdb/pebble"

type ExportRepository interface {
}

type KVExportRepository struct {
	db  *pebble.DB
	seq Sequencer
}

func NewKVExportRepository(db *pebble.DB) ExportRepository {
	seq, err := NewKVSequencer(db, []byte("export:next_id"))
	if err != nil {
		panic(err)
	}
	return &KVExportRepository{db: db, seq: seq}
}
