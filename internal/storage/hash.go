package storage

import (
	"context"
	"crypto/sha256"
	"hash"
	"io"
)

type contentHasher struct {
	hash.Hash
}

// NewHasher returns a hasher for content identity: the first 16 bytes of SHA-256.
func NewHasher() hash.Hash {
	return &contentHasher{Hash: sha256.New()}
}

// Sum returns the content hash of data, using the same truncation as NewHasher.
func Sum(data []byte) [16]byte {
	full := sha256.Sum256(data)
	var truncated [16]byte
	copy(truncated[:], full[:16])
	return truncated
}

func (h *contentHasher) Size() int {
	return 16
}

func (h *contentHasher) Sum(dst []byte) []byte {
	full := h.Hash.Sum(nil)
	return append(dst, full[:16]...)
}

// HashReader hashes the remaining bytes in r, checking ctx between reads.
func HashReader(ctx context.Context, r io.Reader) ([]byte, error) {
	h := NewHasher()
	buf := make([]byte, 128<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			return h.Sum(nil), nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}
