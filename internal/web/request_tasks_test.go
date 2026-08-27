package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type deadlineHTTPServer struct {
	closed bool
}

func (*deadlineHTTPServer) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *deadlineHTTPServer) Close() error {
	s.closed = true
	return nil
}

type gracefulHTTPServer struct {
	release chan struct{}
	exited  <-chan struct{}
}

func (s *gracefulHTTPServer) Shutdown(context.Context) error {
	close(s.release)
	<-s.exited
	return nil
}

func (*gracefulHTTPServer) Close() error { return nil }

func TestShutdownHTTPServerLetsActiveHandlersFinishBeforeCancellation(t *testing.T) {
	requests := newTaskGroup(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	exited := make(chan struct{})
	canceled := make(chan struct{})
	handler := requests.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		close(started)
		select {
		case <-release:
		case <-req.Context().Done():
			close(canceled)
		}
		close(exited)
	}))
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(requests.Context())
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-started

	server := &gracefulHTTPServer{release: release, exited: exited}
	if err := shutdownHTTPServer(server, requests, time.Second); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("graceful shutdown canceled an active handler")
	default:
	}
}

func TestShutdownHTTPServerCancelsAndJoinsHandlersAfterDeadline(t *testing.T) {
	requests := newTaskGroup(context.Background())
	started := make(chan struct{})
	exited := make(chan struct{})
	handler := requests.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		close(started)
		<-req.Context().Done()
		time.Sleep(10 * time.Millisecond)
		close(exited)
	}))
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(requests.Context())
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	server := &deadlineHTTPServer{}
	if err := shutdownHTTPServer(server, requests, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if !server.closed {
		t.Fatal("timed-out shutdown did not close the HTTP server")
	}
	select {
	case <-exited:
	default:
		t.Fatal("shutdown returned before the handler exited")
	}
}
