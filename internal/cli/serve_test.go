package cli

import (
	"context"
	"testing"
)

func TestRunServeTreatsCancellationAsCleanStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runServe(ctx, t.TempDir(), nil); err != nil {
		t.Fatalf("runServe canceled = %v, want clean stop", err)
	}
}
