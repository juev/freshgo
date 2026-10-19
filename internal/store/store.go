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
	"maps"
	"net/url"
	"os"
	"runtime"
	"slices"
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
	system *systemCache
	// systemChanged is set inside a transaction that changed what System
	// reads; the cache lets go of it once the transaction is committed.
	systemChanged *bool
	// EntriesChanged, when set, is told about a user whose entries were
	// added, updated or deleted, read or unread, starred or unstarred, or
	// given or taken a label, and whether entries were added: once the
	// transaction that changed them is committed. It must not wait. It is
	// set before the Store is used.
	EntriesChanged func(userID int64, added bool)
	// changedUsers collects, inside a transaction, the users to tell
	// EntriesChanged about once it is committed, and whether entries of
	// theirs were added.
	changedUsers map[int64]bool
}

// Open connects to the database and brings its schema up to date.
// For SQLite dsn is a file path, for PostgreSQL a connection URL.
func Open(ctx context.Context, driver config.Driver, dsn string) (*Store, error) {
	s, err := connect(driver, dsn)
	if err != nil {
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

// OpenAsIs connects to a database that exists and leaves its schema as it
// is, for a look at it: a SQLite file that is not there is an error, not a
// new database.
func OpenAsIs(ctx context.Context, driver config.Driver, dsn string) (*Store, error) {
	if driver == config.DriverSQLite {
		if _, err := os.Stat(dsn); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	s, err := connect(driver, dsn)
	if err != nil {
		return nil, err
	}
	if err := s.Ping(ctx); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	return s, nil
}

func connect(driver config.Driver, dsn string) (*Store, error) {
	var (
		db  *sql.DB
		err error
	)
	switch driver {
	case config.DriverSQLite:
		db, err = sql.Open("sqlite", sqliteDSN(dsn))
	case config.DriverPostgres:
		if db, err = sql.Open("pgx", dsn); err == nil {
			// A busy server would otherwise open a connection per request
			// until PostgreSQL refuses them.
			db.SetMaxOpenConns(postgresConns())
			db.SetMaxIdleConns(postgresConns())
			db.SetConnMaxLifetime(postgresConnLifetime)
		}
	default:
		return nil, fmt.Errorf("store: unknown driver %q", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	return &Store{db: db, q: db, driver: driver, now: time.Now, system: &systemCache{}}, nil
}

// postgresConns is how many connections to PostgreSQL a Store keeps open at
// most, and idle: four per processor, and no fewer than ten.
func postgresConns() int {
	return max(4*runtime.GOMAXPROCS(0), 10)
}

// postgresConnLifetime retires a connection after a while, so that a
// failover or a restarted pooler in front of the server is picked up.
const postgresConnLifetime = 30 * time.Minute

// sqliteDSN turns a file path into a URI with the pragmas freshgo relies on.
// Foreign keys are off by default in SQLite; immediate transactions make a
// writer wait for the lock at BEGIN instead of failing in the middle.
func sqliteDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Set("_txlock", "immediate")
	return SQLiteURI(path, q)
}

// sqlitePath escapes the characters that end or change the path of a URI.
var sqlitePath = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// SQLiteURI is the URI of the database file at path with the query
// parameters q. Written into the URI as is, a "?" or "#" of the path would cut
// it there: SQLite would open another file, and the driver would read the
// rest of the path as its parameters.
func SQLiteURI(path string, q url.Values) string {
	return "file:" + sqlitePath.Replace(path) + "?" + q.Encode()
}

// Close releases the database connections.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}

// Size returns how many bytes the database takes: the file of SQLite
// without its write-ahead log, the database of PostgreSQL.
func (s *Store) Size(ctx context.Context) (int64, error) {
	query := `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`
	if s.driver == config.DriverPostgres {
		query = `SELECT pg_database_size(current_database())`
	}
	var size int64
	if err := s.queryRow(ctx, query).Scan(&size); err != nil {
		return 0, fmt.Errorf("store: size: %w", err)
	}
	return size, nil
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
	var systemChanged bool
	changedUsers := map[int64]bool{}
	inner := *s
	inner.q, inner.systemChanged, inner.changedUsers = tx, &systemChanged, changedUsers
	if err := fn(&inner); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	if systemChanged {
		s.system.forget()
	}
	for _, user := range slices.Sorted(maps.Keys(changedUsers)) {
		s.EntriesChanged(user, changedUsers[user])
	}
	return nil
}

// entriesChanged tells EntriesChanged that the entries of a user changed, or
// has it told once the transaction is committed.
func (s *Store) entriesChanged(userID int64, added bool) {
	switch {
	case s.EntriesChanged == nil:
	case s.changedUsers != nil:
		s.changedUsers[userID] = s.changedUsers[userID] || added
	default:
		s.EntriesChanged(userID, added)
	}
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
