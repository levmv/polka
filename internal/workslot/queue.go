package workslot

import "context"

// Queue serializes one class of work inside a server process. It is deliberately
// tiny: callers still run work in their own goroutine/request, but only one
// caller at a time enters the protected critical section.
type Queue struct {
	slot chan struct{}
}

func New() *Queue {
	return &Queue{slot: make(chan struct{}, 1)}
}

func (q *Queue) Acquire(ctx context.Context) (func(), error) {
	select {
	case q.slot <- struct{}{}:
		return func() { <-q.slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
