package web

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/levmv/polka/internal/storage"
)

// Only verified file identities are cached, never storage paths or open files.
// Managed byte replacements are atomic; reopening a replacement has a different
// file identity. This avoids hashing a large PDF again for every range request.
type readerContentCache struct {
	mu      sync.Mutex
	entries []verifiedReaderContent
}

type verifiedReaderContent struct {
	info os.FileInfo
	hash [16]byte
}

func (cache *readerContentCache) verify(ctx context.Context, f *os.File, expected []byte) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	cache.mu.Lock()
	for _, entry := range cache.entries {
		if os.SameFile(info, entry.info) && info.Size() == entry.info.Size() &&
			info.ModTime().Equal(entry.info.ModTime()) && bytes.Equal(entry.hash[:], expected) {
			cache.mu.Unlock()
			return true, nil
		}
	}
	cache.mu.Unlock()
	digest, err := readerFileHash(ctx, f)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(digest, expected) {
		return false, nil
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) == 128 {
		cache.entries = cache.entries[1:]
	}
	cache.entries = append(cache.entries, verifiedReaderContent{info: info, hash: [16]byte(digest)})
	return true, nil
}

func readerFileHash(ctx context.Context, f *os.File) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	digest, err := storage.HashReader(ctx, f)
	if err != nil {
		return nil, err
	}
	_, err = f.Seek(0, io.SeekStart)
	return digest, err
}

func (s *Server) verifyReaderContent(w http.ResponseWriter, r *http.Request, f *os.File, expected []byte) bool {
	requested := r.URL.Query().Get("source")
	// Only reading requests pin a particular version of the file.
	if requested == "" {
		return true
	}
	if requested != hex.EncodeToString(expected) {
		http.Error(w, "This book changed. Reopen it to read the current file.", http.StatusConflict)
		return false
	}
	valid, err := s.readerContent.verify(r.Context(), f, expected)
	if err != nil {
		serverError(w, r, err)
		return false
	}
	if !valid {
		http.Error(w, "This copy of the book has changed. Reopen the book to continue.", http.StatusConflict)
		return false
	}
	return true
}
