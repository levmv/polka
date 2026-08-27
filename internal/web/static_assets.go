package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// staticAssets serves the embedded frontend bundle with revalidation and
// precompressed variants.
//
// http.FileServer alone is not enough here: embed.FS reports a zero ModTime, so
// it emits neither Last-Modified nor ETag and a browser refetches every bundle
// on each cold load. It also has no notion of the `.gz` siblings the frontend
// build writes. Both matter on the reading path — the PDF bundle and worker are
// well over a megabyte uncompressed.
//
// A reverse proxy in front of polka usually compresses on its own, but it
// cannot invent validators for an upstream that sends none, so the ETag half is
// worth having regardless of deployment.
type staticAssets struct {
	files fs.FS

	mu      sync.RWMutex
	entries map[string]*staticEntry
}

// staticEntry is the resolved, immutable-for-this-process view of one asset.
// The bytes stay in the embedded FS; only the validators are cached.
type staticEntry struct {
	contentType string
	etag        string

	// gzipPath and gzipETag are empty when the build produced no `.gz` sibling,
	// either because the asset does not compress well or because someone ran a
	// bare `go build` without the frontend step.
	gzipPath string
	gzipETag string
}

func newStaticAssets(files fs.FS) *staticAssets {
	return &staticAssets{files: files, entries: make(map[string]*staticEntry)}
}

func (a *staticAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" || !fs.ValidPath(name) || strings.HasSuffix(name, "/") {
		http.NotFound(w, r)
		return
	}
	// The `.gz` siblings are an encoding of another asset, not assets in their
	// own right. Serving them directly would hand out gzip bytes labelled as
	// identity.
	if strings.HasSuffix(name, ".gz") {
		http.NotFound(w, r)
		return
	}

	entry := a.entry(name)
	if entry == nil {
		http.NotFound(w, r)
		return
	}

	servePath, etag := name, entry.etag
	// Vary is required even when this request is served uncompressed: a shared
	// cache must not reuse this response for a client that does accept gzip.
	w.Header().Set("Vary", "Accept-Encoding")
	if entry.gzipPath != "" && acceptsGzip(r) {
		servePath, etag = entry.gzipPath, entry.gzipETag
		w.Header().Set("Content-Encoding", "gzip")
	}

	file, err := a.files.Open(servePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	// embed.FS hands out a seekable view over the embedded bytes, so the payload
	// is never copied per request. Anything else (a test FS) falls back to a read.
	content, ok := file.(io.ReadSeeker)
	if !ok {
		data, err := fs.ReadFile(a.files, servePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		content = bytes.NewReader(data)
	}

	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("ETag", etag)
	// The bundle URLs are stable across releases, so the browser must ask before
	// reusing a copy — otherwise an upgrade leaves it on a stale bundle. The ETag
	// keeps the answer a 304 in the common case.
	w.Header().Set("Cache-Control", "no-cache")

	// Zero modtime: the ETag is the validator, and embed.FS has no meaningful
	// timestamp to offer.
	http.ServeContent(w, r, servePath, time.Time{}, content)
}

// entry resolves and caches known assets. Misses are deliberately not cached:
// the embedded bundle is finite, while public request paths are not.
func (a *staticAssets) entry(name string) *staticEntry {
	a.mu.RLock()
	cached, ok := a.entries[name]
	a.mu.RUnlock()
	if ok {
		return cached
	}

	resolved := a.resolve(name)
	if resolved == nil {
		return nil
	}

	a.mu.Lock()
	a.entries[name] = resolved
	a.mu.Unlock()
	return resolved
}

func (a *staticAssets) resolve(name string) *staticEntry {
	data, err := fs.ReadFile(a.files, name)
	if err != nil {
		return nil
	}

	entry := &staticEntry{
		contentType: staticContentType(name),
		etag:        staticETag(data, ""),
	}

	gzipPath := name + ".gz"
	if gzipData, err := fs.ReadFile(a.files, gzipPath); err == nil {
		entry.gzipPath = gzipPath
		// A distinct validator per representation: a cache holding the gzip copy
		// must not answer an identity request with it, and vice versa.
		entry.gzipETag = staticETag(gzipData, "gz")
	}
	return entry
}

func staticETag(data []byte, suffix string) string {
	sum := sha256.Sum256(data)
	tag := base64.RawURLEncoding.EncodeToString(sum[:16])
	if suffix != "" {
		tag += "-" + suffix
	}
	return `"` + tag + `"`
}

// staticContentType types the asset from its own extension, which must be read
// before the `.gz` suffix is appended — `app.js.gz` is JavaScript sent gzipped,
// not an application/gzip download.
func staticContentType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	case ".wasm":
		return "application/wasm"
	case ".bcmap":
		return "application/octet-stream"
	}
	if byExt := mime.TypeByExtension(path.Ext(name)); byExt != "" {
		return byExt
	}
	return "application/octet-stream"
}

func acceptsGzip(r *http.Request) bool {
	for encoding := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(encoding), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		// "gzip;q=0" is an explicit refusal, not an offer.
		for param := range strings.SplitSeq(params, ";") {
			key, value, found := strings.Cut(strings.TrimSpace(param), "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "q") && isZeroQValue(value) {
				return false
			}
		}
		return true
	}
	return false
}

func isZeroQValue(value string) bool {
	switch strings.TrimSpace(value) {
	case "0", "0.", "0.0", "0.00", "0.000":
		return true
	}
	return false
}
