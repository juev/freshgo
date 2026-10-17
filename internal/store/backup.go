package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"

	"github.com/juev/freshgo/internal/config"
)

// ErrNotSQLite is returned by Backup on a PostgreSQL database: its copies are
// made by pg_dump.
var ErrNotSQLite = errors.New("store: only a SQLite database is copied by the server")

// Backup writes a consistent copy of a SQLite database to path while it is in
// use. The copy holds what the WAL holds too. An existing file at path is
// left as it is and refused.
func (s *Store) Backup(ctx context.Context, path string) error {
	if s.driver != config.DriverSQLite {
		return ErrNotSQLite
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("store: backup: %s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: backup: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}

// BackupReport is what CheckBackup found in a copy.
type BackupReport struct {
	// Version is the version of the schema of the copy, Supported the latest
	// one this binary knows.
	Version, Supported int
	Users, Feeds, Entries int
}

// CheckBackup opens a copy of a SQLite database read-only and tells whether
// this binary can start on it: the file must be whole and its schema no newer
// than the migrations built in. An older schema is brought up to date at the
// first start.
func CheckBackup(ctx context.Context, path string) (BackupReport, error) {
	var report BackupReport
	if _, err := os.Stat(path); err != nil {
		return report, fmt.Errorf("store: check: %w", err)
	}
	migrations, err := loadMigrations(config.DriverSQLite)
	if err != nil {
		return report, err
	}
	report.Supported = len(migrations)
	db, err := sql.Open("sqlite", SQLiteURI(path, url.Values{"mode": {"ro"}}))
	if err != nil {
		return report, fmt.Errorf("store: check: %w", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return report, fmt.Errorf("store: check: %w", err)
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = rows.Close()
			return report, fmt.Errorf("store: check: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return report, fmt.Errorf("store: check: %w", err)
	}
	if len(problems) > 0 {
		return report, fmt.Errorf("store: check: the file is damaged: %s", strings.Join(problems, "; "))
	}

	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&report.Version); err != nil {
		return report, fmt.Errorf("store: check: not a freshgo database: %w", err)
	}
	if report.Version > report.Supported {
		return report, fmt.Errorf("store: check: schema version %d is newer than this binary supports (%d)", report.Version, report.Supported)
	}
	for _, c := range []struct {
		table string
		n     *int
	}{{"users", &report.Users}, {"feeds", &report.Feeds}, {"entries", &report.Entries}} {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+c.table).Scan(c.n); err != nil {
			return report, fmt.Errorf("store: check: %s: %w", c.table, err)
		}
	}
	return report, nil
}
