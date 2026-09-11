package db

import (
	"context"
	"database/sql"
	"errors"
)

// Tx owns the writer connection until commit, rollback, or caller cancellation.
// Its statements inherit the context passed to BeginWrite.
type Tx struct {
	tx        *sql.Tx
	conn      *sql.Conn
	ctx       context.Context
	stopClose func() bool
}

// BeginWrite waits for the writer and begins an immediate SQLite transaction.
// Queueing has a finite deadline; the transaction keeps the caller's context.
// Reserve Conn separately: sql.DB.BeginTx uses one context for both waiting and
// the transaction, so a writer-wait deadline would also abort the transaction.
// Use tx for every operation until commit or rollback: acquiring the writer
// again from inside the transaction would wait on itself.
func (db *DB) BeginWrite(ctx context.Context) (*Tx, error) {
	conn, err := db.acquireWriter(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Tx{
		tx: tx, conn: conn, ctx: ctx,
		// database/sql rolls back the transaction on cancellation. Close returns
		// our reserved connection to the pool even before the caller reaches its
		// deferred Rollback. sql.Conn.Close waits for active operations and is safe
		// to call concurrently with the Commit/Rollback cleanup below.
		stopClose: context.AfterFunc(ctx, func() { conn.Close() }),
	}, nil
}

func (tx *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.tx.ExecContext(tx.ctx, query, args...)
}

func (tx *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return tx.tx.QueryContext(tx.ctx, query, args...)
}

func (tx *Tx) QueryRow(query string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(tx.ctx, query, args...)
}

// Prepare uses the transaction's context to prepare a statement. The returned
// sql.Stmt still needs an explicit context when executed (e.g. ExecContext).
func (tx *Tx) Prepare(query string) (*sql.Stmt, error) {
	return tx.tx.PrepareContext(tx.ctx, query)
}

func (tx *Tx) Commit() error {
	return errors.Join(tx.tx.Commit(), tx.close())
}

func (tx *Tx) Rollback() error {
	return errors.Join(tx.tx.Rollback(), tx.close())
}

func (tx *Tx) close() error {
	tx.stopClose()
	err := tx.conn.Close()
	if errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}
