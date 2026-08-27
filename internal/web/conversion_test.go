package web

import (
	"context"
	"errors"
	"testing"
)

func TestConversionGateHonorsWaitingContext(t *testing.T) {
	s := &Server{conversionSlots: make(chan struct{}, 1)}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withConversionSlot(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := s.withConversionSlot(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting conversion error = %v; want context.Canceled", err)
	}
	if called {
		t.Fatal("conversion callback ran without an available slot")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first conversion error = %v", err)
	}

	called = false
	err = s.withConversionSlot(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("already-canceled conversion = %v, called = %v; want context.Canceled without callback", err, called)
	}
}
