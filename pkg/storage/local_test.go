package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStoragePutRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	ls := NewLocalStorage(base)
	escaping := []string{
		"../escaped.txt",
		"../../escaped.txt",
		"a/../../escaped.txt",
		"/../escaped.txt",
	}
	for _, p := range escaping {
		if err := ls.Put(context.Background(), p, strings.NewReader("x")); err == nil {
			t.Errorf("Put(%q) should have been rejected", p)
		}
	}
	// A legitimate nested path must still work and stay within base.
	if err := ls.Put(context.Background(), "sub/dir/ok.txt", strings.NewReader("ok")); err != nil {
		t.Fatalf("Put(legit) failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "sub/dir/ok.txt")); err != nil {
		t.Fatalf("legit file not written: %v", err)
	}
}

func TestLocalStorageOpenRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	// Create a file outside base that an attacker might try to read.
	outside := filepath.Join(filepath.Dir(base), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	defer os.Remove(outside)
	ls := NewLocalStorage(base)
	if _, err := ls.Open(context.Background(), "../secret.txt"); err == nil {
		t.Error("Open with traversal should have been rejected")
	}
}
