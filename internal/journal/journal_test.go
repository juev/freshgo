package journal

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// Warnings and errors that name a user are kept for the user; everything
// goes on to the log of the server as before.
func TestHandler(t *testing.T) {
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			driver, dsn := e.New(t)
			db, err := store.Open(ctx, driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			var out bytes.Buffer
			now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			h := New(slog.NewTextHandler(&out, nil), db)
			h.out.now = func() time.Time { return now }
			log := slog.New(h)

			if err := db.AddLog(ctx, &store.Log{Time: now.Add(-Kept - time.Hour).Unix(), Level: "WARN", User: "alice", Message: "old"}); err != nil {
				t.Fatal(err)
			}
			log.Info("feeds refreshed", "user", "alice")
			log.Warn("no user named")
			log.Warn("feed failed", "user", "alice", "feed", 3, "url", "http://x.example/a b", "error", errors.New("status 500\nsecond line"))
			log.Error("request failed", "path", "/x")
			log.With("user", "bob", "part", "icons").Error("broken\x00 text \xff", "n", 1)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			log.WarnContext(cancelled, "after the request ended", "user", "alice")

			// Logged inside a transaction, a record does not wait for it.
			started := time.Now()
			if err := db.InTx(ctx, func(tx *store.Store) error {
				log.Warn("inside a transaction", "user", "carol")
				return tx.DeleteLogs(ctx, "nobody")
			}); err != nil || time.Since(started) > 2*time.Second {
				t.Errorf("logging inside a transaction: %v, took %v", err, time.Since(started))
			}
			h.Close()
			log.Warn("after the journal was closed", "user", "alice")
			logs, err := db.Logs(ctx, store.LogQuery{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, l := range logs {
				if l.Time != now.Unix() {
					t.Errorf("record %q has the time %d", l.Message, l.Time)
				}
				got = append(got, l.Level+" "+l.User+": "+l.Message)
			}
			want := []string{
				"WARN carol: inside a transaction",
				"WARN alice: after the request ended",
				"ERROR bob: broken text � part=icons n=1",
				`WARN alice: feed failed feed=3 url="http://x.example/a b" error="status 500 second line"`,
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("records kept:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if text := out.String(); strings.Count(text, "\n") != 8 || !strings.Contains(text, "feeds refreshed") || !strings.Contains(text, "no user named") {
				t.Errorf("the log of the server:\n%s", text)
			}
			if alice, _ := db.Logs(ctx, store.LogQuery{User: "alice", Text: "FAILED"}); len(alice) != 1 {
				t.Errorf("records of alice about a failure: %d, want 1", len(alice))
			}
		})
	}
}
