package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/config"
)

// openSQLite opens a database in a directory of the test and keeps it open
// until the test ends, as a running server would.
func openSQLite(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	s, err := Open(context.Background(), config.DriverSQLite, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// A copy taken while the database is open holds what is still in its WAL, and
// passes the check with the numbers of what it holds.
func TestBackupOfAnOpenDatabase(t *testing.T) {
	ctx := context.Background()
	s, path := openSQLite(t)
	alice := mustUser(t, s, "alice")
	mustUser(t, s, "bob")
	f := mustFeed(t, s, alice.ID, "https://example.org/feed")
	var entries []*Entry
	for i := range 3 {
		entries = append(entries, &Entry{FeedID: f.ID, GUID: fmt.Sprint(i), Title: "entry"})
	}
	if err := s.InsertEntries(ctx, alice.ID, entries); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the WAL holds nothing to lose (%v), the test proves nothing", err)
	}
	// The main file alone is what a copy of it misses.
	alone := filepath.Join(t.TempDir(), "alone.sqlite")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alone, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if report, err := CheckBackup(ctx, alone); err == nil && report.Entries == 3 {
		t.Fatal("the main file alone already holds every entry, the test proves nothing")
	}

	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	report, err := CheckBackup(ctx, backup)
	if err != nil {
		t.Fatalf("CheckBackup: %v", err)
	}
	migrations, err := loadMigrations(config.DriverSQLite)
	if err != nil {
		t.Fatal(err)
	}
	want := BackupReport{Version: len(migrations), Supported: len(migrations), Users: 2, Feeds: 1, Entries: 3}
	if report != want {
		t.Errorf("report = %+v, want %+v", report, want)
	}
	if _, err := os.Stat(backup + "-wal"); err == nil {
		t.Error("the check left a WAL next to the copy")
	}
}

func TestBackupDoesNotOverwrite(t *testing.T) {
	s, _ := openSQLite(t)
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := os.WriteFile(backup, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(context.Background(), backup); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Backup over a file: %v, want a refusal", err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "kept" {
		t.Errorf("the file became %q (%v)", data, err)
	}
}

func TestBackupOfPostgreSQL(t *testing.T) {
	s := &Store{driver: config.DriverPostgres}
	if err := s.Backup(context.Background(), filepath.Join(t.TempDir(), "x")); !errors.Is(err, ErrNotSQLite) {
		t.Errorf("Backup = %v, want ErrNotSQLite", err)
	}
}

// A copy made by a newer binary cannot be used by this one.
func TestCheckBackupTooNew(t *testing.T) {
	ctx := context.Background()
	s, _ := openSQLite(t)
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	newer, err := Open(ctx, config.DriverSQLite, backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newer.exec(ctx, "INSERT INTO schema_migrations (version) VALUES (100000)"); err != nil {
		t.Fatal(err)
	}
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckBackup(ctx, backup); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Errorf("CheckBackup = %v, want the schema refused as too new", err)
	}
}

func TestCheckBackupRefusesOtherFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("not a database ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckBackup(ctx, garbage); err == nil {
		t.Error("CheckBackup accepted a file that is not a database")
	}
	if _, err := CheckBackup(ctx, filepath.Join(dir, "missing")); err == nil {
		t.Error("CheckBackup accepted a file that does not exist")
	}

	// A SQLite database of something else.
	other := filepath.Join(dir, "other.sqlite")
	s, err := Open(ctx, config.DriverSQLite, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, "DROP TABLE schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckBackup(ctx, other); err == nil || !strings.Contains(err.Error(), "not a freshgo database") {
		t.Errorf("CheckBackup = %v, want it refused as not a freshgo database", err)
	}
}
