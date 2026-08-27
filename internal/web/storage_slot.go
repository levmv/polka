package web

import "context"

// acquireStorageWorkSlot serializes a DB-first storage mutation as one unit,
// including its transaction and post-commit filesystem maintenance. Holding the
// process-wide slot across both phases prevents imports, relayout, cover writes,
// and write-back from racing through an intermediate DB/disk state.
func (s *Server) acquireStorageWorkSlot(ctx context.Context) (func(), error) {
	if s.storageQueue == nil {
		return func() {}, nil
	}
	return s.storageQueue.Acquire(ctx)
}
