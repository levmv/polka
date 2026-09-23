package web

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/koreader"
)

const (
	preparedDownloadTTL            = 10 * time.Minute
	maxPreparedDownloads           = 16
	maxPreparedDownloadBytes int64 = 512 << 20
)

// Prepared files are ephemeral renditions, never managed assets. Each request
// gets an independent section reader; cleanup waits for active downloads. The
// conversion gate and converter's output limit also bound in-flight files.
type preparedDownload struct {
	userID, assetID int64
	target          converter.Target
	filename        string
	hasWarnings     bool
	file            *os.File
	size            int64
	cleanup         func()
	expires         time.Time
	readers         int
}

type preparedDownloadStore struct {
	mu    sync.Mutex
	files map[string]*preparedDownload
	bytes int64
}

func (s *preparedDownloadStore) remove(token string) {
	p := s.files[token]
	p.cleanup()
	s.bytes -= p.size
	delete(s.files, token)
}

func (s *preparedDownloadStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, p := range s.files {
		if !now.Before(p.expires) {
			p.expires = time.Time{}
			if p.readers == 0 {
				s.remove(token)
			}
		}
	}
}

func (s *preparedDownloadStore) add(p *preparedDownload) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for token, old := range s.files {
		if old.readers == 0 && !now.Before(old.expires) {
			s.remove(token)
		}
	}
	for len(s.files) >= maxPreparedDownloads || s.bytes+p.size > maxPreparedDownloadBytes {
		// Retire the oldest idle rendition. Never remove a file being transferred.
		var oldest string
		for token, old := range s.files {
			if old.readers != 0 {
				continue
			}
			if oldest == "" || old.expires.Before(s.files[oldest].expires) {
				oldest = token
			}
		}
		if oldest == "" {
			return "", fmt.Errorf("prepared download capacity reached")
		}
		s.remove(oldest)
	}
	if s.files == nil {
		s.files = make(map[string]*preparedDownload)
	}
	token := rand.Text()
	p.expires = now.Add(preparedDownloadTTL)
	s.files[token] = p
	s.bytes += p.size
	return token, nil
}

func (s *preparedDownloadStore) acquire(token string, userID int64) (*preparedDownload, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.files[token]
	if p == nil || p.userID != userID || !time.Now().Before(p.expires) {
		return nil, func() {}
	}
	p.readers++
	return p, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		p.readers--
		if p.readers == 0 && !time.Now().Before(p.expires) {
			s.remove(token)
		}
	}
}

func (s *Server) runPreparedDownloadCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.preparedDownloads.expire(now)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) handlePrepareDownload(w http.ResponseWriter, r *http.Request) {
	s.handleConvertedDownload(w, r, true)
}

func (s *Server) handlePreparedDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, release := s.preparedDownloads.acquire(r.PathValue("token"), UserID(r.Context()))
	if p == nil {
		http.Error(w, "Download expired. Please prepare it again.", http.StatusNotFound)
		return
	}
	defer release()
	if _, ok := s.requireAssetAccess(w, r, p.assetID); !ok {
		return
	}
	if r.Method == http.MethodGet {
		if hash, err := koreader.PartialMD5(io.NewSectionReader(p.file, 0, p.size)); err == nil {
			_ = s.db.RememberKOReaderHash(r.Context(), p.assetID, hash, string(p.target))
		}
	}
	w.Header().Set("Content-Type", converter.TargetMediaType(p.target))
	w.Header().Set("Content-Disposition", fileContentDisposition("attachment", p.filename))
	if p.hasWarnings {
		w.Header().Set("X-Polka-Conversion-Warnings", "true")
	}
	http.ServeContent(w, r, p.filename, time.Time{}, io.NewSectionReader(p.file, 0, p.size))
}
