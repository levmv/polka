package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

func fileSHA256Context(ctx context.Context, path string) ([]byte, error) {
	hash, _, err := fileSHA256AndSizeContext(ctx, path)
	return hash, err
}

func fileSHA256AndSizeContext(ctx context.Context, path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat file: %w", err)
	}

	h := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, 0, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, 0, fmt.Errorf("hash file: %w", readErr)
		}
	}
	return h.Sum(nil), info.Size(), nil
}
