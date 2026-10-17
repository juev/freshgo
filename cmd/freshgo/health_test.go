package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/store"
)

// The health check answers 200 while the database answers and 503 once it
// does not, and says when the feeds were last refreshed.
func TestHealth(t *testing.T) {
	db, err := store.Open(context.Background(), config.DriverSQLite, filepath.Join(t.TempDir(), "freshgo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	var refreshed atomic.Bool
	lastRun := func() (time.Time, bool) { return ended, refreshed.Load() }
	var logs strings.Builder
	server := httptest.NewServer(healthHandler(db.Ping, lastRun, slog.New(slog.NewTextHandler(&logs, nil))))
	defer server.Close()

	get := func() (int, map[string]any) {
		t.Helper()
		resp, err := http.Get(server.URL + healthPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	if status, body := get(); status != http.StatusOK || body["database"] != "ok" || body["lastRefresh"] != nil {
		t.Errorf("before a refresh: %d %v; want 200, database ok, no lastRefresh", status, body)
	}
	refreshed.Store(true)
	if status, body := get(); status != http.StatusOK || body["lastRefresh"] != "2026-10-17T09:30:00Z" {
		t.Errorf("after a refresh: %d %v; want 200, lastRefresh 2026-10-17T09:30:00Z", status, body)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if status, body := get(); status != http.StatusServiceUnavailable || body["database"] != "unreachable" {
		t.Errorf("database closed: %d %v; want 503, database unreachable", status, body)
	}
	if !strings.Contains(logs.String(), "database is closed") {
		t.Errorf("log %q, want the error of the database", logs.String())
	}

	resp, err := http.Post(server.URL+healthPath, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d, want 405", resp.StatusCode)
	}
}

// A database that does not answer in time fails the check; it does not hold
// it until the orchestrator gives up.
func TestHealthDatabaseTooSlow(t *testing.T) {
	hanging := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	server := httptest.NewServer(healthHandler(hanging, func() (time.Time, bool) { return time.Time{}, false }, slog.New(slog.DiscardHandler)))
	defer server.Close()
	began := time.Now()
	resp, err := http.Get(server.URL + healthPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || time.Since(began) > pingTimeout+time.Second {
		t.Errorf("hanging database: %d after %s; want 503 within %s", resp.StatusCode, time.Since(began), pingTimeout)
	}
}

// The healthcheck command succeeds while the server answers 200 and fails
// otherwise; a server listening on every interface is asked over loopback.
func TestHealthcheckCommand(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthPath {
			http.NotFound(w, r)
			return
		}
		if !healthy.Load() {
			http.Error(w, `{"database":"down"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"database":"ok"}`))
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	for _, listen := range []string{"127.0.0.1:" + port, "0.0.0.0:" + port, ":" + port} {
		code, stdout, stderr := runCLI(t, "healthcheck", "-listen", listen)
		if code != 0 || !strings.Contains(stdout, `"database":"ok"`) {
			t.Errorf("-listen %s, healthy: code %d, stdout %q, stderr %q; want 0", listen, code, stdout, stderr)
		}
	}
	healthy.Store(false)
	code, stdout, stderr := runCLI(t, "healthcheck", "-listen", "127.0.0.1:"+port)
	if code != 1 || !strings.Contains(stdout, "down") || !strings.Contains(stderr, "503") {
		t.Errorf("unhealthy: code %d, stdout %q, stderr %q; want 1 and the answer", code, stdout, stderr)
	}
	server.Close()
	if code, _, _ := runCLI(t, "healthcheck", "-listen", "127.0.0.1:"+port); code != 1 {
		t.Errorf("no server: code %d, want 1", code)
	}
}

