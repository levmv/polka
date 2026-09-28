package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/storage"
)

type sourceInfo struct {
	Source
	Format     format.Format
	Size       int64
	SourceHash []byte
	Extension  string
	PageCount  int
	ModTime    time.Time
}

type preparedSource struct {
	info   sourceInfo
	staged storage.StagedFile
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, off)
}

func hashSourceIfKnownSize(ctx context.Context, src Source, knownSizes map[int64]struct{}) ([]byte, bool, error) {
	if knownSizes == nil {
		return nil, false, nil
	}
	if err := context.Cause(ctx); err != nil {
		return nil, false, err
	}
	if err := validateSource(src); err != nil {
		return nil, false, err
	}
	stat, err := os.Stat(src.Path)
	if err != nil {
		return nil, false, fmt.Errorf("stat file: %w", err)
	}
	if _, found := knownSizes[stat.Size()]; !found {
		return nil, false, nil
	}

	f, err := os.Open(src.Path)
	if err != nil {
		return nil, false, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	hash, err := storage.HashReader(ctx, f)
	if err != nil {
		return nil, false, fmt.Errorf("hash file: %w", err)
	}
	return hash, true, nil
}

// prepareSource hashes bytes as it stages them, then detects the format from
// that staged copy. The caller owns the returned staged file.
func prepareSource(ctx context.Context, root storage.Root, src Source) (preparedSource, error) {
	if err := context.Cause(ctx); err != nil {
		return preparedSource{}, err
	}
	if err := validateSource(src); err != nil {
		return preparedSource{}, err
	}

	f, err := os.Open(src.Path)
	if err != nil {
		return preparedSource{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return preparedSource{}, fmt.Errorf("stat file: %w", err)
	}

	ext := format.BookExtension(src.sourceName())
	h := storage.NewHasher()
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, r: f}, N: stat.Size() + 1}
	staged, err := storage.Stage(root, "pending-import"+ext, io.TeeReader(limited, h))
	if err != nil {
		return preparedSource{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			staged.Cleanup()
		}
	}()

	afterCopy, err := f.Stat()
	if err != nil {
		return preparedSource{}, fmt.Errorf("stat file after copy: %w", err)
	}
	if limited.N != 1 || afterCopy.Size() != stat.Size() || !afterCopy.ModTime().Equal(stat.ModTime()) {
		return preparedSource{}, fmt.Errorf("source changed during import: %s; retry when the file is stable", src.sourceName())
	}

	fileHash := h.Sum(nil)
	if err := staged.Relabel(fmt.Sprintf("%x%s", fileHash, ext)); err != nil {
		return preparedSource{}, err
	}
	stagedFile, err := staged.Open()
	if err != nil {
		return preparedSource{}, err
	}
	defer stagedFile.Close()
	kind := format.DetectFormat(src.sourceName(), contextReaderAt{ctx: ctx, r: stagedFile}, stat.Size())
	if err := context.Cause(ctx); err != nil {
		return preparedSource{}, err
	}

	prepared := preparedSource{
		info: sourceInfo{
			Source:     src,
			Size:       stat.Size(),
			SourceHash: fileHash,
			Extension:  ext,
			Format:     kind,
			ModTime:    stat.ModTime(),
		},
		staged: staged,
	}
	cleanup = false
	return prepared, nil
}

func fingerprintSource(ctx context.Context, src Source) (sourceInfo, error) {
	if err := context.Cause(ctx); err != nil {
		return sourceInfo{}, err
	}
	if err := validateSource(src); err != nil {
		return sourceInfo{}, err
	}

	f, err := os.Open(src.Path)
	if err != nil {
		return sourceInfo{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return sourceInfo{}, fmt.Errorf("stat file: %w", err)
	}

	fileHash, err := storage.HashReader(ctx, f)
	if err != nil {
		return sourceInfo{}, fmt.Errorf("hash file: %w", err)
	}

	kind := format.DetectFormat(src.sourceName(), contextReaderAt{ctx: ctx, r: f}, stat.Size())
	if err := context.Cause(ctx); err != nil {
		return sourceInfo{}, err
	}
	ext := format.BookExtension(src.sourceName())

	return sourceInfo{
		Source:     src,
		Size:       stat.Size(),
		SourceHash: fileHash,
		Extension:  ext,
		Format:     kind,
		ModTime:    stat.ModTime(),
	}, nil
}

func validateSource(src Source) error {
	if src.Path == "" {
		return errors.New("source path is required")
	}
	if !HasBookExtension(src.sourceName()) {
		return fmt.Errorf("unsupported book file extension: %s", filepath.Base(src.sourceName()))
	}
	return nil
}

func (s Source) sourceName() string {
	if s.OriginalName != "" {
		return s.OriginalName
	}
	return s.Path
}

func (s Source) sidecarDir() string {
	if s.SidecarDir != "" {
		return s.SidecarDir
	}
	return filepath.Dir(s.Path)
}
