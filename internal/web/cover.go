package web

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/levmv/polka/internal/covers"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if !s.requireCoverAccess(w, r, workID) {
		return
	}

	// Cover reads are intentionally filesystem-only. The API already checked
	// SQLite and emitted has_cover/cover_version; cover_version is only a
	// browser cache-busting query token and is ignored here.
	variant, err := coverVariantFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	root := s.dataRoot()
	originalPath, err := root.Resolve(covers.OriginalPath(workID))
	if err != nil {
		serverError(w, err)
		return
	}
	originalStat, err := os.Stat(originalPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No stored cover: serve a generated fallback instead of a 404 that
			// the frontend would have to paper over with a "No Cover" block.
			s.serveGeneratedCover(w, r, workID, variant)
		} else {
			serverError(w, err)
		}
		return
	}

	cacheRel := covers.CachePath(workID, variant)
	cachePath, err := root.Resolve(cacheRel)
	if err != nil {
		serverError(w, err)
		return
	}
	if cacheStat, err := os.Stat(cachePath); err == nil && !cacheStat.ModTime().Before(originalStat.ModTime()) {
		serveCoverFile(w, r, cachePath, covers.ContentTypeJPEG)
		return
	}

	src, err := os.ReadFile(originalPath)
	if err != nil {
		serverError(w, err)
		return
	}
	opts := covers.DefaultOptions()
	processed, err := covers.Process(src, variant, opts)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := storage.Place(root, cacheRel, bytes.NewReader(processed.Bytes), nil); err != nil {
		serverError(w, err)
		return
	}
	serveCoverBytes(w, r, processed.Bytes, filepath.Base(cachePath), time.Now(), processed.ContentType)
}

func (s *Server) requireCoverAccess(w http.ResponseWriter, r *http.Request, workID string) bool {
	_, ok := s.requireAccess(w, r, workID, func(q db.Queryer, scope db.VisibilityScope, workID string) (bool, error) {
		allowed, err := db.CanAccessWork(q, scope, workID)
		if err != nil || allowed || !db.RoleAtLeast(contextUser(r.Context()).Role, db.RoleMember) {
			return allowed, err
		}
		return db.CanAccessTrashedWork(q, scope, workID)
	})
	return ok
}

// serveGeneratedCover renders and serves a deterministic placeholder for a work
// with no stored cover. The image is not cached on disk: it is a pure function
// of title+author (which a single edit can change), so we instead let the
// browser revalidate via an ETag over those inputs.
func (s *Server) serveGeneratedCover(w http.ResponseWriter, r *http.Request, workID string, variant covers.Variant) {
	title, author, found, err := s.db.PlaceholderCoverText(workID)
	if err != nil {
		serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	sum := sha1.Sum([]byte(string(variant) + "\x00" + title + "\x00" + author))
	etag := `"gen-` + covers.CacheVersion + "-" + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=300")
	if ifNoneMatchContains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	enc, err := covers.Placeholder(title, author, variant, covers.DefaultOptions())
	if err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Content-Type", enc.ContentType)
	http.ServeContent(w, r, "cover.jpg", time.Time{}, bytes.NewReader(enc.Bytes))
}

// ifNoneMatchContains performs the weak comparison required for GET
// If-None-Match. Entity-tag contents may legally contain commas, so scan quoted
// tags instead of splitting the header. Malformed input does not match and
// falls through to the standard ServeContent handling.
func ifNoneMatchContains(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "*" {
		return true
	}
	etag = strings.TrimPrefix(etag, "W/")
	for header != "" {
		header = strings.TrimLeft(header, " \t")
		header = strings.TrimPrefix(header, "W/")
		if len(header) < 2 || header[0] != '"' {
			return false
		}
		end := strings.IndexByte(header[1:], '"')
		if end < 0 {
			return false
		}
		end += 2
		if header[:end] == etag {
			return true
		}
		header = strings.TrimLeft(header[end:], " \t")
		if header == "" {
			return false
		}
		if header[0] != ',' {
			return false
		}
		header = header[1:]
	}
	return false
}

func coverVariantFromRequest(r *http.Request) (covers.Variant, error) {
	switch r.URL.Query().Get("variant") {
	case "", string(covers.VariantDisplay):
		return covers.VariantDisplay, nil
	case string(covers.VariantThumb):
		return covers.VariantThumb, nil
	default:
		return "", errors.New("Invalid cover variant")
	}
}

func serveCoverFile(w http.ResponseWriter, r *http.Request, fullPath, contentType string) {
	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
		} else {
			serverError(w, err)
		}
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}

func serveCoverBytes(w http.ResponseWriter, r *http.Request, data []byte, name string, modTime time.Time, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, modTime, bytes.NewReader(data))
}
