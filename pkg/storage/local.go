package storage

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"

	uuid "github.com/google/uuid"
)

type LocalStorage struct {
}

func (ls *LocalStorage) Delete(ctx context.Context, path string) error {
	return os.RemoveAll(path)
}

func (ls *LocalStorage) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (ls *LocalStorage) Size(ctx context.Context, path string) (int64, error) {
	s, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return s.Size(), nil
}

func (ls *LocalStorage) Put(ctx context.Context, path string, r io.ReadSeeker) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, filename := filepath.Split(path)
	os.MkdirAll(filepath.Join(cwd, dir), 0755)
	f, err := os.OpenFile(filepath.Join(cwd, dir, filename), os.O_TRUNC|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)

	return err
}

func (ls *LocalStorage) InitUpload() (string, error) {
	tmpFilename := uuid.NewString()
	file, err := os.Create(path.Join("upload_temp", tmpFilename))
	if err != nil {
		return "", err
	}
	defer file.Close()
	return tmpFilename, nil
}

func (ls *LocalStorage) UploadPart(uploadId string, offset int64, buf []byte) error {
	file, err := os.OpenFile(path.Join("upload_temp", uploadId), os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteAt(buf, offset)
	return err
}

func (ls *LocalStorage) FinishUpload(uploadId string, dest string) error {
	return os.Rename(path.Join("upload_temp", uploadId), dest)
}
