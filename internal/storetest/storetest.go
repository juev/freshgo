// Package storetest gives tests a database on every engine freshgo supports.
package storetest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"github.com/juev/freshgo/internal/config"
)

// EnvPostgresURL points the tests at a PostgreSQL server; `make
// test-integration` starts one and sets it. Without it only SQLite is tested.
const EnvPostgresURL = "FRESHGO_TEST_POSTGRES_URL"

// Engine is a database engine tests can run against.
type Engine struct {
	Name string
	// New prepares an empty database and returns what store.Open takes. It
	// skips the test when the engine is not available.
	New func(t *testing.T) (driver config.Driver, dsn string)
}

// Engines lists the supported engines.
func Engines() []Engine {
	return []Engine{
		{"sqlite", func(t *testing.T) (config.Driver, string) {
			return config.DriverSQLite, filepath.Join(t.TempDir(), "freshgo.sqlite")
		}},
		{"postgres", func(t *testing.T) (config.Driver, string) {
			return config.DriverPostgres, PostgresSchema(t)
		}},
	}
}

// PostgresSchema creates a schema of its own for the test and returns a
// connection URL that makes it the search path.
func PostgresSchema(t *testing.T) string {
	t.Helper()
	admin, base := postgresAdmin(t)
	schema := uniqueName()
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	q := base.Query()
	q.Set("search_path", schema)
	base.RawQuery = q.Encode()
	return base.String()
}

// PostgresDatabase creates a database of its own for the test and returns its
// connection URL. For data that names the public schema, such as a dump.
func PostgresDatabase(t *testing.T) string {
	t.Helper()
	admin, base := postgresAdmin(t)
	name := uniqueName()
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})
	base.Path = "/" + name
	return base.String()
}

func postgresAdmin(t *testing.T) (*sql.DB, *url.URL) {
	t.Helper()
	raw := os.Getenv(EnvPostgresURL)
	if raw == "" {
		t.Skip(EnvPostgresURL + " is not set; run `make test-integration`")
	}
	base, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvPostgresURL, err)
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatalf("open %s: %v", EnvPostgresURL, err)
	}
	// Registered first, so it runs after the cleanup that drops the schema.
	t.Cleanup(func() { _ = admin.Close() })
	return admin, base
}

func uniqueName() string {
	return fmt.Sprintf("t%d", time.Now().UnixNano())
}
