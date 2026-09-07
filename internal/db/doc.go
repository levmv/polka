// Package db owns Polka's persisted catalog operations and the entity
// invariants that must hold for every caller. Higher-level workflows, pure
// format and storage policy, and filesystem or network work belong outside this
// package.
//
// DB owns a read-only pool and a single-connection writer pool. Writes queue
// with a maximum wait independent of the caller's operation deadline.
// ErrWriterTimeout reports that wait expiring, separately from caller
// cancellation. Read-only opens have no writer and never migrate.
//
// SQL helpers accept context-bound handles: DB.Read(ctx) provides a Queryer and
// DB.Write(ctx) provides an Execer. Callers must consume or close rows promptly
// to release connections. Mutations requiring several related statements use
// DB.Transact; all SQL within its callback, including reads, must use the Tx.
// Tx implements both interfaces and carries the transaction's context.
package db
