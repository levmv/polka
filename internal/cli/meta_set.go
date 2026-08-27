package cli

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
)

type metaSetOptions struct {
	Title       trackedStringFlag
	SortTitle   trackedStringFlag
	Authors     trackedStringFlag
	Series      trackedStringFlag
	SeriesIndex trackedFloatFlag
	Tags        trackedStringFlag
	Description trackedStringFlag
	Publisher   trackedStringFlag
	Date        trackedStringFlag
	Language    trackedStringFlag
	Identifiers trackedStringFlag
}

type trackedStringFlag struct {
	Value string
	Seen  bool
}

func (f *trackedStringFlag) String() string {
	return f.Value
}

func (f *trackedStringFlag) Set(value string) error {
	f.Value = value
	f.Seen = true
	return nil
}

type trackedFloatFlag struct {
	Value float64
	Seen  bool
}

func (f *trackedFloatFlag) String() string {
	if !f.Seen {
		return ""
	}
	return strconv.FormatFloat(f.Value, 'f', -1, 64)
}

func (f *trackedFloatFlag) Set(value string) error {
	f.Seen = true
	value = strings.TrimSpace(value)
	if value == "" {
		f.Value = 0
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("invalid series index %q", value)
	}
	f.Value = parsed
	return nil
}

func runMetaSet(args []string) error {
	fs := commandFlagSet("meta set", "polka meta set <file> [--title <value>] [--authors <list>] [--series <value>] [--series-index <number>] [field flags]")
	var opts metaSetOptions
	fs.Var(&opts.Title, "title", "set title; empty clears")
	fs.Var(&opts.SortTitle, "sort-title", "set sort title; empty clears")
	fs.Var(&opts.Authors, "authors", "set authors as a semicolon-separated list; empty clears")
	fs.Var(&opts.Series, "series", "set series; empty clears")
	fs.Var(&opts.SeriesIndex, "series-index", "set series index; empty clears to 0")
	fs.Var(&opts.Tags, "tags", "set comma-separated tags; empty clears")
	fs.Var(&opts.Description, "description", "set description; empty clears")
	fs.Var(&opts.Publisher, "publisher", "set publisher; empty clears")
	fs.Var(&opts.Date, "date", "set published date; empty clears")
	fs.Var(&opts.Language, "language", "set language; empty clears")
	fs.Var(&opts.Identifiers, "identifiers", "set comma-separated identifiers; empty clears")
	if help, err := parseCommandFlags(fs, normalizeMetaSetArgs(args)); help || err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return reportedErrorf("usage: polka meta set <file> [field flags]")
	}
	if !opts.hasChanges() {
		fs.Usage()
		return reportedErrorf("meta set requires at least one field flag")
	}

	result, err := setMetaFile(fs.Arg(0), opts)
	if err != nil {
		return err
	}
	if result.Unchanged {
		fmt.Printf("Metadata already up to date: %s\n", result.Path)
	} else {
		fmt.Printf("Updated metadata: %s\n", result.Path)
	}
	return nil
}

func normalizeMetaSetArgs(args []string) []string {
	valueFlags := map[string]bool{
		"authors":      true,
		"date":         true,
		"description":  true,
		"identifiers":  true,
		"language":     true,
		"publisher":    true,
		"series":       true,
		"series-index": true,
		"sort-title":   true,
		"tags":         true,
		"title":        true,
	}
	var flags []string
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			files = append(files, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			files = append(files, arg)
			continue
		}
		flags = append(flags, arg)
		name, hasValue := metaSetFlagName(arg)
		if !valueFlags[name] || hasValue {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, files...)
}

func metaSetFlagName(arg string) (name string, hasValue bool) {
	arg = strings.TrimLeft(arg, "-")
	if idx := strings.IndexByte(arg, '='); idx >= 0 {
		return arg[:idx], true
	}
	return arg, false
}

func (opts metaSetOptions) hasChanges() bool {
	return opts.Title.Seen ||
		opts.SortTitle.Seen ||
		opts.Authors.Seen ||
		opts.Series.Seen ||
		opts.SeriesIndex.Seen ||
		opts.Tags.Seen ||
		opts.Description.Seen ||
		opts.Publisher.Seen ||
		opts.Date.Seen ||
		opts.Language.Seen ||
		opts.Identifiers.Seen
}

func (opts metaSetOptions) apply(meta *format.Metadata) {
	if opts.Title.Seen {
		meta.Title = strings.TrimSpace(opts.Title.Value)
	}
	if opts.SortTitle.Seen {
		meta.SortTitle = strings.TrimSpace(opts.SortTitle.Value)
	}
	if opts.Authors.Seen {
		meta.Authors = metaSetAuthors(opts.Authors.Value)
	}
	if opts.Series.Seen {
		meta.Series = strings.TrimSpace(opts.Series.Value)
	}
	if opts.SeriesIndex.Seen {
		meta.SeriesIndex = opts.SeriesIndex.Value
	}
	if opts.Tags.Seen {
		meta.Tags = bookmeta.ParseTagList(opts.Tags.Value)
	}
	if opts.Description.Seen {
		meta.Description = strings.TrimSpace(opts.Description.Value)
	}
	if opts.Publisher.Seen {
		meta.Publisher = strings.TrimSpace(opts.Publisher.Value)
	}
	if opts.Date.Seen {
		value := strings.TrimSpace(opts.Date.Value)
		if value != "" {
			if normalized, _ := bookmeta.ParseDate(value); normalized != "" {
				value = normalized
			}
		}
		meta.Date = value
	}
	if opts.Language.Seen {
		meta.Language = bookmeta.NormalizeLanguage(opts.Language.Value)
	}
	if opts.Identifiers.Seen {
		meta.Identifier = bookmeta.FormatIdentifiers(bookmeta.ParseIdentifiers(opts.Identifiers.Value))
	}
}

func metaSetAuthors(raw string) []bookmeta.AuthorMeta {
	names := bookmeta.ParseAuthorList(raw)
	authors := make([]bookmeta.AuthorMeta, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		authors = append(authors, bookmeta.AuthorMeta{
			Name:     name,
			SortName: bookmeta.AuthorSort(name),
		})
	}
	return authors
}

type metaSetResult struct {
	Path      string
	Unchanged bool
}

func setMetaFile(path string, opts metaSetOptions) (metaSetResult, error) {
	result := metaSetResult{Path: path}
	src, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return result, fmt.Errorf("stat source: %w", err)
	}
	if info.IsDir() {
		return result, fmt.Errorf("source is a directory: %s", path)
	}
	currentHash, err := sha256ReaderAt(src, info.Size())
	if err != nil {
		return result, fmt.Errorf("hash source: %w", err)
	}

	kind := format.DetectFormat(path, src, info.Size())
	if !format.SupportsMetadataWriteback(kind) {
		return result, fmt.Errorf("unsupported metadata write-back format: %s", format.FormatKey(kind))
	}

	meta, err := format.ExtractMetadata(src, info.Size(), kind)
	if err != nil {
		return result, fmt.Errorf("extract metadata: %w", err)
	}
	if meta == nil {
		meta = &format.Metadata{}
	}
	opts.apply(meta)

	tempPath, renderedHash, renderedSize, err := renderMetaSetTemp(path, info.Mode().Perm(), kind, src, info.Size(), *meta)
	if err != nil {
		return result, err
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := validateMetaSetTemp(tempPath, kind); err != nil {
		return result, fmt.Errorf("validate rendered metadata: %w", err)
	}
	latestInfo, err := os.Stat(path)
	if err != nil {
		return result, fmt.Errorf("stat source before replace: %w", err)
	}
	latestHash, err := sha256File(path)
	if err != nil {
		return result, fmt.Errorf("hash source before replace: %w", err)
	}
	if latestInfo.Size() != info.Size() || !bytes.Equal(latestHash[:], currentHash[:]) {
		return result, errors.New("source file changed during metadata write")
	}
	if renderedSize == info.Size() && bytes.Equal(renderedHash[:], currentHash[:]) {
		result.Unchanged = true
		return result, nil
	}

	cleanupTemp = false
	if err := replaceMetaSetFile(tempPath, path); err != nil {
		return result, fmt.Errorf("replace source with rendered metadata; temp kept at %s: %w", tempPath, err)
	}
	return result, nil
}

func renderMetaSetTemp(path string, mode os.FileMode, kind format.Format, src io.ReaderAt, size int64, meta format.Metadata) (string, [32]byte, int64, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".meta-*")
	if err != nil {
		return "", [32]byte{}, 0, fmt.Errorf("create temp output: %w", err)
	}
	tempPath := tmp.Name()
	cleanup := true
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()

	if mode == 0 {
		mode = 0o644
	}
	if err := tmp.Chmod(mode); err != nil {
		return "", [32]byte{}, 0, fmt.Errorf("chmod temp output: %w", err)
	}

	hasher := sha256.New()
	counter := &metaSetCountingWriter{}
	if err := renderMetaSet(io.MultiWriter(tmp, hasher, counter), kind, src, size, meta); err != nil {
		return "", [32]byte{}, 0, err
	}
	if err := tmp.Sync(); err != nil {
		return "", [32]byte{}, 0, fmt.Errorf("sync temp output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		closed = true
		return "", [32]byte{}, 0, fmt.Errorf("close temp output: %w", err)
	}
	closed = true

	cleanup = false
	var sum [32]byte
	copy(sum[:], hasher.Sum(nil))
	return tempPath, sum, counter.N, nil
}

func renderMetaSet(w io.Writer, kind format.Format, src io.ReaderAt, size int64, meta format.Metadata) error {
	switch {
	case format.IsEPUBContainerFormat(kind):
		modified := time.Now().UTC().Truncate(time.Second)
		return format.RewriteEPUBMetadataTo(w, src, size, meta, modified)
	case kind == format.FormatFB2:
		return format.RewriteFB2MetadataTo(w, src, size, meta)
	default:
		return fmt.Errorf("unsupported metadata write-back format: %s", format.FormatKey(kind))
	}
}

func validateMetaSetTemp(path string, kind format.Format) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open rendered file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat rendered file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("rendered path is a directory: %s", path)
	}
	meta, err := format.ExtractMetadata(f, info.Size(), kind)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("metadata not found in rendered %s", format.FormatKey(kind))
	}
	return nil
}

func sha256File(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func sha256ReaderAt(src io.ReaderAt, size int64) ([32]byte, error) {
	if size < 0 {
		return [32]byte{}, fmt.Errorf("negative size %d", size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(src, 0, size)); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func replaceMetaSetFile(tempPath, finalPath string) error {
	if err := os.Rename(tempPath, finalPath); err != nil {
		if _, statErr := os.Stat(finalPath); statErr == nil {
			if removeErr := os.Remove(finalPath); removeErr != nil {
				return fmt.Errorf("move temp output: %w", err)
			}
			if retryErr := os.Rename(tempPath, finalPath); retryErr != nil {
				return fmt.Errorf("move temp output: %w", retryErr)
			}
			return nil
		}
		return fmt.Errorf("move temp output: %w", err)
	}
	return nil
}

type metaSetCountingWriter struct {
	N int64
}

func (w *metaSetCountingWriter) Write(p []byte) (int, error) {
	w.N += int64(len(p))
	return len(p), nil
}
