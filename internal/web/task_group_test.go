package web

import (
	"context"
	"testing"
)

func TestTaskGroupStopCancelsJoinsAndRejectsNewWork(t *testing.T) {
	tasks := newTaskGroup(context.Background())
	started := make(chan struct{})
	exited := make(chan struct{})
	if !tasks.Go(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(exited)
	}) {
		t.Fatal("first task was rejected")
	}
	<-started

	tasks.Stop()
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before the owned task exited")
	}
	if tasks.Go(func(context.Context) {}) {
		t.Fatal("task was accepted after Stop")
	}
}

func TestTaskGroupBeginStopCancelsWithoutWaiting(t *testing.T) {
	tasks := newTaskGroup(context.Background())
	canceled := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	if !tasks.Go(func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
		<-release
		close(done)
	}) {
		t.Fatal("task was rejected")
	}

	tasks.BeginStop()
	<-canceled
	select {
	case <-done:
		t.Fatal("task exited before the test released it")
	default:
	}
	if tasks.Go(func(context.Context) {}) {
		t.Fatal("task was accepted after BeginStop")
	}

	close(release)
	tasks.Stop()
	select {
	case <-done:
	default:
		t.Fatal("Stop returned before the accepted task exited")
	}
}
