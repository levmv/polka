package web

import (
	"testing"
	"time"
)

func TestStopIngesterWaitsForServiceExit(t *testing.T) {
	canceled := make(chan struct{})
	done := make(chan struct{})
	stopped := make(chan struct{})
	s := &Server{
		ingestCancel: func() { close(canceled) },
		ingestDone:   done,
	}

	go func() {
		s.stopIngester()
		close(stopped)
	}()
	<-canceled
	select {
	case <-stopped:
		t.Fatal("stopIngester returned before the service exited")
	default:
	}

	close(done)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stopIngester did not return after the service exited")
	}
}
