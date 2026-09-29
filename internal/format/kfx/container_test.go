package kfx

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

func TestContainerFailures(t *testing.T) {
	good := testfixture.KFX(t, false)
	badRange := bytes.Clone(good)
	binary.LittleEndian.PutUint64(badRange[18+8:], ^uint64(0))
	badBundle := testfixture.KFX(t, true)
	zr, err := zip.NewReader(bytes.NewReader(badBundle), int64(len(badBundle)))
	if err != nil {
		t.Fatal(err)
	}
	offset, err := zr.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	badBundle[offset] = 7 // Reserved DEFLATE block type.
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated header", good[:12]},
		{"truncated entity", good[:len(good)-1]},
		{"overflowed range", badRange},
		{"corrupt bundle compression", badBundle},
		{"protected", append(bytes.Clone(drmSignature), make([]byte, 32)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExtractDocument(context.Background(), bytes.NewReader(tc.data), int64(len(tc.data)), nil)
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	want := errors.New("storage unavailable")
	_, err = ExtractDocument(context.Background(), brokenReader{want}, int64(len(good)), nil)
	if !errors.Is(err, want) {
		t.Fatalf("IO error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ExtractDocument(ctx, bytes.NewReader(good), int64(len(good)), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

type brokenReader struct{ err error }

func (r brokenReader) ReadAt([]byte, int64) (int, error) { return 0, r.err }

func TestIonBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		limit int
		want  error
	}{
		{"truncated string", []byte{0x8e, 0xff, 'x'}, 100, ErrUnsupported},
		{"variable integer overflow", bytes.Repeat([]byte{0x7f}, 20), 100, ErrUnsupported},
		{"annotation without value", []byte{0xe3, 0x82, 0x81, 0x81}, 100, ErrUnsupported},
		{"value limit", []byte{0xb2, 0x20, 0x20}, 2, ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ionDecoder{ctx: context.Background(), remaining: tc.limit}
			_, err := d.decode(tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	d := ionDecoder{ctx: context.Background(), remaining: 10}
	v, err := d.decode([]byte{0xb2, 0x8f, 0x80})
	if err != nil || len(v.list) != 2 || v.list[0].kind != 15 {
		t.Fatalf("null changes list indexes: %+v %v", v, err)
	}
}

func FuzzContainer(f *testing.F) {
	f.Add(testfixture.KFX(f, false))
	f.Add(testfixture.KFX(f, true))
	f.Add([]byte("CONT\x02\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = ExtractDocument(context.Background(), bytes.NewReader(data), int64(len(data)), nil)
	})
}
