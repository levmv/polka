package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func fileSHA256(path string) (string, error) {
	hash, _, err := fileSHA256AndSize(path)
	return hash, err
}

func fileSHA256Context(ctx context.Context, path string) (string, error) {
	hash, _, err := fileSHA256AndSizeContext(ctx, path)
	return hash, err
}

func fileSHA256AndSize(path string) (string, int64, error) {
	return fileSHA256AndSizeContext(context.Background(), path)
}

func fileSHA256AndSizeContext(ctx context.Context, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("stat file: %w", err)
	}

	h := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return "", 0, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, fmt.Errorf("hash file: %w", readErr)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), info.Size(), nil
}
