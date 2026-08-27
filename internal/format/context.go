package format

import (
	"context"
	"io"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := checkContext(r.ctx); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
