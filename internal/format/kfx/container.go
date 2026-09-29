// Package kfx reads unencrypted KFX book containers and explicit ZIP bundles.
// Container names never grant access to neighboring files on the host.
package kfx

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var (
	ErrUnsupported = errors.New("unsupported or damaged KFX")
	ErrLimit       = errors.New("KFX resource limit exceeded")
)

const (
	maxHeaderBytes   = 16 << 20
	maxFragmentBytes = 32 << 20
	maxBundleBytes   = 256 << 20
	maxDecodedBytes  = 256 << 20
	maxFragments     = 100000
)

var drmSignature = []byte{0xea, 'D', 'R', 'M', 'I', 'O', 'N', 0xee}

func invalid(message string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(message, args...))
}

func isSignature(data []byte) bool {
	return bytes.HasPrefix(data, []byte("CONT")) || bytes.HasPrefix(data, drmSignature)
}

// Is recognizes the container signature, including protected containers.
// It does not preflight whether the book can be converted.
func Is(r io.ReaderAt, size int64) bool {
	var signature [8]byte
	n, _ := r.ReadAt(signature[:], 0)
	if isSignature(signature[:n]) {
		return true
	}
	if !bytes.HasPrefix(signature[:n], []byte("PK\x03\x04")) {
		return false
	}
	zr, err := zip.NewReader(r, size)
	if err != nil || len(zr.File) > 4096 {
		return false
	}
	for _, f := range zr.File {
		if !containerFilename(f.Name) {
			continue
		}
		reader, err := f.Open()
		if err != nil {
			continue
		}
		n, _ := io.ReadFull(reader, signature[:])
		reader.Close()
		if isSignature(signature[:n]) {
			return true
		}
	}
	return false
}

func containerFilename(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".kfx", ".azw", ".res", ".azw8", ".azw9":
		return true
	}
	return false
}

type fragmentKey struct {
	kind int
	id   string
}
type fragment struct {
	key          fragmentKey
	numericID    uint64
	source       io.ReaderAt
	offset, size int64
	value        value
	loaded       bool
}

type book struct {
	ctx       context.Context
	fragments map[fragmentKey]*fragment
	ordered   []*fragment
	decoder   ionDecoder
	readBytes int64
	symbols   symbols
}

func openBook(ctx context.Context, r io.ReaderAt, size int64) (*book, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := &book{ctx: ctx, fragments: make(map[fragmentKey]*fragment), decoder: ionDecoder{ctx: ctx, remaining: 1_000_000}}
	var signature [8]byte
	n, err := r.ReadAt(signature[:], 0)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if bytes.HasPrefix(signature[:n], []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return nil, bundleError(err)
		}
		if len(zr.File) > 4096 {
			return nil, fmt.Errorf("%w: too many bundle members", ErrLimit)
		}
		var total uint64
		for _, f := range zr.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if f.FileInfo().IsDir() || !containerFilename(f.Name) {
				continue
			}
			if f.UncompressedSize64 > maxBundleBytes-total {
				return nil, fmt.Errorf("%w: KFX bundle is too large", ErrLimit)
			}
			reader, err := f.Open()
			if err != nil {
				return nil, bundleError(err)
			}
			data, err := io.ReadAll(io.LimitReader(contextReader{ctx, reader}, int64(maxBundleBytes-total)+1))
			reader.Close()
			if err != nil {
				return nil, bundleError(err)
			}
			total += uint64(len(data))
			if total > maxBundleBytes {
				return nil, fmt.Errorf("%w: KFX bundle is too large", ErrLimit)
			}
			if !isSignature(data) {
				continue
			}
			if err := b.addContainer(bytes.NewReader(data), int64(len(data))); err != nil {
				return nil, err
			}
		}
		if len(b.ordered) == 0 {
			return nil, invalid("ZIP bundle contains no KFX containers")
		}
	} else if err := b.addContainer(r, size); err != nil {
		return nil, err
	}
	if err := b.indexFragments(); err != nil {
		return nil, err
	}
	return b, nil
}

func bundleError(err error) error {
	var corrupt flate.CorruptInputError
	if errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrAlgorithm) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &corrupt) {
		return invalid("invalid ZIP bundle: %v", err)
	}
	return err
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (b *book) read(r io.ReaderAt, size, offset, length, limit int64) ([]byte, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset > size || length > size-offset {
		return nil, invalid("container range is out of bounds")
	}
	if length > limit || length > maxDecodedBytes-b.readBytes {
		return nil, fmt.Errorf("%w: container data is too large", ErrLimit)
	}
	b.readBytes += length
	data := make([]byte, length)
	n, err := r.ReadAt(data, offset)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if n != len(data) {
		return nil, invalid("truncated container data")
	}
	return data, nil
}

func (b *book) addContainer(r io.ReaderAt, size int64) error {
	header, err := b.read(r, size, 0, min(size, 18), 18)
	if err != nil {
		return err
	}
	if bytes.HasPrefix(header, drmSignature) {
		return invalid("book is protected; an unencrypted KFX file is required")
	}
	if len(header) != 18 || string(header[:4]) != "CONT" {
		return invalid("invalid container header")
	}
	version := binary.LittleEndian.Uint16(header[4:])
	if version != 1 && version != 2 {
		return invalid("container version %d", version)
	}
	headerSize := int64(binary.LittleEndian.Uint32(header[6:]))
	if headerSize < 18 || headerSize > size {
		return invalid("invalid container header size")
	}
	if headerSize > maxHeaderBytes {
		return fmt.Errorf("%w: container header is too large", ErrLimit)
	}
	infoOffset := int64(binary.LittleEndian.Uint32(header[10:]))
	infoSize := int64(binary.LittleEndian.Uint32(header[14:]))
	raw, err := b.read(r, headerSize, infoOffset, infoSize, maxHeaderBytes)
	if err != nil {
		return err
	}
	b.decoder.symbols = nil
	info, err := b.decoder.decode(raw)
	if err != nil {
		return err
	}
	if err := checkEncoding(info); err != nil {
		return err
	}
	var names symbols
	if info.get(416).integer > 0 {
		raw, err := b.read(r, headerSize, info.get(415).integer, info.get(416).integer, maxHeaderBytes)
		if err != nil {
			return err
		}
		table, err := b.decoder.decode(raw)
		if err != nil {
			return err
		}
		names, err = documentSymbols(table)
		if err != nil {
			return err
		}
		if b.symbols == nil {
			b.symbols = make(symbols)
		}
		for id, name := range names {
			if existing, ok := b.symbols[id]; ok && existing != name {
				return invalid("conflicting document symbol tables in bundle")
			}
			b.symbols[id] = name
		}
	}
	index, err := b.read(r, headerSize, info.get(413).integer, info.get(414).integer, maxHeaderBytes)
	if err != nil {
		return err
	}
	if len(index)%24 != 0 {
		return invalid("invalid entity index length")
	}
	if len(index)/24 > maxFragments-len(b.ordered) {
		return fmt.Errorf("%w: too many fragments", ErrLimit)
	}
	for len(index) > 0 {
		id := uint64(binary.LittleEndian.Uint32(index))
		kind := int(binary.LittleEndian.Uint32(index[4:]))
		offset, length := binary.LittleEndian.Uint64(index[8:]), binary.LittleEndian.Uint64(index[16:])
		index = index[24:]
		if offset > uint64(size-headerSize) || length > uint64(size-headerSize)-offset {
			return invalid("entity range is out of bounds")
		}
		key := fragmentKey{kind: kind}
		f := &fragment{key: key, numericID: id, source: r, offset: headerSize + int64(offset), size: int64(length)}
		b.ordered = append(b.ordered, f)
	}
	return nil
}

func (b *book) indexFragments() error {
	ordered := b.ordered
	b.ordered = nil
	for _, f := range ordered {
		// A delivery bundle can carry its book-wide symbol table in a separate
		// metadata container. Archive member order has no semantic meaning.
		f.key.id = b.symbols.name(f.numericID)
		// Font declarations are repeated roots, not addressable fragments.
		// Distinct faces legitimately share the container's root ID ($348).
		if f.key.kind == fragmentFont {
			b.ordered = append(b.ordered, f)
			continue
		}
		if previous := b.fragments[f.key]; previous != nil {
			a, err := b.payload(previous)
			if err != nil {
				return err
			}
			data, err := b.payload(f)
			if err != nil {
				return err
			}
			if !bytes.Equal(a, data) {
				return invalid("conflicting fragment %d/%s in bundle", f.key.kind, f.key.id)
			}
			continue
		}
		b.fragments[f.key] = f
		b.ordered = append(b.ordered, f)
	}
	return nil
}

func documentSymbols(table value) (symbols, error) {
	base := int64(9)
	for _, imp := range table.named("imports").list {
		if imp.named("name").text != "YJ_symbols" {
			return nil, invalid("unknown shared symbol table")
		}
		// KFX's import count includes the nine Ion system symbols.
		base = imp.named("max_id").integer
	}
	if base < 9 || base > 1_000_000 {
		return nil, invalid("invalid symbol table range")
	}
	names := make(symbols, len(table.named("symbols").list))
	for i, v := range table.named("symbols").list {
		names[uint64(base)+uint64(i)+1] = v.text
	}
	return names, nil
}

func checkEncoding(info value) error {
	if info.get(411).integer != 0 {
		return invalid("book is protected; an unencrypted KFX file is required")
	}
	if info.get(410).integer != 0 {
		return invalid("unsupported container compression %d", info.get(410).integer)
	}
	return nil
}

func (b *book) payload(f *fragment) ([]byte, error) {
	header, err := b.read(f.source, f.offset+f.size, f.offset, 10, 10)
	if err != nil {
		return nil, err
	}
	if string(header[:4]) != "ENTY" {
		return nil, invalid("invalid entity header")
	}
	version := binary.LittleEndian.Uint16(header[4:])
	if version != 1 {
		return nil, invalid("entity version %d", version)
	}
	headerSize := int64(binary.LittleEndian.Uint32(header[6:]))
	if headerSize < 10 || headerSize > f.size {
		return nil, invalid("invalid entity header size")
	}
	raw, err := b.read(f.source, f.offset+f.size, f.offset+10, headerSize-10, maxHeaderBytes)
	if err != nil {
		return nil, err
	}
	b.decoder.symbols = b.symbols
	info, err := b.decoder.decode(raw)
	if err != nil {
		return nil, err
	}
	if err := checkEncoding(info); err != nil {
		return nil, err
	}
	return b.read(f.source, f.offset+f.size, f.offset+headerSize, f.size-headerSize, maxFragmentBytes)
}

func (b *book) load(f *fragment) (value, error) {
	if f.loaded {
		return f.value, nil
	}
	raw, err := b.payload(f)
	if err != nil {
		return value{}, err
	}
	b.decoder.symbols = b.symbols
	v, err := b.decoder.decode(raw)
	if err != nil {
		return value{}, fmt.Errorf("fragment %d/%s: %w", f.key.kind, f.key.id, err)
	}
	f.value, f.loaded = v, true
	return v, nil
}

func (b *book) find(kind int, id string) (value, error) {
	if f := b.fragments[fragmentKey{kind, id}]; f != nil {
		return b.load(f)
	}
	return value{}, invalid("missing fragment %d/%s; supply the complete book bundle", kind, id)
}

func (b *book) singleton(kind int) (value, error) {
	for _, f := range b.ordered {
		if f.key.kind == kind {
			return b.load(f)
		}
	}
	return value{}, nil
}

func (b *book) resolve(kind int, v value) (value, error) {
	if v.kind == ionSymbol || v.kind == ionString {
		return b.find(kind, v.text)
	}
	return v, nil
}
