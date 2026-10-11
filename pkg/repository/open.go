package repository

import (
	"errors"
	"fmt"
	"io/fs"

	pebblev1 "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/v2"
	pebblev1vfs "github.com/cockroachdb/pebble/vfs"
	"go.uber.org/zap"
)

const kvMergerName = "autograder-kv-merger"

// OpenDB opens the key-value store in dir, creating it when it does not exist.
//
// Stores written by earlier releases (pebble v1 at its default format) are at
// a format major version that pebble v2 refuses to open. OpenDB first
// ratchets such a store in place, using pebble v1, to the newest format that
// v1 knows, which is also accepted by v2. The upgrade is one-way: afterwards
// the store can still be opened by the previous release (pebble v1.1) but not
// by anything older, so back up the data directory before upgrading.
func OpenDB(dir string) (*pebble.DB, error) {
	if err := upgradeLegacyFormat(dir); err != nil {
		return nil, err
	}
	// FormatMinSupported leaves an upgraded store at the version pebble v1
	// wrote, so a rollback to the previous release stays possible.
	return pebble.Open(dir, &pebble.Options{Merger: NewKVMerger(), FormatMajorVersion: pebble.FormatMinSupported})
}

// upgradeLegacyFormat ratchets a store that pebble v2 cannot open yet. It is a
// no-op for new directories and for stores already at a supported version.
func upgradeLegacyFormat(dir string) error {
	desc, err := pebblev1.Peek(dir, pebblev1vfs.Default)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspecting store %q: %w", dir, err)
	}
	// Format major versions are numbered identically in both pebble modules
	// up to v1's newest, so the constants compare directly.
	if !desc.Exists || uint64(desc.FormatMajorVersion) >= uint64(pebble.FormatMinSupported) {
		return nil
	}
	zap.L().Warn(
		"DB.FormatUpgrade",
		zap.String("dir", dir),
		zap.Uint64("from", uint64(desc.FormatMajorVersion)),
		zap.Uint64("to", uint64(pebblev1.FormatNewest)),
	)
	// Open ratchets the store to Options.FormatMajorVersion when it is newer.
	db, err := pebblev1.Open(dir, &pebblev1.Options{Merger: legacyMerger(), FormatMajorVersion: pebblev1.FormatNewest})
	if err != nil {
		return fmt.Errorf("upgrading store %q: %w", dir, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("upgrading store %q: %w", dir, err)
	}
	return nil
}

// legacyMerger is NewKVMerger for pebble v1; the merger name is checked
// against the OPTIONS file on open. KVMerge satisfies both ValueMerger
// interfaces as they declare the same methods.
func legacyMerger() *pebblev1.Merger {
	return &pebblev1.Merger{
		Name: kvMergerName,
		Merge: func(key, value []byte) (pebblev1.ValueMerger, error) {
			return NewKVMerge(key, value)
		},
	}
}
