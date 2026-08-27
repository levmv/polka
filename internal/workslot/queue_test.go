package workslot

import (
	"context"
	"errors"
	"testing"
)

func TestAcquireRespectsCanceledContextWhileQueued(t *testing.T) {
	q := New()
	release, err := q.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Acquire error = %v; want context.Canceled", err)
	}
}
