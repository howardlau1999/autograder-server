package repository

import (
	"os"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/google/go-cmp/cmp"
	"gotest.tools/v3/assert"
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

func iterAllManifestKeys(t *testing.T, db *pebble.DB) {

	iter := db.NewIter(prefixIterOptions([]byte("manifest:")))
	for iter.First(); iter.Valid(); iter.Next() {
		t.Logf("%s", iter.Key())
	}
	if err := iter.Close(); err != nil {
		t.Errorf("failed to close iter: %v", err)
	}
}

func TestCreateManifest(t *testing.T) {
	db, err := pebble.Open("manifest-test", &pebble.Options{Merger: NewKVMerger(), FS: vfs.NewMem()})
	if err != nil {
		t.Errorf("failed to open database: %v", err)
		return
	}
	defer func(db *pebble.DB) {
		err := db.Close()
		if err != nil {
			t.Errorf("failed to close database: %v", err)
		}
	}(db)
	repo := NewKVManifestRepository(db)
	id, err := repo.CreateManifest(nil, 1, 1)
	if err != nil {
		t.Errorf("failed to create manifest: %v", err)
		return
	}
	t.Logf("created manifest id = %d", id)
	iterAllManifestKeys(t, db)
}

func TestAddFileToManifest(t *testing.T) {
	db, err := pebble.Open("manifest-test", &pebble.Options{Merger: NewKVMerger(), FS: vfs.NewMem()})
	if err != nil {
		t.Errorf("failed to open database: %v", err)
		return
	}
	defer func(db *pebble.DB) {
		err := db.Close()
		if err != nil {
			t.Errorf("failed to close database: %v", err)
		}
	}(db)

	repo := NewKVManifestRepository(db)
	id, err := repo.CreateManifest(nil, 1, 1)
	if err != nil {
		t.Errorf("failed to create manifest: %v", err)
		return
	}
	t.Logf("created manifest id = %d", id)
	filesToAdd := []string{"src/scala/Main.scala", "src/scala/CPU.scala", "src/scala/Del.scala"}
	filesToDelete := []string{"src/scala/Del.scala"}
	expectedFiles := []string{"src/scala/CPU.scala", "src/scala/Main.scala"}

	for _, fn := range filesToAdd {
		_, err = repo.AddFileToManifest(nil, fn, id)
		if err != nil {
			t.Errorf("failed to add file: %v", err)
			return
		}
	}

	for _, fn := range filesToDelete {
		_, err = repo.DeleteFileInManifest(nil, fn, id)
		if err != nil {
			t.Errorf("failed to delete file: %v", err)
			return
		}
	}
	files, err := repo.GetFilesInManifest(nil, id)
	if err != nil {
		t.Errorf("failed to get files in manifest: %v", err)
		return
	}
	assert.Assert(t, cmp.Equal(files, expectedFiles))
}
