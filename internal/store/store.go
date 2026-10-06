// Package store keeps freshgo data in SQLite or PostgreSQL.
//
// The two engines share one implementation: queries are written with "?"
// placeholders and standard SQL, and the few places where the engines differ
// are isolated in this file and in the migrations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/juev/freshgo/internal/config"
)

var (
	// ErrNotFound is returned when the requested row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when a write breaks a uniqueness rule: a taken
	// name, a reused identifier, a duplicate guid within a feed.
	ErrConflict = errors.New("store: conflict")
)

// querier is the part of *sql.DB and *sql.Tx the queries need.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store gives access to the database. A Store obtained inside InTx runs its
// queries in that transaction.
type Store struct {
	db     *sql.DB
	q      querier
	driver config.Driver
	now    func() time.Time
}

// Open connects to the database and brings its schema up to date.
// For SQLite dsn is a file path, for PostgreSQL a connection URL.
func Open(ctx context.Context, driver config.Driver, dsn string) (*Store, error) {
	var (
		db  *sql.DB
		err error
	)
	switch driver {
	case config.DriverSQLite:
		db, err = sql.Open("sqlite", sqliteDSN(dsn))
	case config.DriverPostgres:
		db, err = sql.Open("pgx", dsn)
	default:
		return nil, fmt.Errorf("store: unknown driver %q", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	s := &Store{db: db, q: db, driver: driver, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// sqliteDSN turns a file path into a URI with the pragmas freshgo relies on.
// Foreign keys are off by default in SQLite; immediate transactions make a
// writer wait for the lock at BEGIN instead of failing in the middle.
func sqliteDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Close releases the database connections.
func (s *Store) Close() error {
	return s.db.Close()
}

// InTx runs fn in a transaction and commits if fn returns nil. Called on a
// Store that is already inside a transaction, it joins that transaction.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	if _, ok := s.q.(*sql.Tx); ok {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	inner := *s
	inner.q = tx
	if err := fn(&inner); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := s.q.ExecContext(ctx, s.rebind(query), args...)
	return res, mapError(err)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := s.q.QueryContext(ctx, s.rebind(query), args...)
	return rows, mapError(err)
}

// row is a single-row query whose Scan reports store errors.
type row struct{ r *sql.Row }

func (r row) Scan(dest ...any) error {
	return mapError(r.r.Scan(dest...))
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) row {
	return row{s.q.QueryRowContext(ctx, s.rebind(query), args...)}
}

// rebind rewrites "?" placeholders to "$1", "$2", … for PostgreSQL.
// Queries in this package never contain a literal question mark.
func (s *Store) rebind(query string) string {
	if s.driver != config.DriverPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// mapError translates driver errors into the sentinel errors of this package.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pgErr.Message)
	}
	var liteErr *sqlite.Error
	if errors.As(err, &liteErr) {
		switch liteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %s", ErrConflict, liteErr.Error())
		}
	}
	return err
}

// scanner is what *sql.Rows and a single-row query have in common.
type scanner interface {
	Scan(dest ...any) error
}

// collect reads every row with scan and closes rows.
func collect[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// placeholders returns "?, ?, …" with n question marks.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
