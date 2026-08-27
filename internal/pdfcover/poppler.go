package pdfcover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	popplerProbeTimeout = 2 * time.Second
	maxDiagnosticBytes  = 64 << 10
)

type externalCommand func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

func externalCommandContext(
	ctx context.Context,
	executable string,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = workerStopTimeout
	return cmd.Run()
}

func detectPoppler(command externalCommand, lookPath func(string) (string, error)) (BackendInfo, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	executable, err := lookPath("pdftoppm")
	if err != nil {
		return BackendInfo{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return BackendInfo{}, fmt.Errorf("resolve pdftoppm path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), popplerProbeTimeout)
	defer cancel()

	var stdout, stderr limitedBuffer
	stdout.limit = maxDiagnosticBytes
	stderr.limit = maxDiagnosticBytes
	if err := command(ctx, executable, []string{"-v"}, nil, &stdout, &stderr); err != nil {
		return BackendInfo{}, fmt.Errorf("probe pdftoppm: %w", err)
	}
	version := firstDiagnosticLine(stdout.Bytes(), stderr.Bytes())
	if !strings.HasPrefix(strings.ToLower(version), "pdftoppm version ") {
		return BackendInfo{}, fmt.Errorf("probe pdftoppm: unexpected version output %q", version)
	}
	return BackendInfo{
		Backend:    BackendPoppler,
		Version:    version,
		Executable: executable,
	}, nil
}

func (r *Renderer) renderPoppler(ctx context.Context, pdf io.Reader, size int64, dpi int) ([]byte, error) {
	var stdout, stderr limitedBuffer
	stdout.limit = maxRenderedCoverBytes
	stderr.limit = maxDiagnosticBytes
	args := []string{
		"-f", "1",
		"-l", "1",
		"-singlefile",
		"-r", strconv.Itoa(dpi),
		"-jpeg",
		"-jpegopt", "quality=90",
		"-",
	}
	err := r.command(ctx, r.backend.Executable, args, io.LimitReader(pdf, size), &stdout, &stderr)
	if ctx.Err() != nil {
		return nil, renderContextError(ctx, "render PDF page 1 with pdftoppm", r.renderTimeout)
	}
	if err != nil {
		diagnostic := oneLineDiagnostic(stderr.Bytes())
		if diagnostic == "" {
			return nil, fmt.Errorf("render PDF page 1 with pdftoppm: %w", err)
		}
		return nil, fmt.Errorf("render PDF page 1 with pdftoppm: %w: %s", err, diagnostic)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("pdftoppm output exceeds %d bytes", maxRenderedCoverBytes)
	}
	if stdout.Len() == 0 {
		return nil, errors.New("pdftoppm returned no image bytes")
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := min(b.limit-b.Len(), len(p))
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:remaining])
	}
	if remaining < len(p) {
		b.exceeded = true
	}
	return n, nil
}

func firstDiagnosticLine(outputs ...[]byte) string {
	for _, output := range outputs {
		for line := range strings.SplitSeq(string(output), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return line
			}
		}
	}
	return ""
}

func oneLineDiagnostic(data []byte) string {
	return strings.Join(strings.Fields(string(data)), " ")
}
