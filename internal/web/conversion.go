package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxConcurrentConversions = 2

// withConversionSlot bounds CPU- and memory-heavy on-demand conversions
// independently from serialized storage mutations. Waiting observes the caller
// context so a closed browser tab does not leave queued work behind.
func (s *Server) withConversionSlot(ctx context.Context, convert func() error) error {
	s.conversionOnce.Do(func() {
		if s.conversionSlots == nil {
			s.conversionSlots = make(chan struct{}, maxConcurrentConversions)
		}
	})
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case s.conversionSlots <- struct{}{}:
		defer func() { <-s.conversionSlots }()
		return convert()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stageConvertedDownload completes and closes a conversion before exposing any
// HTTP response bytes. The ready file remains mode 0600 and is removed when the
// returned cleanup runs; it is never a managed library asset or cache entry.
func (s *Server) stageConvertedDownload(ctx context.Context, ext string, convert func(*os.File) error) (*os.File, int64, func(), error) {
	var ready *os.File
	var tmpPath string
	cleanup := func() {
		if ready != nil {
			_ = ready.Close()
		}
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}

	err := s.withConversionSlot(ctx, func() error {
		tmpDir := filepath.Join(s.dataDir, "tmp", "conversion")
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			return fmt.Errorf("create conversion temp directory: %w", err)
		}
		if err := os.Chmod(tmpDir, 0o700); err != nil {
			return fmt.Errorf("protect conversion temp directory: %w", err)
		}
		if ext != "" && !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		tmp, err := os.CreateTemp(tmpDir, "download-*"+ext)
		if err != nil {
			return fmt.Errorf("create conversion temp file: %w", err)
		}
		tmpPath = tmp.Name()

		convertErr := convert(tmp)
		closeErr := tmp.Close()
		if convertErr != nil {
			return convertErr
		}
		if closeErr != nil {
			return fmt.Errorf("close converted download: %w", closeErr)
		}

		ready, err = os.Open(tmpPath)
		if err != nil {
			return fmt.Errorf("reopen converted download: %w", err)
		}
		return nil
	})
	if err != nil {
		cleanup()
		return nil, 0, func() {}, err
	}
	info, err := ready.Stat()
	if err != nil {
		cleanup()
		return nil, 0, func() {}, fmt.Errorf("stat converted download: %w", err)
	}
	return ready, info.Size(), cleanup, nil
}
