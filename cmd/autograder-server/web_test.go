package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"autograder-server/pkg/web"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/html"
)

func assertRenderedWebIndex(t *testing.T, resources fs.FS) {
	t.Helper()
	config := &ServerProvidedTokens{
		ServerProvided:  "true",
		HcaptchaSiteKey: "configured-captcha-\"<>&",
		GithubClientId:  "configured-client-\"<>&",
	}
	router := chi.NewRouter()
	if err := initStaticWebResources(router, resources, config); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/index.html", "/login", "/github/login?code=test", "/course/42/assignment/7"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("index response: status %d, headers %v", w.Code, w.Header())
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("index response has no CSP")
			}
			doc, err := html.Parse(strings.NewReader(w.Body.String()))
			if err != nil {
				t.Fatal(err)
			}
			var data string
			var visit func(*html.Node)
			visit = func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "script" {
					for _, attr := range n.Attr {
						if attr.Key == "id" && attr.Val == "server-config" {
							for child := n.FirstChild; child != nil; child = child.NextSibling {
								if child.Type == html.TextNode {
									data += child.Data
								}
							}
						}
					}
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(doc)
			var got struct {
				HcaptchaSiteKey string `json:"hcaptchaSiteKey"`
				GithubClientId  string `json:"githubClientId"`
			}
			if err := json.Unmarshal([]byte(data), &got); err != nil {
				t.Fatalf("invalid server config %q: %v", data, err)
			}
			if got.HcaptchaSiteKey != config.HcaptchaSiteKey || got.GithubClientId != config.GithubClientId {
				t.Fatalf("server config not preserved: %+v", got)
			}
		})
	}
}

func TestWebSourceIndexRendersConfig(t *testing.T) {
	index, err := os.ReadFile("../../web/src/index.html")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("web submodule is not checked out")
	}
	if err != nil {
		t.Fatal(err)
	}
	assertRenderedWebIndex(t, fstest.MapFS{"index.html": &fstest.MapFile{Data: index}})
}

func TestEmbeddedWebIndexRendersConfig(t *testing.T) {
	resources, err := fs.Sub(web.WebResources, "dist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(resources, "index.html"); errors.Is(err, fs.ErrNotExist) {
		t.Skip("server-only build: no embedded web index")
	}
	assertRenderedWebIndex(t, resources)
}

func TestStaticWebResourcesRejectInvalidIndex(t *testing.T) {
	for _, index := range []string{"<!-- literal {{...}} -->", "{{.MissingConfigField}}"} {
		resources := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(index)}}
		if err := initStaticWebResources(chi.NewRouter(), resources, &ServerProvidedTokens{}); err == nil {
			t.Fatalf("invalid index accepted: %q", index)
		}
	}
}

func TestStaticWebResourcesWithoutIndex(t *testing.T) {
	resources := fstest.MapFS{"asset.txt": &fstest.MapFile{Data: []byte("static asset")}}
	router := chi.NewRouter()
	if err := initStaticWebResources(router, resources, &ServerProvidedTokens{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/asset.txt", http.StatusOK},
		{"/not-found", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, w.Code, tc.status)
		}
		if tc.status == http.StatusOK && w.Body.String() != "static asset" {
			t.Errorf("asset content changed: %q", w.Body.String())
		}
	}
}
