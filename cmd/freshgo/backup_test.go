package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/store"
)

// A copy taken while the database is open, as under a running server,
// passes the check.
func TestBackupThenCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	db, err := store.Open(context.Background(), config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateUser(context.Background(), &store.User{Name: "alice"}); err != nil {
		t.Fatal(err)
	}

	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	code, stdout, stderr := runCLI(t, "backup", "-database-url", "sqlite://"+path, "-to", backup)
	if code != 0 || stdout != "copied to "+backup+"\n" {
		t.Fatalf("backup: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(t, "restore", "-check", backup)
	if code != 0 || !strings.HasSuffix(stdout, ", 1 users, 0 feeds, 0 entries\n") {
		t.Errorf("restore -check: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	code, _, stderr = runCLI(t, "backup", "-database-url", "sqlite://"+path, "-to", backup)
	if code != 1 || !strings.Contains(stderr, "already exists") {
		t.Errorf("backup over the copy: code %d, stderr %q; want a refusal", code, stderr)
	}
}

func TestBackupAndRestoreUsage(t *testing.T) {
	database := "sqlite://" + filepath.Join(t.TempDir(), "freshgo.sqlite")
	if code, _, stderr := runCLI(t, "backup", "-database-url", database); code != 1 || !strings.Contains(stderr, "-to is required") {
		t.Errorf("backup without -to: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "restore", "x.sqlite"); code != 1 || !strings.Contains(stderr, "only -check is supported") {
		t.Errorf("restore without -check: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "restore", "-check"); code != 1 || !strings.Contains(stderr, "the file to check is required") {
		t.Errorf("restore -check without a file: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "restore", "-check", filepath.Join(t.TempDir(), "missing")); code != 1 || stderr == "" {
		t.Errorf("restore -check of a missing file: code %d, stderr %q", code, stderr)
	}
}

// Without pg_dump the command fails and says what to run, without the
// password.
func TestBackupOfPostgreSQLWithoutPgDump(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	to := filepath.Join(t.TempDir(), "freshgo.dump")
	code, stdout, stderr := runCLI(t, "backup", "-database-url", "postgres://alice:secret@db.example/freshgo?sslmode=disable", "-to", to)
	want := "pg_dump '--format=custom' '--file=" + to + "' '--dbname=postgres://alice@db.example/freshgo?sslmode=disable'"
	if code != 1 || stdout != "" || !strings.Contains(stderr, want) {
		t.Errorf("code %d, stdout %q, stderr %q; want 1 and %q", code, stdout, stderr, want)
	}
	if strings.Contains(stderr, "secret") {
		t.Errorf("the password is printed: %q", stderr)
	}
}

// pg_dump is given the password in its environment, not among its arguments.
func TestBackupOfPostgreSQLWithPgDump(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
for a; do case $a in --file=*) f=${a#--file=};; esac; done
{ echo "$@"; echo "PGPASSWORD=$PGPASSWORD"; } > "$f"
`
	if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("PGPASSWORD", "")
	to := filepath.Join(t.TempDir(), "freshgo.dump")
	code, stdout, stderr := runCLI(t, "backup", "-database-url", "postgres://alice:secret@db.example/freshgo", "-to", to)
	if code != 0 || stdout != "copied to "+to+"\n" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	data, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	want := "--format=custom --file=" + to + " --dbname=postgres://alice@db.example/freshgo\nPGPASSWORD=secret\n"
	if string(data) != want {
		t.Errorf("pg_dump got\n%q\nwant\n%q", data, want)
	}

	if code, _, stderr := runCLI(t, "backup", "-database-url", "postgres://alice:secret@db.example/freshgo", "-to", to); code != 1 || !strings.Contains(stderr, "already exists") {
		t.Errorf("backup over the dump: code %d, stderr %q; want a refusal", code, stderr)
	}
}
