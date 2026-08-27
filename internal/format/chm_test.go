package format

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestDetectFormatCHM(t *testing.T) {
	data := testfixture.MinimalCHM(3)
	r := bytes.NewReader(data)
	if got := DetectFormat("manual.chm", r, r.Size()); got != FormatCHM {
		t.Fatalf("DetectFormat = %v; want FormatCHM", got)
	}
}

func TestDetectFormatCHMRejectsExtensionOnlyFiles(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "manual.chm", data: []byte("not a chm")},
		{name: "manual.chm", data: testfixture.MinimalCHM(1)},
		{name: "manual.txt", data: testfixture.MinimalCHM(3)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader(tt.data)
			if got := DetectFormat(tt.name, r, r.Size()); got != FormatUnknown {
				t.Fatalf("DetectFormat = %v; want FormatUnknown", got)
			}
		})
	}
}

func TestExtractCHMMetadataTitle(t *testing.T) {
	data := testCHMBytesWithSystemTitle("Oracle PL/SQL by Example, Third Edition")
	r := bytes.NewReader(data)
	meta, err := ExtractCHMMetadata(r, r.Size())
	if err != nil {
		t.Fatalf("ExtractCHMMetadata: %v", err)
	}
	if meta == nil || meta.Title != "Oracle PL/SQL by Example, Third Edition" {
		t.Fatalf("Metadata = %+v; want CHM title", meta)
	}
}

func testCHMBytesWithSystemTitle(title string) []byte {
	const (
		directoryOffset = 0x78
		dirHeaderLen    = 0x54
		blockSize       = 0x1000
		chunksOffset    = directoryOffset + dirHeaderLen
		directoryLength = dirHeaderLen + blockSize
		contentOffset   = directoryOffset + directoryLength
	)

	system := testCHMSystemStream(title)
	data := make([]byte, contentOffset+len(system))
	copy(data[:4], "ITSF")
	binary.LittleEndian.PutUint32(data[4:8], 3)
	binary.LittleEndian.PutUint32(data[8:12], chmHeaderSize)
	binary.LittleEndian.PutUint64(data[0x48:0x50], directoryOffset)
	binary.LittleEndian.PutUint64(data[0x50:0x58], directoryLength)
	binary.LittleEndian.PutUint64(data[0x58:0x60], contentOffset)

	copy(data[directoryOffset:directoryOffset+4], "ITSP")
	binary.LittleEndian.PutUint32(data[directoryOffset+4:directoryOffset+8], 1)
	binary.LittleEndian.PutUint32(data[directoryOffset+8:directoryOffset+12], dirHeaderLen)
	binary.LittleEndian.PutUint32(data[directoryOffset+16:directoryOffset+20], blockSize)
	binary.LittleEndian.PutUint32(data[directoryOffset+32:directoryOffset+36], 0)
	binary.LittleEndian.PutUint32(data[directoryOffset+36:directoryOffset+40], 0)
	binary.LittleEndian.PutUint32(data[directoryOffset+44:directoryOffset+48], 1)

	block := data[chunksOffset : chunksOffset+blockSize]
	copy(block[:4], "PMGL")
	binary.LittleEndian.PutUint32(block[12:16], 0xffffffff)
	binary.LittleEndian.PutUint32(block[16:20], 0xffffffff)
	entry := testCHMDirectoryEntry("/#SYSTEM", 0, 0, uint64(len(system)))
	copy(block[chmPMGLHeaderSize:], entry)
	binary.LittleEndian.PutUint32(block[4:8], uint32(blockSize-chmPMGLHeaderSize-len(entry)))

	copy(data[contentOffset:], system)
	return data
}

func testCHMSystemStream(title string) []byte {
	value := append([]byte(title), 0)
	stream := make([]byte, 4+4+len(value))
	binary.LittleEndian.PutUint32(stream[:4], 3)
	binary.LittleEndian.PutUint16(stream[4:6], 3)
	binary.LittleEndian.PutUint16(stream[6:8], uint16(len(value)))
	copy(stream[8:], value)
	return stream
}

func testCHMDirectoryEntry(name string, section, offset, size uint64) []byte {
	var out []byte
	out = append(out, testCHMCWord(uint64(len(name)))...)
	out = append(out, name...)
	out = append(out, testCHMCWord(section)...)
	out = append(out, testCHMCWord(offset)...)
	out = append(out, testCHMCWord(size)...)
	return out
}

func testCHMCWord(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var rev []byte
	for v > 0 {
		rev = append(rev, byte(v&0x7f))
		v >>= 7
	}
	out := make([]byte, len(rev))
	for i := range rev {
		b := rev[len(rev)-1-i]
		if i < len(rev)-1 {
			b |= 0x80
		}
		out[i] = b
	}
	return out
}
