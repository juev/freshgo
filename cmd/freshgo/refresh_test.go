package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/store"
)

const testFeed = `<?xml version="1.0"?><rss version="2.0"><channel><title>Blog</title>
<item><guid>a</guid><title>First</title><description>one</description></item>
<item><guid>b</guid><title>Second</title><description>two</description></item>
</channel></rss>`

// subscribed returns a database with one user subscribed to a feed on a
// local server, and the number of times the feed was requested.
func subscribed(t *testing.T) (database, host string, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "freshgo/") {
			http.Error(w, "unexpected User-Agent", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, testFeed)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	db, err := store.Open(ctx, config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	u := &store.User{Name: "alice"}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateFeed(ctx, &store.Feed{UserID: u.ID, URL: server.URL + "/feed", Name: "Blog"}); err != nil {
		t.Fatal(err)
	}
	return "sqlite://" + path, strings.TrimPrefix(server.URL, "http://"), hits
}

func TestRefresh(t *testing.T) {
	database, host, hits := subscribed(t)

	// The feed is on a loopback address, which is refused unless allowed.
	code, stdout, stderr := runCLI(t, "refresh", "-database-url", database)
	if code != 0 || stdout != "alice: 0 feeds refreshed, 1 failed, 0 new and 0 updated entries\n" {
		t.Errorf("refresh without the allowlist: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "address is not allowed") || hits.Load() != 0 {
		t.Errorf("stderr %q, %d requests; want the refusal logged and no request", stderr, hits.Load())
	}

	code, stdout, stderr = runCLI(t, "refresh", "-database-url", database, "-fetch-allowlist", host)
	if code != 0 || stdout != "alice: 1 feeds refreshed, 0 failed, 2 new and 0 updated entries\n" {
		t.Errorf("refresh: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	// Refreshed a moment ago: not due, unless forced.
	code, stdout, _ = runCLI(t, "refresh", "-database-url", database, "-fetch-allowlist", host)
	if code != 0 || stdout != "alice: 0 feeds refreshed, 0 failed, 0 new and 0 updated entries\n" || hits.Load() != 1 {
		t.Errorf("second refresh: code %d, stdout %q, %d requests", code, stdout, hits.Load())
	}
	code, stdout, _ = runCLI(t, "refresh", "-database-url", database, "-fetch-allowlist", host, "-force")
	if code != 0 || stdout != "alice: 1 feeds refreshed, 0 failed, 0 new and 0 updated entries\n" || hits.Load() != 2 {
		t.Errorf("forced refresh: code %d, stdout %q, %d requests", code, stdout, hits.Load())
	}

	if code, _, stderr := runCLI(t, "refresh", "-database-url", database, "-fetch-allowlist", "10.0.0.0/40"); code != 1 || stderr == "" {
		t.Errorf("refresh with a malformed allowlist: code %d, stderr %q", code, stderr)
	}
}

// syncBuffer is a buffer the command writes to while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServeRefreshesUntilStopped(t *testing.T) {
	database, host, hits := subscribed(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, env{stdout: io.Discard, stderr: &stderr, getenv: func(string) string { return "" }},
			[]string{"serve", "-database-url", database, "-fetch-allowlist", host, "-refresh-interval", "10ms"})
	}()

	deadline := time.After(10 * time.Second)
	for hits.Load() == 0 || !strings.Contains(stderr.String(), "new=2") {
		select {
		case code := <-done:
			t.Fatalf("serve exited with code %d: %s", code, stderr.String())
		case <-deadline:
			t.Fatalf("serve did not refresh the feed in 10 s: %s", stderr.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve exited with code %d after the stop signal: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestPurge(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	db, err := store.Open(ctx, config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the two entries listed most recently, whatever their state.
	alice := &store.User{Name: "alice", Settings: []byte(`{"archiving":{"keep_period":false,"keep_max":2,"keep_min":0}}`)}
	bob := &store.User{Name: "bob"}
	for _, u := range []*store.User{alice, bob} {
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		f := &store.Feed{UserID: u.ID, URL: "https://example.org/feed", Name: "Blog"}
		if err := db.CreateFeed(ctx, f); err != nil {
			t.Fatal(err)
		}
		var entries []*store.Entry
		for i, guid := range []string{"a", "b", "c", "d", "e"} {
			entries = append(entries, &store.Entry{FeedID: f.ID, GUID: guid, LastSeen: int64(1000 + i)})
		}
		if err := db.InsertEntries(ctx, u.ID, entries); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// bob has the default settings, which keep a feed this small whole.
	code, stdout, stderr := runCLI(t, "purge", "-database-url", "sqlite://"+path)
	if code != 0 || stdout != "alice: 3 entries deleted\nbob: 0 entries deleted\n" {
		t.Errorf("purge: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, _ = runCLI(t, "purge", "-database-url", "sqlite://"+path)
	if code != 0 || stdout != "alice: 0 entries deleted\nbob: 0 entries deleted\n" {
		t.Errorf("second purge: code %d, stdout %q", code, stdout)
	}
}
