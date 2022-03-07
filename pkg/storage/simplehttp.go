package storage

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"go.uber.org/zap"
)

type SimpleHTTPFS struct {
	baseURL *url.URL
	token   string
}

func (sfs *SimpleHTTPFS) Put(ctx context.Context, path string, body io.Reader) error {
	rel, err := url.Parse(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sfs.baseURL.ResolveReference(rel).String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("token", sfs.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (sfs *SimpleHTTPFS) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sfs.baseURL.ResolveReference(rel).String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("token", sfs.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func NewSimpleHTTPFS(baseURL string, token string) *SimpleHTTPFS {
	base, err := url.Parse(baseURL)
	if err != nil {
		zap.L().Fatal("Client.HTTPFS.New", zap.Error(err))
	}
	return &SimpleHTTPFS{baseURL: base, token: token}
}
