package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/httprate"
)

func TestAPIOnlyLimitsJustTheAPI(t *testing.T) {
	limiter := httprate.LimitBy(2, time.Hour, func(r *http.Request) (string, error) { return "client", nil })
	handler := apiOnly(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	get := func(path string) int {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	for i := 0; i < 10; i++ {
		if code := get("/chunk-ABC123.js"); code != http.StatusOK {
			t.Fatalf("static request %d: got %d, want 200", i, code)
		}
	}
	if code := get(apiPathPrefix + "Login"); code != http.StatusOK {
		t.Fatalf("first API request: got %d, want 200", code)
	}
	if code := get(apiPathPrefix + "FileDownload/x"); code != http.StatusOK {
		t.Fatalf("second API request: got %d, want 200", code)
	}
	if code := get(apiPathPrefix + "Login"); code != http.StatusTooManyRequests {
		t.Fatalf("third API request: got %d, want 429", code)
	}
	if code := get("/index.html"); code != http.StatusOK {
		t.Fatalf("static request after the API budget is spent: got %d, want 200", code)
	}
}
