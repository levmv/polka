package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// taskGroup owns server work accepted through Go or an HTTP wrapper. Seal
// prevents new work, BeginStop also cancels the shared context, and Stop joins
// everything accepted before returning.
type taskGroup struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	sealed bool
	wg     sync.WaitGroup
}

func newTaskGroup(parent context.Context) *taskGroup {
	ctx, cancel := context.WithCancel(parent)
	return &taskGroup{ctx: ctx, cancel: cancel}
}

func (g *taskGroup) Context() context.Context {
	return g.ctx
}

func (g *taskGroup) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed {
		return false
	}
	g.wg.Add(1)
	return true
}

func (g *taskGroup) leave() {
	g.wg.Done()
}

func (g *taskGroup) Go(run func(context.Context)) bool {
	if !g.enter() {
		return false
	}
	go func() {
		defer g.leave()
		run(g.ctx)
	}()
	return true
}

func (g *taskGroup) Seal() {
	g.mu.Lock()
	g.sealed = true
	g.mu.Unlock()
}

// BeginStop cancels accepted work without waiting for it. This lets emergency
// shutdown cancel background tasks and requests before joining either group.
func (g *taskGroup) BeginStop() {
	g.Seal()
	g.cancel()
}

func (g *taskGroup) Wait() {
	g.wg.Wait()
}

func (g *taskGroup) Stop() {
	g.BeginStop()
	g.Wait()
}

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
