package web

import (
	"context"
	"sync"
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
