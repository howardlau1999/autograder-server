package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

type LocalStorage struct {
	basePath string
}

func (ls *LocalStorage) NotExists(ctx context.Context, path string) (bool, error) {
	_, err := os.Stat(filepath.Join(ls.basePath, path))
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func (ls *LocalStorage) Delete(ctx context.Context, path string) error {
	return os.RemoveAll(filepath.Join(ls.basePath, path))
}

func (ls *LocalStorage) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(ls.basePath, path))
}

func (ls *LocalStorage) Size(ctx context.Context, path string) (int64, error) {
	s, err := os.Stat(filepath.Join(ls.basePath, path))
	if err != nil {
		return 0, err
	}
	return s.Size(), nil
}

func (ls *LocalStorage) Put(ctx context.Context, path string, r io.Reader) error {
	dir, filename := filepath.Split(path)
	os.MkdirAll(filepath.Join(ls.basePath, dir), 0755)
	f, err := os.OpenFile(filepath.Join(ls.basePath, dir, filename), os.O_TRUNC|os.O_CREATE|os.O_RDWR, 0644)
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
