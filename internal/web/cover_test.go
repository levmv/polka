package web

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/workslot"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 6))
	for y := range 6 {
		for x := range 4 {
			img.SetNRGBA(x, y, color.NRGBA{R: 80, G: 120, B: 160, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCoverHandler(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{
		db:      database,
		dataDir: dir,
	}
	reader := mustUser(t, database, "cover-reader", db.RoleReader)
	coverRequest := func(target, workID string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.SetPathValue("id", workID)
		return req.WithContext(withUser(req.Context(), reader))
	}

	coverPath := covers.OriginalPath("w_1")
	os.MkdirAll(filepath.Join(dir, filepath.Dir(coverPath)), 0o755)
	os.WriteFile(filepath.Join(dir, coverPath), testPNG(t), 0o644)
	database.Exec("UPDATE works SET cover_version = 1 WHERE id = 'w_1'")

	req := coverRequest("/covers/w_1", "w_1")
	w := httptest.NewRecorder()
	s.handleCover(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Cache-Control"); got != "private, max-age=86400" {
		t.Errorf("cache control = %q, want private browser caching", got)
	}
	if res.Header.Get("Content-Type") != covers.ContentTypeJPEG {
		t.Errorf("expected jpeg content type, got %q", res.Header.Get("Content-Type"))
	}

	cachePath := filepath.Join(dir, covers.CachePath("w_1", covers.VariantDisplay))
	if _, err := os.Stat(cachePath); err != nil {
		t.Errorf("expected display cache file: %v", err)
	}

	req = coverRequest("/covers/w_1?variant=thumb", "w_1")
	w = httptest.NewRecorder()
	s.handleCover(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected thumb 200, got %d", w.Result().StatusCode)
	}

	// w_2 exists but has no stored cover file: a generated fallback is served.
	req = coverRequest("/covers/w_2", "w_2")
	w = httptest.NewRecorder()
	s.handleCover(w, req)

	res = w.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected generated 200, got %d", res.StatusCode)
	}
	if res.Header.Get("Content-Type") != covers.ContentTypeJPEG {
		t.Errorf("expected jpeg content type, got %q", res.Header.Get("Content-Type"))
	}
	generatedETag := res.Header.Get("ETag")
	if generatedETag == "" {
		t.Errorf("expected ETag on generated cover for revalidation")
	}
	if got := res.Header.Get("Cache-Control"); got != "private, max-age=300" {
		t.Errorf("generated cache control = %q, want private browser caching", got)
	}
	if _, _, err := image.Decode(res.Body); err != nil {
		t.Errorf("generated cover did not decode as an image: %v", err)
	}
	// A no-cover work must not leave a cache file behind; the fallback is
	// regenerated on the fly, never stored.
	if _, err := os.Stat(filepath.Join(dir, covers.CachePath("w_2", covers.VariantDisplay))); !os.IsNotExist(err) {
		t.Errorf("generated cover should not be cached on disk")
	}

	// Conditional fallback requests return before the image is rendered.
	req = coverRequest("/covers/w_2", "w_2")
	req.Header.Set("If-None-Match", generatedETag)
	w = httptest.NewRecorder()
	s.handleCover(w, req)
	res = w.Result()
	if res.StatusCode != http.StatusNotModified {
		t.Errorf("expected generated conditional 304, got %d", res.StatusCode)
	}
	if body, err := io.ReadAll(res.Body); err != nil || len(body) != 0 {
		t.Errorf("generated conditional response body = %d bytes, err=%v", len(body), err)
	}

	// An unknown work ID still 404s — no DB row, nothing to render from.
	req = coverRequest("/covers/w_999", "w_999")
	w = httptest.NewRecorder()
	s.handleCover(w, req)
	if w.Result().StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown work, got %d", w.Result().StatusCode)
	}
}

func TestIfNoneMatchContains(t *testing.T) {
	etag := `"gen-v1-abc"`
	for _, tc := range []struct {
		name   string
		header string
		want   bool
	}{
		{name: "exact", header: etag, want: true},
		{name: "weak", header: `W/"gen-v1-abc"`, want: true},
		{name: "list", header: `"other", "gen-v1-abc"`, want: true},
		{name: "quoted comma", header: `"other,gen-v1-abc", "gen-v1-def"`, want: false},
		{name: "wildcard", header: "*", want: true},
		{name: "different", header: `"gen-v1-def"`, want: false},
		{name: "malformed", header: `not-an-etag, "gen-v1-abc"`, want: false},
		{name: "empty", header: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ifNoneMatchContains(tc.header, etag); got != tc.want {
				t.Fatalf("ifNoneMatchContains(%q, %q) = %v, want %v", tc.header, etag, got, tc.want)
			}
		})
	}
}

func TestCoverHandlerAllowsMemberTrashCoversOnly(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	member := mustUser(t, database, "member-trash-cover", db.RoleMember)
	reader := mustUser(t, database, "reader-trash-cover", db.RoleReader)

	coverPath := covers.OriginalPath("w_1")
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(coverPath)), 0o755); err != nil {
		t.Fatalf("mkdir cover dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, coverPath), testPNG(t), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET cover_version = 1 WHERE id = 'w_1'"); err != nil {
		t.Fatalf("mark cover: %v", err)
	}
	if err := db.SoftDeleteWork(database, "w_1", member.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	memberReq := jsonRequest(t, s, member.ID, http.MethodGet, "/covers/w_1", nil)
	memberRec := httptest.NewRecorder()
	handler.ServeHTTP(memberRec, memberReq)
	if memberRec.Code != http.StatusOK {
		t.Fatalf("member trash cover = %d, want 200; body: %s", memberRec.Code, memberRec.Body.String())
	}

	readerReq := jsonRequest(t, s, reader.ID, http.MethodGet, "/covers/w_1", nil)
	readerRec := httptest.NewRecorder()
	handler.ServeHTTP(readerRec, readerReq)
	if readerRec.Code != http.StatusNotFound {
		t.Fatalf("reader trash cover = %d, want 404", readerRec.Code)
	}
}

func TestAPICoverUpload(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{
		db:      database,
		dataDir: dir,
	}
	member := mustUser(t, database, "member", db.RoleMember)

	uploadReq := func(workID string, filename string, contentType string, content []byte) *http.Request {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		part, _ := mw.CreateFormFile("cover", filename)
		part.Write(content)
		mw.Close()

		req := httptest.NewRequest("POST", "/api/books/"+workID+"/cover", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.SetPathValue("id", workID)
		return req.WithContext(withUser(req.Context(), member))
	}

	// 1. Upload valid PNG
	req := uploadReq("w_1", "test.png", "image/png", testPNG(t))
	w := httptest.NewRecorder()
	s.handleAPICoverUpload(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", res.StatusCode)
	}

	var coverVersion int
	database.QueryRow("SELECT cover_version FROM works WHERE id = 'w_1'").Scan(&coverVersion)
	if coverVersion != 1 {
		t.Errorf("expected cover_version 1, got %d", coverVersion)
	}
	if _, err := os.Stat(filepath.Join(dir, covers.OriginalPath("w_1"))); err != nil {
		t.Errorf("expected original cover file: %v", err)
	}

	// 2. Upload invalid image (text file)
	req = uploadReq("w_2", "test.txt", "text/plain", []byte("not an image"))
	w = httptest.NewRecorder()
	s.handleAPICoverUpload(w, req)

	res = w.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad image, got %d", res.StatusCode)
	}
}

func TestStoreCoverWaitsForStorageSlotAndMergesCurrentOverrides(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	queue := workslot.New()
	s := &Server{db: database, dataDir: dir, storageQueue: queue}
	releasePausedWriteback, err := queue.Acquire(context.Background())
	if err != nil {
		t.Fatalf("hold storage slot: %v", err)
	}

	coverBytes, err := validateCoverBytes(testPNG(t))
	if err != nil {
		t.Fatalf("validate cover: %v", err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- s.storeCoverBytes(context.Background(), "w_1", coverBytes)
	}()
	<-started

	// The held slot represents a write-back paused inside its file mutation. A
	// cover save must not publish a new revision or original until that work exits.
	select {
	case err := <-done:
		releasePausedWriteback()
		t.Fatalf("cover mutation bypassed occupied storage slot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	var coverVersion int
	if err := database.QueryRow("SELECT cover_version FROM works WHERE id = 'w_1'").Scan(&coverVersion); err != nil {
		releasePausedWriteback()
		t.Fatalf("query cover version: %v", err)
	}
	if coverVersion != 0 {
		releasePausedWriteback()
		t.Fatalf("cover_version changed while storage slot was occupied: %d", coverVersion)
	}

	// Commit another override while the cover is queued. The cover transaction
	// must load the then-current map, rather than replace an earlier snapshot.
	if _, err := database.Exec(`UPDATE works SET manual_overrides = '{"title":true}' WHERE id = 'w_1'`); err != nil {
		releasePausedWriteback()
		t.Fatalf("set concurrent override: %v", err)
	}
	releasePausedWriteback()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("store cover: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cover mutation did not resume after storage slot release")
	}

	var rawOverrides string
	if err := database.QueryRow("SELECT cover_version, manual_overrides FROM works WHERE id = 'w_1'").Scan(&coverVersion, &rawOverrides); err != nil {
		t.Fatalf("query stored cover: %v", err)
	}
	overrides := bookmeta.ParseOverrides(rawOverrides)
	if coverVersion != 1 || !overrides["title"] || !overrides["cover"] {
		t.Fatalf("cover state = version:%d overrides:%v; want title+cover at version 1", coverVersion, overrides)
	}
	if _, err := os.Stat(filepath.Join(dir, covers.OriginalPath("w_1"))); err != nil {
		t.Fatalf("stored cover missing: %v", err)
	}
}

func TestAPIGeneratedCoverPreviewDoesNotPersist(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	s := &Server{
		db:      database,
		dataDir: dir,
	}
	member := mustUser(t, database, "member", db.RoleMember)

	req := httptest.NewRequest(
		"POST",
		"/api/books/w_1/cover-generated-preview",
		strings.NewReader(`{"title":"Draft Title","author":"Draft Author"}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w := httptest.NewRecorder()
	s.handleAPIGeneratedCoverPreview(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, w.Body.String())
	}
	if res.Header.Get("Content-Type") != covers.ContentTypeJPEG {
		t.Fatalf("content type = %q, want %q", res.Header.Get("Content-Type"), covers.ContentTypeJPEG)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q, want no-store", res.Header.Get("Cache-Control"))
	}
	bodyA, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read generated preview: %v", err)
	}
	if _, _, err := image.Decode(bytes.NewReader(bodyA)); err != nil {
		t.Fatalf("generated preview did not decode as an image: %v", err)
	}

	req = httptest.NewRequest(
		"POST",
		"/api/books/w_1/cover-generated-preview",
		strings.NewReader(`{"title":"Draft Title","author":"Draft Author","seed":2}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w = httptest.NewRecorder()
	s.handleAPIGeneratedCoverPreview(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for second seed, got %d: %s", w.Result().StatusCode, w.Body.String())
	}
	bodyB, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatalf("read second generated preview: %v", err)
	}
	if bytes.Equal(bodyA, bodyB) {
		t.Fatal("generated preview seed did not change the rendered bytes")
	}

	req = httptest.NewRequest(
		"POST",
		"/api/books/w_1/cover-generated-preview",
		strings.NewReader(`{"title":"Draft Title","author":"Draft Author","seed":2,"style":"label"}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w = httptest.NewRecorder()
	s.handleAPIGeneratedCoverPreview(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for styled preview, got %d: %s", w.Result().StatusCode, w.Body.String())
	}
	bodyC, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatalf("read styled generated preview: %v", err)
	}
	if bytes.Equal(bodyB, bodyC) {
		t.Fatal("generated preview style did not change the rendered bytes")
	}

	var coverVersion int
	if err := database.QueryRow("SELECT cover_version FROM works WHERE id = 'w_1'").Scan(&coverVersion); err != nil {
		t.Fatalf("query cover_version: %v", err)
	}
	if coverVersion != 0 {
		t.Fatalf("cover_version = %d, want 0", coverVersion)
	}
	if _, err := os.Stat(filepath.Join(dir, covers.OriginalPath("w_1"))); !os.IsNotExist(err) {
		t.Fatalf("generated preview should not store original cover, stat err = %v", err)
	}

	req = httptest.NewRequest(
		"POST",
		"/api/books/w_missing/cover-generated-preview",
		strings.NewReader(`{"title":"Draft Title","author":"Draft Author"}`),
	)
	req.SetPathValue("id", "w_missing")
	req = req.WithContext(withUser(req.Context(), member))
	w = httptest.NewRecorder()
	s.handleAPIGeneratedCoverPreview(w, req)
	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown work, got %d", w.Result().StatusCode)
	}
}

func TestAPICoverURL(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	var requestedURL string
	s := &Server{
		db:      database,
		dataDir: dir,
		coverClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(testPNG(t))),
				Request:    req,
			}, nil
		})},
	}
	member := mustUser(t, database, "member", db.RoleMember)

	req := httptest.NewRequest(
		"POST",
		"/api/books/w_1/cover-url",
		strings.NewReader(`{"url":"https://covers.openlibrary.org/b/id/1-L.jpg?default=false"}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w := httptest.NewRecorder()
	s.handleAPICoverURL(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, w.Body.String())
	}
	if requestedURL != "https://covers.openlibrary.org/b/id/1-L.jpg?default=false" {
		t.Fatalf("requested URL = %q", requestedURL)
	}

	var coverVersion int
	var metadataRev int64
	var overrides string
	if err := database.QueryRow(
		"SELECT cover_version, metadata_rev, manual_overrides FROM works WHERE id = 'w_1'",
	).Scan(&coverVersion, &metadataRev, &overrides); err != nil {
		t.Fatalf("query cover fields: %v", err)
	}
	if coverVersion != 1 {
		t.Fatalf("cover_version = %d, want 1", coverVersion)
	}
	if metadataRev != 1 {
		t.Fatalf("metadata_rev = %d, want 1", metadataRev)
	}
	if !bookmeta.ParseOverrides(overrides)["cover"] {
		t.Fatalf("manual_overrides = %q, want cover override", overrides)
	}
	if _, err := os.Stat(filepath.Join(dir, covers.OriginalPath("w_1"))); err != nil {
		t.Fatalf("expected original cover file: %v", err)
	}

	req = httptest.NewRequest(
		"POST",
		"/api/books/w_1/cover-url",
		strings.NewReader(`{"url":"https://example.test/cover.png"}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w = httptest.NewRecorder()
	s.handleAPICoverURL(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported host, got %d", w.Result().StatusCode)
	}

	s.coverClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider offline")
	})}
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/books/w_1/cover-url",
		strings.NewReader(`{"url":"https://covers.openlibrary.org/b/id/1-L.jpg"}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w = httptest.NewRecorder()
	s.handleAPICoverURL(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unavailable remote image, got %d", w.Result().StatusCode)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "Remote image unavailable" {
		t.Fatalf("remote image error = %q; want cause-only response", got)
	}
}

func TestRemoteCoverURLRedirectValidation(t *testing.T) {
	if _, err := parseRemoteCoverURL("https://archive.org/download/olcovers39/olcovers39-L.zip/392508-L.jpg"); err == nil {
		t.Fatal("direct archive.org cover URL was accepted; want direct provider URL only")
	}

	openLibrary := httptest.NewRequest(
		http.MethodGet,
		"https://covers.openlibrary.org/b/id/392508-L.jpg?default=false",
		nil,
	)
	archiveCover := mustURL(t, "https://archive.org/download/olcovers39/olcovers39-L.zip/392508-L.jpg")
	if err := validateRemoteCoverRedirectURL(archiveCover, []*http.Request{openLibrary}); err != nil {
		t.Fatalf("Open Library archive cover redirect rejected: %v", err)
	}

	archiveCDN := mustURL(t, "https://ia600409.us.archive.org/view_archive.php?archive=/6/items/olcovers39/olcovers39-L.zip&file=392508-L.jpg")
	archiveStep := httptest.NewRequest(http.MethodGet, archiveCover.String(), nil)
	if err := validateRemoteCoverRedirectURL(archiveCDN, []*http.Request{openLibrary, archiveStep}); err != nil {
		t.Fatalf("Open Library archive CDN redirect rejected: %v", err)
	}

	google := httptest.NewRequest(http.MethodGet, "https://books.google.com/cover.jpg", nil)
	if err := validateRemoteCoverRedirectURL(archiveCover, []*http.Request{google}); err == nil {
		t.Fatal("Google -> archive.org redirect accepted; want only Open Library archive cover redirects")
	}

	otherHost := mustURL(t, "https://example.test/cover.jpg")
	if err := validateRemoteCoverRedirectURL(otherHost, []*http.Request{openLibrary}); err == nil {
		t.Fatal("Open Library -> non-archive redirect accepted")
	}
}

func TestCoverSearchDuckDuckGoFiltering(t *testing.T) {
	s := &Server{
		coverSearchClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "duckduckgo.com" {
				t.Fatalf("unexpected host %q", req.URL.Host)
			}
			var body string
			switch req.URL.Path {
			case "/":
				body = `<html><script>var vqd="123-456";</script></html>`
			case "/i.js":
				query := req.URL.Query().Get("q")
				if strings.Contains(query, "site:amazon.com") {
					body = `{"results":[
						{"image":"https://images.example/dune.jpg","thumbnail":"https://thumb.example/dune.jpg","url":"https://www.goodreads.com/book/show/1","width":600,"height":900},
						{"image":"https://images.example/small.jpg","thumbnail":"https://thumb.example/small.jpg","url":"https://example.com/small","width":200,"height":300},
						{"image":"https://images.example/wide.jpg","thumbnail":"https://thumb.example/wide.jpg","url":"https://example.com/wide","width":900,"height":600}
					]}`
				} else {
					body = `{"results":[
						{"image":"https://images.example/dune.jpg","thumbnail":"https://thumb.example/dune.jpg","url":"https://www.goodreads.com/book/show/1","width":600,"height":900},
						{"image":"https://cdn.example/dune-alt.jpg","thumbnail":"","url":"https://covers.example/dune-alt","width":700,"height":1050}
					]}`
				}
			default:
				t.Fatalf("unexpected path %q", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		})},
	}

	results, err := s.searchWebCoverCandidates(context.Background(), "Dune", "Frank Herbert")
	if err != nil {
		t.Fatalf("searchWebCoverCandidates: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2: %#v", len(results), results)
	}
	if results[0].Source != "Goodreads" {
		t.Fatalf("first source = %q, want Goodreads", results[0].Source)
	}
	if results[0].PreviewURL != "https://thumb.example/dune.jpg" {
		t.Fatalf("first preview URL = %q", results[0].PreviewURL)
	}
	if results[1].PreviewURL != results[1].SourceURL {
		t.Fatalf("missing thumbnail should fall back to source URL")
	}
}

func TestCoverSearchReturnsEmptyWhenAnyQuerySucceeds(t *testing.T) {
	s := &Server{
		coverSearchClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			query := req.URL.Query().Get("q")
			if strings.Contains(query, "site:amazon.com") {
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Status:     "502 Bad Gateway",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("bad gateway")),
					Request:    req,
				}, nil
			}

			var body string
			switch req.URL.Path {
			case "/":
				body = `<html><script>var vqd="123-456";</script></html>`
			case "/i.js":
				body = `{"results":[]}`
			default:
				t.Fatalf("unexpected path %q", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		})},
	}

	results, err := s.searchWebCoverCandidates(context.Background(), "Unknown Book", "")
	if err != nil {
		t.Fatalf("searchWebCoverCandidates returned error after one successful empty query: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(results))
	}
}

func TestAPICoverSearchProviderErrorStatesCause(t *testing.T) {
	database, _ := setupTestDB(t)
	defer database.Close()

	s := &Server{
		db: database,
		coverSearchClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("provider offline")
		})},
	}
	member := mustUser(t, database, "member-cover-search-error", db.RoleMember)
	req := httptest.NewRequest(http.MethodGet, "/api/books/w_1/cover-search?title=Dune", nil)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w := httptest.NewRecorder()
	s.handleAPICoverSearch(w, req)

	if w.Result().StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for unavailable provider, got %d", w.Result().StatusCode)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "Cover provider unavailable" {
		t.Fatalf("cover search error = %q; want cause-only response", got)
	}
}

func TestCoverSearchTokenValidation(t *testing.T) {
	s := &Server{coverSearchKey: bytes.Repeat([]byte{7}, 32)}
	payload := coverSearchTokenPayload{
		SourceURL:  "https://images.example/cover.jpg",
		PreviewURL: "https://thumb.example/cover.jpg",
		Source:     "example.test",
		ExpiresAt:  time.Now().Add(time.Minute).Unix(),
	}
	token, err := s.signCoverSearchToken(payload)
	if err != nil {
		t.Fatalf("signCoverSearchToken: %v", err)
	}
	got, err := s.verifyCoverSearchToken(token)
	if err != nil {
		t.Fatalf("verifyCoverSearchToken: %v", err)
	}
	if got.SourceURL != payload.SourceURL || got.PreviewURL != payload.PreviewURL {
		t.Fatalf("payload mismatch: %#v", got)
	}
	if _, err := s.verifyCoverSearchToken(token + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}

	expired := payload
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	expiredToken, err := s.signCoverSearchToken(expired)
	if err != nil {
		t.Fatalf("sign expired token: %v", err)
	}
	if _, err := s.verifyCoverSearchToken(expiredToken); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestPublicImageHostValidationBlocksLocalNamesAndLiteralAddresses(t *testing.T) {
	for _, host := range []string{
		"localhost",
		"cover.localhost",
		"cover.local",
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.10",
		"169.254.1.1",
		"100.64.0.1",
		"198.18.0.1",
		"240.0.0.1",
		"::1",
		"fc00::1",
		"fe80::1",
	} {
		if err := validatePublicImageHost(host); err == nil {
			t.Fatalf("host %q accepted; want blocked", host)
		}
	}
	for _, host := range []string{"8.8.8.8", "2606:4700:4700::1111", "images.example"} {
		if err := validatePublicImageHost(host); err != nil {
			t.Fatalf("host %q rejected: %v", host, err)
		}
	}
}

func TestPublicImageClientRejectsUnsafeRedirectBeforeFollowing(t *testing.T) {
	client := defaultPublicImageClient()
	start := httptest.NewRequest(http.MethodGet, "https://8.8.8.8/cover.png", nil)
	redirect := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/cover.png", nil)
	if err := client.CheckRedirect(redirect, []*http.Request{start}); err == nil {
		t.Fatal("unsafe redirect accepted")
	}
	redirect = httptest.NewRequest(http.MethodGet, "https://8.8.4.4/cover.png", nil)
	if err := client.CheckRedirect(redirect, []*http.Request{start}); err != nil {
		t.Fatalf("public redirect rejected: %v", err)
	}
}

func TestAPICoverSearchApply(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	var requestedURL string
	s := &Server{
		db:             database,
		dataDir:        dir,
		coverSearchKey: bytes.Repeat([]byte{9}, 32),
		publicImageClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(testPNG(t))),
				Request:    req,
			}, nil
		})},
	}
	member := mustUser(t, database, "member-cover-search", db.RoleMember)
	token, err := s.signCoverSearchToken(coverSearchTokenPayload{
		SourceURL:  "https://images.example/cover.png",
		PreviewURL: "https://thumb.example/cover.png",
		Source:     "images.example",
		ExpiresAt:  time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/books/w_1/cover-search",
		strings.NewReader(`{"token":"`+token+`"}`),
	)
	req.SetPathValue("id", "w_1")
	req = req.WithContext(withUser(req.Context(), member))
	w := httptest.NewRecorder()
	s.handleAPICoverSearchApply(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Result().StatusCode, w.Body.String())
	}
	if requestedURL != "https://images.example/cover.png" {
		t.Fatalf("requested URL = %q", requestedURL)
	}
	var coverVersion int
	if err := database.QueryRow("SELECT cover_version FROM works WHERE id = 'w_1'").Scan(&coverVersion); err != nil {
		t.Fatalf("query cover_version: %v", err)
	}
	if coverVersion != 1 {
		t.Fatalf("cover_version = %d, want 1", coverVersion)
	}
}
