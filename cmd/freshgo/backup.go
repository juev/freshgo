package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/store"
)

// runBackup copies the database while the server keeps running: SQLite by
// itself, PostgreSQL with pg_dump.
func runBackup(ctx context.Context, e env, args []string) (err error) {
	flags, conf := newFlagSet(e, "backup")
	to := flags.String("to", "", "file to write the copy to; an existing file is refused (required)")
	driver, dsn, err := parseDatabase(flags, conf, args, 0)
	if err != nil {
		return err
	}
	if *to == "" {
		return errors.New("-to is required")
	}
	if driver == config.DriverPostgres {
		return pgDump(ctx, e, dsn, *to)
	}
	db, err := store.Open(ctx, driver, dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if err := db.Backup(ctx, *to); err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "copied to %s\n", *to)
	return err
}

// pgDump runs pg_dump into a file. Without pg_dump on PATH it fails with the
// command to run, so that a scheduled job does not go on doing nothing. The
// password goes in the environment of pg_dump, where the list of processes
// does not show it, and is left out of the command printed.
func pgDump(ctx context.Context, e env, dsn, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists", to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("database URL: %w", err)
	}
	var password string
	if u.User != nil {
		password, _ = u.User.Password()
		u.User = url.User(u.User.Username())
	}
	if q := u.Query(); q.Has("password") {
		password = q.Get("password")
		q.Del("password")
		u.RawQuery = q.Encode()
	}
	args := []string{"--format=custom", "--file=" + to, "--dbname=" + u.String()}
	program, err := exec.LookPath("pg_dump")
	if err != nil {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = shellQuote(a)
		}
		return fmt.Errorf("pg_dump is not on PATH; install the client of PostgreSQL or run where it is, with the password in PGPASSWORD or ~/.pgpass:\n  pg_dump %s", strings.Join(quoted, " "))
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdout, cmd.Stderr = e.stdout, e.stderr
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	_, err = fmt.Fprintf(e.stdout, "copied to %s\n", to)
	return err
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runRestore checks a copy made by backup without touching the database the
// server is set up with. Putting a copy in place is left to the
// administrator: the server is stopped and the file copied over.
func runRestore(ctx context.Context, e env, args []string) error {
	flags := flag.NewFlagSet("freshgo restore", flag.ContinueOnError)
	flags.SetOutput(e.stderr)
	synopsis(flags, e, "restore -check <file>")
	check := flags.Bool("check", false, "check that the server can start on a SQLite copy, and count what it holds (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*check {
		return errors.New("only -check is supported: to restore, stop the server and copy the file over the database")
	}
	path, err := operand(flags, "the file to check")
	if err != nil {
		return err
	}
	report, err := store.CheckBackup(ctx, path)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s: schema version %d, %d users, %d feeds, %d entries", path, report.Version, report.Users, report.Feeds, report.Entries)
	if report.Version < report.Supported {
		line += fmt.Sprintf("; the server brings it to version %d at start", report.Supported)
	}
	_, err = fmt.Fprintln(e.stdout, line)
	return err
}
