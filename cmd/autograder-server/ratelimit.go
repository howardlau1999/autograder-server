package main

import (
	"net/http"
	"strings"
)

// apiPathPrefix is the gRPC path prefix of AutograderService (the proto has
// no package), which the file upload and download handlers share.
const apiPathPrefix = "/AutograderService/"

// apiOnly applies middleware (the rate limiter) to API requests only. Every
// RPC, upload and download lives under apiPathPrefix; the embedded web client
// is made of many small chunks that a single page load fetches in a burst, and
// counting those against the per-client budget would exhaust it before the
// user has done anything.
func apiOnly(middleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		limited := middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, apiPathPrefix) {
				limited.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
