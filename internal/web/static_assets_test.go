package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testStaticFS() fstest.MapFS {
	return fstest.MapFS{
		"app.js":                  {Data: []byte("console.log('identity')")},
		"app.js.gz":               {Data: []byte("gzip-bytes-for-app-js")},
		"app-icon.svg":            {Data: []byte("<svg/>")},
		"manifest.webmanifest":    {Data: []byte(`{"display":"standalone"}`)},
		"manifest.webmanifest.gz": {Data: []byte("gzip-bytes-for-manifest")},
		"pdfjs/x.bcmap":           {Data: []byte("bcmap")},
	}
}

// The delivery contract for the embedded bundle: negotiate the precompressed
// sibling, keep a distinct validator per representation, and never hand out the
// `.gz` file as an asset of its own.
func TestStaticAssetDelivery(t *testing.T) {
	handler := newStaticAssets(testStaticFS())

	tests := []struct {
		name           string
		path           string
		acceptEncoding string
		wantStatus     int
		wantEncoding   string
		wantBody       string
		wantType       string
	}{
		{
			name:           "gzip client gets the precompressed sibling",
			path:           "/app.js",
			acceptEncoding: "gzip, deflate, br",
			wantStatus:     http.StatusOK,
			wantEncoding:   "gzip",
			wantBody:       "gzip-bytes-for-app-js",
			wantType:       "text/javascript; charset=utf-8",
		},
		{
			name:       "client without gzip gets the original bytes",
			path:       "/app.js",
			wantStatus: http.StatusOK,
			wantBody:   "console.log('identity')",
			wantType:   "text/javascript; charset=utf-8",
		},
		{
			name:           "explicit refusal is honoured",
			path:           "/app.js",
			acceptEncoding: "gzip;q=0, identity",
			wantStatus:     http.StatusOK,
			wantBody:       "console.log('identity')",
		},
		{
			name:           "asset without a sibling stays uncompressed",
			path:           "/app-icon.svg",
			acceptEncoding: "gzip",
			wantStatus:     http.StatusOK,
			wantBody:       "<svg/>",
			wantType:       "image/svg+xml",
		},
		{
			name:           "the compressed sibling is not addressable",
			path:           "/app.js.gz",
			acceptEncoding: "gzip",
			wantStatus:     http.StatusNotFound,
		},
		{
			name:       "missing asset is a 404",
			path:       "/nope.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:           "web manifest uses its registered type and compressed sibling",
			path:           "/manifest.webmanifest",
			acceptEncoding: "gzip",
			wantStatus:     http.StatusOK,
			wantEncoding:   "gzip",
			wantBody:       "gzip-bytes-for-manifest",
			wantType:       "application/manifest+json",
		},
		{
			name:       "traversal outside the bundle is rejected",
			path:       "/../assets.go",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got, tt.wantEncoding)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if tt.wantType != "" {
				if got := rec.Header().Get("Content-Type"); got != tt.wantType {
					t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
				}
			}
			if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", got)
			}
			if rec.Header().Get("ETag") == "" {
				t.Error("ETag is missing")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
		})
	}
}

// The bundle URLs are stable across releases, so revalidation is what keeps a
// browser off a stale bundle while still avoiding the refetch.
func TestStaticAssetRevalidation(t *testing.T) {
	handler := newStaticAssets(testStaticFS())

	fetch := func(acceptEncoding, ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	identity := fetch("", "").Header().Get("ETag")
	compressed := fetch("gzip", "").Header().Get("ETag")
	if identity == compressed {
		t.Fatalf("identity and gzip share ETag %q; a shared cache could cross them over", identity)
	}

	if got := fetch("", identity); got.Code != http.StatusNotModified {
		t.Errorf("identity revalidation = %d, want 304", got.Code)
	}
	if got := fetch("gzip", compressed); got.Code != http.StatusNotModified {
		t.Errorf("gzip revalidation = %d, want 304", got.Code)
	}
	if got := fetch("gzip", identity); got.Code != http.StatusOK {
		t.Errorf("gzip request with the identity ETag = %d, want a fresh 200", got.Code)
	}
}

func TestStaticAssetMissesDoNotGrowTheCache(t *testing.T) {
	handler := newStaticAssets(testStaticFS())
	for _, requestPath := range []string{"/missing-1.js", "/missing-2.js", "/missing-3.js"} {
		req := httptest.NewRequest(http.MethodGet, requestPath, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", requestPath, rec.Code)
		}
	}
	if got := len(handler.entries); got != 0 {
		t.Fatalf("cached entries after public misses = %d, want 0", got)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("known asset status = %d, want 200", rec.Code)
	}
	if got := len(handler.entries); got != 1 {
		t.Fatalf("cached entries after known asset = %d, want 1", got)
	}
}

// The bundle has to load before there is a session to authenticate.
func TestStaticBundleServedBeforeLogin(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{db: database, dataDir: dir}
	mux, err := s.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	for _, path := range []string{"/static/app.js", "/static/manifest.webmanifest"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			s.authMiddleware(mux).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Fatal("empty response body")
			}
			if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
				t.Errorf("Content-Encoding = %q, want gzip (did the frontend build run?)", got)
			}
		})
	}
}
