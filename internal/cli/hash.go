package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/levmv/polka/internal/storage"
)

func fileHashContext(ctx context.Context, path string) ([]byte, error) {
	hash, _, err := fileHashAndSizeContext(ctx, path)
	return hash, err
}

func fileHashAndSizeContext(ctx context.Context, path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat file: %w", err)
	}

	hash, err := storage.HashReader(ctx, f)
	if err != nil {
		return nil, 0, fmt.Errorf("hash file: %w", err)
	}
	return hash, info.Size(), nil
}
