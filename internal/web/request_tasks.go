package web

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type httpServerLifecycle interface {
	Shutdown(context.Context) error
	Close() error
}

// Wrap tracks handlers admitted before the group is sealed. This makes it safe
// to release the writer lease and close the database only after every accepted
// request has returned.
func (g *taskGroup) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !g.enter() {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}

		defer g.leave()
		next.ServeHTTP(w, req)
	})
}

// shutdownHTTPServer first gives active requests the normal graceful window.
// If that expires, it cancels their common base context, closes connections,
// and still joins the handlers before returning.
func shutdownHTTPServer(server httpServerLifecycle, requests *taskGroup, timeout time.Duration) error {
	requests.Seal()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := server.Shutdown(ctx)
	cancel()
	if err == nil {
		requests.Stop()
		return nil
	}

	requests.BeginStop()
	closeErr := server.Close()
	requests.Wait()
	if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
		return errors.Join(err, closeErr)
	}
	return err
}

func forceCloseHTTPServer(server httpServerLifecycle, requests *taskGroup) error {
	requests.BeginStop()
	err := server.Close()
	requests.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
