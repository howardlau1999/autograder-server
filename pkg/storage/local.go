package storage

import (
	"context"
	uuid "github.com/google/uuid"
	"io"
	"os"
	"path"
	"path/filepath"
)

type LocalStorage struct {
}

func (ls *LocalStorage) Put(ctx context.Context, dir string, filename string, r io.ReadSeeker) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
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
