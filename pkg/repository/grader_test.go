package repository

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

func openTestDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open("grader-test", &pebble.Options{Merger: NewKVMerger(), FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close database: %v", err)
		}
	})
	return db
}

func TestGetMetadataReturnsStableCopy(t *testing.T) {
	// With no block cache, closing Get's closer releases its SST block. Race
	// builds poison the freed block, making borrowed return values fail here.
	cache := pebble.NewCache(0)
	defer cache.Unref()
	db, err := pebble.Open("metadata-test", &pebble.Options{Cache: cache, FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	repo, err := NewKVGraderRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want := bytes.Repeat([]byte("value"), 400)
	for i := 0; i < 32; i++ {
		if err := repo.PutMetadata(ctx, 1, []byte(fmt.Sprint(i)), want); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		got, err := repo.GetMetadata(ctx, 1, []byte(fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("metadata %d changed after GetMetadata released its database value", i)
		}
	}
}

// GetAllMetadata returns slices that outlive the iterator it reads them from,
// so the entries must be copies rather than views into pebble's buffers.
func TestGetAllMetadataReturnsStableCopies(t *testing.T) {
	db := openTestDB(t)
	repo, err := NewKVGraderRepository(db)
	if err != nil {
		t.Fatalf("NewKVGraderRepository: %v", err)
	}
	ctx := context.Background()
	const graderId = 7
	const n = 64
	want := map[string][]byte{}
	for i := 0; i < n; i++ {
		key := []byte(fmt.Sprintf("docker:metadata:%d", i))
		value := bytes.Repeat([]byte{byte(i)}, 32+i)
		if err := repo.PutMetadata(ctx, graderId, key, value); err != nil {
			t.Fatalf("PutMetadata: %v", err)
		}
		want[string(key)] = value
	}
	// Metadata of another grader must not leak into the result.
	if err := repo.PutMetadata(ctx, graderId+1, []byte("docker:metadata:other"), []byte("x")); err != nil {
		t.Fatalf("PutMetadata: %v", err)
	}
	// Flushing moves the data from the memtable into sstables, which is where
	// iterator buffers get recycled once the iterator is closed.
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	keys, values, err := repo.GetAllMetadata(ctx, graderId)
	if err != nil {
		t.Fatalf("GetAllMetadata: %v", err)
	}
	// Churn the database after the iterator has been closed so any slice still
	// aliasing iterator memory would be observed as corrupted.
	for i := 0; i < 4096; i++ {
		if err := db.Set([]byte(fmt.Sprintf("noise:%d", i)), bytes.Repeat([]byte{0xff}, 64), pebble.NoSync); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if len(keys) != n || len(values) != n {
		t.Fatalf("got %d keys / %d values, want %d", len(keys), len(values), n)
	}
	for i := range keys {
		wantValue, ok := want[string(keys[i])]
		if !ok {
			t.Errorf("unexpected key %q", keys[i])
			continue
		}
		if !bytes.Equal(values[i], wantValue) {
			t.Errorf("value for %q corrupted: got %x, want %x", keys[i], values[i], wantValue)
		}
		delete(want, string(keys[i]))
	}
	for k := range want {
		t.Errorf("key %q missing from result", k)
	}
}
