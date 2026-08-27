package format

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
)

const (
	chmHeaderSize         = 0x60
	chmDirectoryHeaderMin = 0x54
	chmPMGLHeaderSize     = 0x14
	maxCHMSystemStream    = 1 << 20
)

func isCHM(r io.ReaderAt, size int64) bool {
	if size < 8 {
		return false
	}
	header := make([]byte, 8)
	if _, err := r.ReadAt(header, 0); err != nil {
		return false
	}
	if !bytes.Equal(header[:4], []byte("ITSF")) {
		return false
	}
	version := binary.LittleEndian.Uint32(header[4:8])
	return version >= 2 && version <= 4
}

// ExtractCHMMetadata reads the cheap title metadata available in the internal
// /#SYSTEM stream. It intentionally does not extract or decompress normal CHM
// content streams.
func ExtractCHMMetadata(r io.ReaderAt, size int64) (*Metadata, error) {
	stream, err := readCHMSystemStream(r, size)
	if err != nil || len(stream) == 0 {
		return &Metadata{}, err
	}
	return &Metadata{Title: chmSystemTitle(stream)}, nil
}

func readCHMSystemStream(r io.ReaderAt, size int64) ([]byte, error) {
	if size < chmHeaderSize {
		return nil, nil
	}
	header := make([]byte, chmHeaderSize)
	if _, err := r.ReadAt(header, 0); err != nil && err != io.EOF {
		return nil, err
	}
	if !bytes.Equal(header[:4], []byte("ITSF")) {
		return nil, nil
	}

	directoryOffset := int64(binary.LittleEndian.Uint64(header[0x48:0x50]))
	directoryLength := int64(binary.LittleEndian.Uint64(header[0x50:0x58]))
	contentOffset := int64(binary.LittleEndian.Uint64(header[0x58:0x60]))
	if directoryOffset <= 0 || directoryLength <= chmDirectoryHeaderMin || contentOffset <= 0 {
		return nil, nil
	}
	if directoryOffset > size || directoryLength > size-directoryOffset || contentOffset > size {
		return nil, nil
	}

	dirHeader := make([]byte, chmDirectoryHeaderMin)
	if _, err := r.ReadAt(dirHeader, directoryOffset); err != nil && err != io.EOF {
		return nil, err
	}
	if !bytes.Equal(dirHeader[:4], []byte("ITSP")) {
		return nil, nil
	}
	headerLength := int64(binary.LittleEndian.Uint32(dirHeader[8:12]))
	blockSize := int64(binary.LittleEndian.Uint32(dirHeader[16:20]))
	if headerLength < chmDirectoryHeaderMin || blockSize < chmPMGLHeaderSize || blockSize > 1<<20 {
		return nil, nil
	}

	chunksOffset := directoryOffset + headerLength
	directoryEnd := directoryOffset + directoryLength
	if chunksOffset > directoryEnd || directoryEnd > size {
		return nil, nil
	}
	blockCount := (directoryEnd - chunksOffset) / blockSize
	if blockCount <= 0 || blockCount > 1<<20 {
		return nil, nil
	}

	block := make([]byte, blockSize)
	for i := range blockCount {
		if _, err := r.ReadAt(block, chunksOffset+i*blockSize); err != nil && err != io.EOF {
			return nil, err
		}
		entry, ok := chmDirectoryEntry(block, "/#SYSTEM")
		if !ok {
			continue
		}
		if entry.Section != 0 || entry.Size == 0 || entry.Size > maxCHMSystemStream {
			return nil, nil
		}
		if entry.Offset > uint64(size) || entry.Size > uint64(size) || entry.Offset+entry.Size > uint64(size)-uint64(contentOffset) {
			return nil, nil
		}
		stream := make([]byte, entry.Size)
		if _, err := r.ReadAt(stream, contentOffset+int64(entry.Offset)); err != nil && err != io.EOF {
			return nil, err
		}
		return stream, nil
	}
	return nil, nil
}

type chmDirectoryEntryInfo struct {
	Section uint64
	Offset  uint64
	Size    uint64
}

func chmDirectoryEntry(block []byte, name string) (chmDirectoryEntryInfo, bool) {
	if len(block) < chmPMGLHeaderSize || !bytes.Equal(block[:4], []byte("PMGL")) {
		return chmDirectoryEntryInfo{}, false
	}
	freeSpace := int(binary.LittleEndian.Uint32(block[4:8]))
	entriesEnd := len(block)
	if freeSpace >= 0 && freeSpace < len(block)-chmPMGLHeaderSize {
		entriesEnd = len(block) - freeSpace
	}
	pos := chmPMGLHeaderSize
	for pos < entriesEnd {
		nameLen, n, ok := readCHMCWord(block[pos:entriesEnd])
		if !ok || nameLen == 0 {
			break
		}
		pos += n
		if nameLen > uint64(entriesEnd-pos) {
			break
		}
		entryName := string(block[pos : pos+int(nameLen)])
		pos += int(nameLen)

		section, n, ok := readCHMCWord(block[pos:entriesEnd])
		if !ok {
			break
		}
		pos += n
		offset, n, ok := readCHMCWord(block[pos:entriesEnd])
		if !ok {
			break
		}
		pos += n
		size, n, ok := readCHMCWord(block[pos:entriesEnd])
		if !ok {
			break
		}
		pos += n

		if entryName == name {
			return chmDirectoryEntryInfo{Section: section, Offset: offset, Size: size}, true
		}
	}
	return chmDirectoryEntryInfo{}, false
}

func readCHMCWord(b []byte) (uint64, int, bool) {
	var v uint64
	for i, c := range b {
		if i >= 10 || v > (1<<57) {
			return 0, 0, false
		}
		v = (v << 7) | uint64(c&0x7f)
		if c&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

func chmSystemTitle(stream []byte) string {
	if len(stream) < 4 {
		return ""
	}
	for pos := 4; pos+4 <= len(stream); {
		code := binary.LittleEndian.Uint16(stream[pos : pos+2])
		length := int(binary.LittleEndian.Uint16(stream[pos+2 : pos+4]))
		pos += 4
		if length < 0 || length > len(stream)-pos {
			break
		}
		if code == 3 {
			return chmSystemString(stream[pos : pos+length])
		}
		pos += length
	}
	return ""
}

func chmSystemString(b []byte) string {
	if idx := bytes.IndexByte(b, 0); idx >= 0 {
		b = b[:idx]
	}
	s := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, string(b))
	return cleanText(s)
}
