package repository

import (
	"bytes"
	"path/filepath"
	"testing"

	model_pb "autograder-server/pkg/model/proto"

	pebblev1 "github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/v2"
	"google.golang.org/protobuf/proto"
)

func mergeableCounter(t *testing.T, n int64) []byte {
	t.Helper()
	raw, err := proto.Marshal(&model_pb.Mergeable{MergeableOneof: &model_pb.Mergeable_Counter{Counter: n}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func counterValue(t *testing.T, db *pebble.DB, key []byte) int64 {
	t.Helper()
	raw, closer, err := db.Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer closer.Close()
	m := &model_pb.Mergeable{}
	if err := proto.Unmarshal(raw, m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter()
}

// A store written by the previous release (pebble v1 at its default format)
// must be upgraded in place and keep its contents, including pending merges.
func TestOpenDBUpgradesLegacyStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")

	legacy, err := pebblev1.Open(dir, &pebblev1.Options{Merger: legacyMerger()})
	if err != nil {
		t.Fatal(err)
	}
	if got := legacy.FormatMajorVersion(); uint64(got) >= uint64(pebble.FormatMinSupported) {
		t.Fatalf("pebble v1 created the store at format %d, which v2 opens anyway; the test no longer exercises the upgrade", got)
	}
	if err := legacy.Set([]byte("user:1"), []byte("alice"), pebblev1.Sync); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := legacy.Merge([]byte("counter"), mergeableCounter(t, 2), pebblev1.Sync); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := pebble.Open(dir, &pebble.Options{Merger: NewKVMerger()}); err == nil {
		t.Fatal("pebble v2 opened the legacy store directly; the upgrade path is untested")
	}

	for round := 0; round < 2; round++ { // the second open must find nothing to do
		db, err := OpenDB(dir)
		if err != nil {
			t.Fatalf("OpenDB round %d: %v", round, err)
		}
		if got := db.FormatMajorVersion(); uint64(got) < uint64(pebble.FormatMinSupported) {
			t.Errorf("format major version %d after upgrade, want >= %d", got, pebble.FormatMinSupported)
		}
		value, closer, err := db.Get([]byte("user:1"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(value, []byte("alice")) {
			t.Errorf("user:1 = %q after upgrade", value)
		}
		closer.Close()
		if got := counterValue(t, db, []byte("counter")); got != 6 {
			t.Errorf("merged counter = %d after upgrade, want 6", got)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenDBCreatesStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "db") // parent does not exist either
	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Merge([]byte("counter"), mergeableCounter(t, 5), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if got := counterValue(t, db, []byte("counter")); got != 5 {
		t.Errorf("counter = %d, want 5", got)
	}
}
