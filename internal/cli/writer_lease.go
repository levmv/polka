package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/levmv/polka/internal/db"
)

var errCLIWriterLeaseStopped = errors.New("CLI writer lease stopped")

type cliWriterLease struct {
	lease  *db.WriterLease
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   <-chan struct{}
}

func acquireCLIWriterLease(ctx context.Context, database *db.DB, command string, force bool) (*cliWriterLease, error) {
	lease, err := db.AcquireWriterLease(ctx, database, db.NewWriterLeaseOwner(command), force)
	if err != nil {
		if errors.Is(err, db.ErrWriterLeaseHeld) {
			return nil, fmt.Errorf("%v; retry later or rerun with --force", err)
		}
		return nil, err
	}
	return superviseCLIWriterLease(ctx, lease, db.DefaultWriterLeaseTTL/3), nil
}

func superviseCLIWriterLease(parent context.Context, lease *db.WriterLease, interval time.Duration) *cliWriterLease {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := lease.RunHeartbeat(ctx, interval); err != nil {
			cancel(fmt.Errorf("writer lease lost; another process may be changing managed files; rerun this command after it finishes: %w", err))
		}
	}()
	return &cliWriterLease{lease: lease, ctx: ctx, cancel: cancel, done: done}
}

func (l *cliWriterLease) Context() context.Context {
	return l.ctx
}

func (l *cliWriterLease) finish(commandErr error) error {
	l.cancel(errCLIWriterLeaseStopped)
	<-l.done
	cause := context.Cause(l.ctx)
	if errors.Is(cause, errCLIWriterLeaseStopped) {
		cause = nil
	}
	if cause != nil {
		switch {
		case errors.Is(commandErr, context.Canceled):
			commandErr = nil
		case errors.Is(commandErr, cause):
			cause = nil
		}
	}
	releaseErr := l.lease.Release(context.Background())
	return errors.Join(commandErr, cause, releaseErr)
}
