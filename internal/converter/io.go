package converter

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const (
	maxConverterDecodedInputBytes int64 = 128 << 20
	maxConverterPackageBytes      int64 = 8 << 20
	maxConverterMetadataBytes     int64 = 2 << 20
	maxConverterResourceBytes     int64 = 32 << 20

	maxConversionDecodedBytes  int64 = 512 << 20
	maxConversionResourceCount       = 4096
	maxConversionOutputBytes   int64 = 256 << 20
)

var ErrInputTooLarge = errors.New("conversion input exceeds size limit")

// ErrResourceLimit marks an aggregate conversion budget failure. Individual
// inputs may be valid while their combined decoded resources or generated
// output would consume too much process memory, CPU, or temporary storage.
var ErrResourceLimit = errors.New("conversion resource limit exceeded")

type conversionLimits struct {
	decodedBytes int64
	resources    int
	outputBytes  int64
}

var defaultConversionLimits = conversionLimits{
	decodedBytes: maxConversionDecodedBytes,
	resources:    maxConversionResourceCount,
	outputBytes:  maxConversionOutputBytes,
}

type conversionBudget struct {
	limits       conversionLimits
	decodedBytes int64
	resources    int
}

type conversionBudgetKey struct{}

func withConversionBudget(ctx context.Context, budget *conversionBudget) context.Context {
	return context.WithValue(ctx, conversionBudgetKey{}, budget)
}

func conversionBudgetFromContext(ctx context.Context) *conversionBudget {
	budget, _ := ctx.Value(conversionBudgetKey{}).(*conversionBudget)
	return budget
}

func (b *conversionBudget) remainingDecodedBytes() int64 {
	if b == nil {
		return -1
	}
	return max(b.limits.decodedBytes-b.decodedBytes, 0)
}

func (b *conversionBudget) addDecodedBytes(n int64, label string) error {
	if b == nil || n <= 0 {
		return nil
	}
	if n > b.remainingDecodedBytes() {
		return fmt.Errorf("%s exceeds aggregate decoded-data limit (%d bytes): %w", label, b.limits.decodedBytes, ErrResourceLimit)
	}
	b.decodedBytes += n
	return nil
}

func claimConversionDecodedBytes(ctx context.Context, n int64, label string) error {
	return conversionBudgetFromContext(ctx).addDecodedBytes(n, label)
}

func claimConversionResources(ctx context.Context, n int, label string) error {
	budget := conversionBudgetFromContext(ctx)
	if budget == nil || n <= 0 {
		return nil
	}
	if n > budget.limits.resources-budget.resources {
		return fmt.Errorf("%s exceeds aggregate resource-count limit (%d): %w", label, budget.limits.resources, ErrResourceLimit)
	}
	budget.resources += n
	return nil
}

type conversionOutputWriter struct {
	w        io.Writer
	maxBytes int64
	written  int64
}

func (w *conversionOutputWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > max(w.maxBytes-w.written, 0) {
		return 0, fmt.Errorf("generated output exceeds conversion limit (%d bytes): %w", w.maxBytes, ErrResourceLimit)
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

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

type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := checkContext(r.ctx); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, offset)
}

type contextWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := checkContext(w.ctx); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func readSectionContextLimited(ctx context.Context, src io.ReaderAt, size int64, maxBytes int64, label string) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("%s size is invalid", label)
	}
	if maxBytes >= 0 && size > maxBytes {
		return nil, fmt.Errorf("%s exceeds conversion input limit (%d bytes): %w", label, maxBytes, ErrInputTooLarge)
	}
	return readAllContextLimited(ctx, io.NewSectionReader(src, 0, size), maxBytes, label)
}

func readAllContextLimited(ctx context.Context, r io.Reader, maxBytes int64, label string) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	budget := conversionBudgetFromContext(ctx)
	aggregateLimited := false
	if remaining := budget.remainingDecodedBytes(); remaining >= 0 && (maxBytes < 0 || remaining < maxBytes) {
		maxBytes = remaining
		aggregateLimited = true
	}
	reader := io.Reader(contextReader{ctx: ctx, r: r})
	if maxBytes >= 0 {
		reader = io.LimitReader(reader, maxBytes+1)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if maxBytes >= 0 && int64(len(raw)) > maxBytes {
		if aggregateLimited {
			return nil, fmt.Errorf("%s exceeds aggregate decoded-data limit (%d bytes): %w", label, budget.limits.decodedBytes, ErrResourceLimit)
		}
		return nil, fmt.Errorf("%s exceeds conversion input limit (%d bytes): %w", label, maxBytes, ErrInputTooLarge)
	}
	if err := budget.addDecodedBytes(int64(len(raw)), label); err != nil {
		return nil, err
	}
	return raw, nil
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if err := claimConversionDecodedBytes(ctx, int64(n), "streamed conversion resource"); err != nil {
				return err
			}
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
