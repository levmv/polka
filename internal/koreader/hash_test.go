package koreader

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"testing"
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

	got, err := PartialMD5(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PartialMD5: %v", err)
	}

	h := md5.New()
	h.Write(data[0:1024])
	h.Write(data[1024:2048])
	h.Write(data[4096:5000])
	want := hex.EncodeToString(h.Sum(nil))
	if got != want {
		t.Fatalf("PartialMD5 = %q; want %q", got, want)
	}
}
