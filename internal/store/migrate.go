package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/config"
)

//go:embed migrations/*/*.sql
var migrationFiles embed.FS

// migrationLockID is the PostgreSQL advisory lock that serializes migrations
// of processes started at the same time. The value is arbitrary.
const migrationLockID = 0x66726573 // "fres"

type migration struct {
	version int
	sql     string
}

// loadMigrations reads migrations/<engine>/NNNN_<name>.sql in version order.
func loadMigrations(driver config.Driver) ([]migration, error) {
	dir := "migrations/" + string(driver)
	entries, err := fs.ReadDir(migrationFiles, dir)
	if err != nil {
		return nil, fmt.Errorf("store: migrations of %s: %w", driver, err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, e := range entries {
		prefix, _, _ := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("store: migration %s: name must start with a number", e.Name())
		}
		body, err := fs.ReadFile(migrationFiles, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: migration %s: %w", e.Name(), err)
		}
		migrations = append(migrations, migration{version: version, sql: string(body)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migrations of %s: version %d is missing", driver, i+1)
		}
	}
	return migrations, nil
}

// migrate applies the migrations the database has not seen yet in one
// transaction: a failed upgrade leaves the schema as it was. A database newer
// than the binary is refused.
func (s *Store) migrate(ctx context.Context) error {
	migrations, err := loadMigrations(s.driver)
	if err != nil {
		return err
	}
	return s.InTx(ctx, func(tx *Store) error {
		if tx.driver == config.DriverPostgres {
			if _, err := tx.exec(ctx, "SELECT pg_advisory_xact_lock(?)", migrationLockID); err != nil {
				return fmt.Errorf("store: migration lock: %w", err)
			}
		}
		if _, err := tx.exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY)"); err != nil {
			return fmt.Errorf("store: schema_migrations: %w", err)
		}
		var current int
		if err := tx.queryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
			return fmt.Errorf("store: schema version: %w", err)
		}
		if current > len(migrations) {
			return fmt.Errorf("store: database schema version %d is newer than this binary supports (%d)", current, len(migrations))
		}
		for _, m := range migrations[current:] {
			// No arguments: both drivers then accept several statements at once.
			if _, err := tx.q.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("store: migration %d: %w", m.version, err)
			}
			if _, err := tx.exec(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
				return fmt.Errorf("store: migration %d: %w", m.version, err)
			}
		}
		return nil
	})
}
