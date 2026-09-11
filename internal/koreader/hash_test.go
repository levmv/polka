package koreader

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestPartialMD5SmallFile(t *testing.T) {
	got, err := PartialMD5(bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("PartialMD5: %v", err)
	}
	sum := md5.Sum([]byte("hello"))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("PartialMD5 = %q; want %q", got, want)
	}
}

func TestPartialMD5SamplesKOReaderOffsets(t *testing.T) {
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i % 251)
	}

	h := md5.New()
	h.Write(data[0:1024])
	h.Write(data[1024:2048])
	h.Write(data[4096:5000])
	want := hex.EncodeToString(h.Sum(nil))
	for _, shortReads := range []bool{false, true} {
		r := bytes.NewReader(data)
		var source io.ReadSeeker = r
		if shortReads {
			source = struct {
				io.Reader
				io.Seeker
			}{iotest.OneByteReader(r), r}
		}
		got, err := PartialMD5(source)
		if err != nil || got != want {
			t.Fatalf("shortReads=%v: PartialMD5 = %q, %v; want %q", shortReads, got, err, want)
		}
	}
}

func TestPartialMD5ReportsReadFailure(t *testing.T) {
	readErr := errors.New("storage unavailable")
	source := struct {
		io.Reader
		io.Seeker
	}{iotest.ErrReader(readErr), bytes.NewReader(nil)}
	if _, err := PartialMD5(source); !errors.Is(err, readErr) {
		t.Fatalf("PartialMD5 error = %v; want %v", err, readErr)
	}
}
