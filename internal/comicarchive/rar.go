// Package comicarchive owns RAR index and decoder access shared by comic
// inspection and conversion paths.
package comicarchive

import (
	"io"
	"io/fs"
	"time"

	"github.com/nwaples/rardecode/v2"
)

const maxRARDictionaryBytes int64 = 128 << 20

// NewRARReader opens one RAR archive with the same bounded decoder policy used
// by import, metadata inspection, cover extraction, and conversion.
func NewRARReader(src io.ReaderAt, size int64) (*rardecode.Reader, error) {
	return rardecode.NewReader(
		io.NewSectionReader(src, 0, size),
		rardecode.MaxDictionarySize(maxRARDictionaryBytes),
	)
}

// ListRAR reads file headers without decompressing solid archives. The library's
// index API takes a filename; this filesystem exposes only the caller's open
// source, so it cannot reopen a stale storage path or access another volume.
func ListRAR(src io.ReaderAt, size int64) ([]*rardecode.File, error) {
	return rardecode.List(rarSourceName,
		rardecode.FileSystem(rarSourceFS{src: src, size: size}),
		rardecode.MaxDictionarySize(maxRARDictionaryBytes),
	)
}

const rarSourceName = "source.rar"

type rarSourceFS struct {
	src  io.ReaderAt
	size int64
}

func (source rarSourceFS) Open(name string) (fs.File, error) {
	if name != rarSourceName {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return rarSourceFile{io.NewSectionReader(source.src, 0, source.size)}, nil
}

type rarSourceFile struct{ *io.SectionReader }

func (rarSourceFile) Close() error { return nil }
func (file rarSourceFile) Stat() (fs.FileInfo, error) {
	return rarSourceInfo(file.Size()), nil
}

type rarSourceInfo int64

func (rarSourceInfo) Name() string       { return rarSourceName }
func (info rarSourceInfo) Size() int64   { return int64(info) }
func (rarSourceInfo) Mode() fs.FileMode  { return 0o444 }
func (rarSourceInfo) ModTime() time.Time { return time.Time{} }
func (rarSourceInfo) IsDir() bool        { return false }
func (rarSourceInfo) Sys() any           { return nil }
