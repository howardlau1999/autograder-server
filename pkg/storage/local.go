package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LocalStorage struct {
	basePath string
}

// safeJoin joins p onto basePath and verifies the cleaned result stays within
// basePath, rejecting any path that would escape the storage root via "..".
func (ls *LocalStorage) safeJoin(p string) (string, error) {
	full := filepath.Join(ls.basePath, p)
	rel, err := filepath.Rel(ls.basePath, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("storage: path %q escapes base directory", p)
	}
	return full, nil
}

func (ls *LocalStorage) NotExists(ctx context.Context, path string) (bool, error) {
	full, err := ls.safeJoin(path)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func (ls *LocalStorage) Delete(ctx context.Context, path string) error {
	full, err := ls.safeJoin(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}

func (ls *LocalStorage) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	full, err := ls.safeJoin(path)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (ls *LocalStorage) Size(ctx context.Context, path string) (int64, error) {
	full, err := ls.safeJoin(path)
	if err != nil {
		return 0, err
	}
	s, err := os.Stat(full)
	if err != nil {
		return 0, err
	}
	return s.Size(), nil
}

func (ls *LocalStorage) Put(ctx context.Context, path string, r io.Reader) error {
	full, err := ls.safeJoin(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_TRUNC|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func NewLocalStorage(basePath string) *LocalStorage {
	return &LocalStorage{basePath: basePath}
}
