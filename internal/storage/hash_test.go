package storage

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestContentHash(t *testing.T) {
	got, err := HashReader(t.Context(), strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 || hex.EncodeToString(got) != "ba7816bf8f01cfea414140de5dae2223" {
		t.Fatalf("hash = %x; want first 16 SHA-256 bytes", got)
	}
	if sum := Sum([]byte("abc")); sum != [16]byte(got) {
		t.Fatalf("Sum = %x; want %x", sum, got)
	}
}
