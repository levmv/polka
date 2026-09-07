package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/levmv/polka/internal/fsprofile"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DSN builds a SQLite `file:` URI from a filesystem path, percent-encoding
// characters that are significant in a URI (notably `?`, `#` and spaces) so a
// path containing them opens the intended file instead of being parsed as query
// parameters / fragment. Path separators are preserved; relative paths stay
// relative (no `//` authority is introduced).
func DSN(path string) string {
	u := url.URL{Path: path}
	return "file:" + u.EscapedPath()
}

const (
	sqliteOptions                = "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqliteNetworkOptions         = "_pragma=foreign_keys(1)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)&_pragma=busy_timeout(15000)&_txlock=immediate"
	sqliteReadOnlyOptions        = "mode=ro&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	sqliteNetworkReadOnlyOptions = "mode=ro&_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=query_only(1)"
)

func appendDSNQuery(dsn, query string) string {
	if query == "" {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&" + query
	}
	return dsn + "?" + query
}

func sqliteOptionsFor(info fsprofile.Info, readOnly bool) string {
	if info.IsNetwork() {
		if readOnly {
			return sqliteNetworkReadOnlyOptions
		}
		return sqliteNetworkOptions
	}
	if readOnly {
		return sqliteReadOnlyOptions
	}
	return sqliteOptions
}

// DB owns independent read and write pools for one library. Readers cannot
// write; the writer has one connection, so concurrent writes wait in Go rather
// than competing for SQLite's write lock. All SQL inside a write transaction,
// including reads, must use its Tx.
type DB struct {
	reader            *sql.DB
	writer            *sql.DB // nil when the library was opened read-only
	writerWaitTimeout time.Duration
}

var ErrReadOnly = errors.New("library database is read-only")

// ErrWriterTimeout means the writer was not acquired; no SQL was executed.
// It is distinct from a deadline or cancellation of the caller's context.
var ErrWriterTimeout = errors.New("timed out waiting for library writer")

const (
	defaultWriterWaitTimeout = 30 * time.Second
	bestEffortWriteTimeout   = 100 * time.Millisecond
)

// Queryer reads from the library's reader pool or an existing transaction.
type Queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Execer writes through the library's single writer or an existing transaction.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// rowScanner is the Scan method shared by *sql.Row and *sql.Rows, which the
// standard library does not expose through a common type.
type rowScanner interface {
	Scan(dest ...any) error
}

// detailedError keeps a stable errors.Is classification while preserving the
// specific validation message a caller can show to a person.
type detailedError struct {
	class  error
	detail string
}

func (e detailedError) Error() string { return e.detail }
func (e detailedError) Unwrap() error { return e.class }

func errorWithDetail(class error, detail string) error {
	return detailedError{class: class, detail: detail}
}

// Read binds query helpers to the caller's context without reserving a connection.
// Inside a transaction, pass Tx instead; it already owns the caller's context.
func (db *DB) Read(ctx context.Context) Queryer {
	return contextReader{pool: db.reader, ctx: ctx}
}

type contextReader struct {
	pool *sql.DB
	ctx  context.Context
}

func (r contextReader) Query(query string, args ...any) (*sql.Rows, error) {
	return r.pool.QueryContext(r.ctx, query, args...)
}

func (r contextReader) QueryRow(query string, args ...any) *sql.Row {
	return r.pool.QueryRowContext(r.ctx, query, args...)
}

// Write binds SQL helpers to the caller's context. Each Exec acquires and
// releases the writer independently; use Transact when statements must be atomic.
func (db *DB) Write(ctx context.Context) Execer {
	return contextWriter{db: db, ctx: ctx}
}

type contextWriter struct {
	db  *DB
	ctx context.Context
}

func (w contextWriter) Exec(query string, args ...any) (sql.Result, error) {
	conn, err := w.db.acquireWriter(w.ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ExecContext(w.ctx, query, args...)
}

// ExecBestEffort bounds optional bookkeeping, such as credential timestamps,
// to a short deadline. It returns false without an error if the write times out;
// caller cancellation and other database errors still propagate. Do not use it
// for required mutations, including credential creation or revocation.
func (db *DB) ExecBestEffort(ctx context.Context, query string, args ...any) (bool, error) {
	writeCtx, cancel := context.WithTimeout(ctx, bestEffortWriteTimeout)
	defer cancel()
	_, err := db.Write(writeCtx).Exec(query, args...)
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrWriterTimeout) {
		return false, nil
	}
	return false, err
}

// acquireWriter bounds queueing independently of the operation's lifetime.
// Once acquired, the connection is used with the caller's original context.
func (db *DB) acquireWriter(ctx context.Context) (*sql.Conn, error) {
	if db.writer == nil {
		return nil, ErrReadOnly
	}
	waitCtx, cancel := context.WithTimeoutCause(ctx, db.writerWaitTimeout, ErrWriterTimeout)
	defer cancel()
	conn, err := db.writer.Conn(waitCtx)
	if err != nil {
		if waitCtx.Err() != nil {
			return nil, context.Cause(waitCtx)
		}
		return nil, err
	}
	return conn, nil
}

func (db *DB) Close() error {
	err := db.reader.Close()
	if db.writer != nil {
		err = errors.Join(err, db.writer.Close())
	}
	return err
}

// Transact runs fn in a transaction using the caller's context. It rolls back
// if fn returns an error and commits after fn succeeds.
func (db *DB) Transact(ctx context.Context, fn func(tx *Tx) error) error {
	tx, err := db.BeginWrite(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// InitPath opens a SQLite DB at path using filesystem-aware pragmas.
func InitPath(path string) (*DB, error) {
	return initPath(path, fsprofile.Detect(path), false)
}

// InitPathReadOnly opens a SQLite DB at path in read-only mode using
// filesystem-aware pragmas. It neither runs migrations nor changes journal mode.
func InitPathReadOnly(path string) (*DB, error) {
	return initPath(path, fsprofile.Detect(path), true)
}

func initPath(path string, info fsprofile.Info, readOnly bool) (*DB, error) {
	if info.IsNetwork() {
		if readOnly {
			log.Printf("WARNING: SQLite database is on network filesystem %q at %s; opening read-only with extended busy timeout", info.TypeOrUnknown(), info.Path)
		} else {
			log.Printf("WARNING: SQLite database is on network filesystem %q at %s; using rollback journal and extended busy timeout", info.TypeOrUnknown(), info.Path)
		}
	}
	database, err := openPools(path, info, readOnly)
	if err != nil {
		return nil, fmt.Errorf("library database %s: %w", path, err)
	}
	return database, nil
}

func openPools(path string, info fsprofile.Info, readOnly bool) (*DB, error) {
	var writer *sql.DB
	if !readOnly {
		var err error
		writer, err = openPool(appendDSNQuery(DSN(path), sqliteOptionsFor(info, false)), true)
		if err != nil {
			return nil, fmt.Errorf("open writer: %w", err)
		}
		// Finish schema initialization before any read-only connection opens.
		// The persistent idle writer also keeps WAL's shared-memory file ready
		// for readers; do not give it an idle timeout or connection lifetime.
		if err := runMigrations(context.Background(), writer); err != nil {
			writer.Close()
			return nil, err
		}
	}

	reader, err := openPool(appendDSNQuery(DSN(path), sqliteOptionsFor(info, true)), false)
	if err != nil {
		if writer != nil {
			writer.Close()
		}
		return nil, fmt.Errorf("open reader: %w", err)
	}
	database := &DB{reader: reader, writer: writer, writerWaitTimeout: defaultWriterWaitTimeout}
	if readOnly {
		if err := verifySchemaCurrent(context.Background(), reader); err != nil {
			database.Close()
			return nil, err
		}
	}
	return database, nil
}

func openPool(dsn string, writer bool) (*sql.DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if writer {
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return sqlDB, nil
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY
		);
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	files, err := pendingMigrations(ctx, db)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := applyMigration(ctx, db, file); err != nil {
			return err
		}
	}
	return nil
}

func verifySchemaCurrent(ctx context.Context, db *sql.DB) error {
	files, err := pendingMigrations(ctx, db)
	if err != nil {
		return err
	}
	if len(files) != 0 {
		return fmt.Errorf("library schema has pending migration %s; cannot open read-only", files[0])
	}
	return nil
}

func pendingMigrations(ctx context.Context, db *sql.DB) ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)

	var pending []string
	for _, file := range files {
		var applied bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)", file).Scan(&applied)
		if err != nil {
			return nil, fmt.Errorf("check migration %s: %w", file, err)
		}
		if !applied {
			pending = append(pending, file)
		}
	}
	return pending, nil
}

func applyMigration(ctx context.Context, db *sql.DB, file string) error {
	content, err := migrationsFS.ReadFile("migrations/" + file)
	if err != nil {
		return fmt.Errorf("read migration file %s: %w", file, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", file, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		return fmt.Errorf("exec migration %s: %w", file, err)
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", file); err != nil {
		return fmt.Errorf("record migration %s: %w", file, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", file, err)
	}
	return nil
}
