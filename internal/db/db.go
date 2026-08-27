package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"

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
	sqliteNetworkReadOnlyOptions = "_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_txlock=immediate"
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
	return sqliteOptions
}

// DB wraps the sql.DB instance.
type DB struct {
	*sql.DB
}

// Queryer is satisfied by both *sql.DB and *sql.Tx.
type Queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Execer is satisfied by both *sql.DB and *sql.Tx.
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

// Transact runs fn inside a transaction, rolling back on any returned error and
// committing only after fn succeeds. The context controls BeginTx and the
// transaction lifetime; callers should pass the request or command context they
// already own.
func (db *DB) Transact(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
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
	info := fsprofile.Detect(path)
	if info.IsNetwork() {
		log.Printf("WARNING: SQLite database is on network filesystem %q at %s; using rollback journal and extended busy timeout", info.TypeOrUnknown(), info.Path)
	}
	return initPath(path, appendDSNQuery(DSN(path), sqliteOptionsFor(info, false)))
}

// InitPathReadOnly opens a SQLite DB at path in read-only mode using
// filesystem-aware pragmas. Network filesystems avoid journal_mode changes here:
// read-only checks should not need to mutate the database just to inspect it.
func InitPathReadOnly(path string) (*DB, error) {
	info := fsprofile.Detect(path)
	if info.IsNetwork() {
		log.Printf("WARNING: SQLite database is on network filesystem %q at %s; opening read-only with extended busy timeout", info.TypeOrUnknown(), info.Path)
	}
	dsn := appendDSNQuery(DSN(path), "mode=ro")
	return initPath(path, appendDSNQuery(dsn, sqliteOptionsFor(info, true)))
}

func initPath(path, dsn string) (*DB, error) {
	database, err := initDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("library database %s: %w", path, err)
	}
	return database, nil
}

func initDSN(dsn string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, err
	}

	if err := runMigrations(context.Background(), sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &DB{DB: sqlDB}, nil
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

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	slices.Sort(files)

	for _, file := range files {
		var applied bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)", file).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", file, err)
		}

		if applied {
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + file)
		if err != nil {
			return fmt.Errorf("read migration file %s: %w", file, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", file, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec migration %s: %w", file, err)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", file); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", file, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", file, err)
		}
	}

	return nil
}
