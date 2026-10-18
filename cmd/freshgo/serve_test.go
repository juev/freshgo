package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
)

// Every path goes to the part of the server it belongs to, and the address
// of a feed inside a path survives the trip.
func TestRoutes(t *testing.T) {
	part := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, name+" "+r.RequestURI)
		})
	}
	registry := &hooks.Registry{}
	registry.HandleAPI("Share By Mail", part("extension"))
	server := httptest.NewServer(routes(part("health"), part("api"), part("fever"), part("icons"), extensions(registry), part("hubs"), part("pages")))
	defer server.Close()
	// Without WebSub its addresses do not exist.
	off := httptest.NewServer(routes(part("health"), part("api"), part("fever"), part("icons"), extensions(registry), nil, part("pages")))
	defer off.Close()
	if resp, err := http.Get(off.URL + "/websub/abc"); err != nil {
		t.Errorf("GET /websub/abc with WebSub off: %v", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != "pages /websub/abc" {
			t.Errorf("GET /websub/abc with WebSub off: %q, want it left to the web interface", body)
		}
	}

	for path, want := range map[string]struct {
		status int
		body   string
	}{
		"/accounts/ClientLogin":                   {200, "api /accounts/ClientLogin"},
		"/reader/api/0/token":                     {200, "api /reader/api/0/token"},
		"/api/greader.php":                        {200, "api /api/greader.php"},
		"/api/greader.php/reader/api/0/token?x=1": {200, "api /api/greader.php/reader/api/0/token?x=1"},
		"/reader/api/0/stream/contents/feed/http%3A%2F%2Fexample.org%2F%2Ffeed": {200, "api /reader/api/0/stream/contents/feed/http%3A%2F%2Fexample.org%2F%2Ffeed"},
		"/websub/abc?hub.mode=subscribe":                                        {200, "hubs /websub/abc?hub.mode=subscribe"},
		"/favicon/0123":                                                         {200, "icons /favicon/0123"},
		"/api/misc.php/Share%20By%20Mail/send?to=x":                             {200, "extension /api/misc.php/Share%20By%20Mail/send?to=x"},
		"/api/misc.php?ext=Share+By+Mail":                                       {200, "extension /api/misc.php?ext=Share+By+Mail"},
		"/api/misc.php/Unknown":                                                 {404, "Not Found!"},
		"/api/misc.php":                                                         {400, "Bad Request!"},
		"/api/misc.php/":                                                        {400, "Bad Request!"},
		"/healthz":                                                              {200, "health /healthz"},
		"/healthz/x":                                                            {200, "pages /healthz/x"},
		"/":                                                                     {200, "pages /"},
		"/api/greader.phpx":                                                     {200, "pages /api/greader.phpx"},
		"/api/fever.php":                                                        {200, "fever /api/fever.php"},
		"/api/fever.php?api&unread_item_ids":                                    {200, "fever /api/fever.php?api&unread_item_ids"},
		"/api/fever.phpx":                                                       {200, "pages /api/fever.phpx"},
		"/readers":                                                              {200, "pages /readers"},
		"/static/app.css":                                                       {200, "pages /static/app.css"},
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != want.status || string(body) != want.body {
			t.Errorf("GET %s: status %d, body %q; want %d, %q", path, resp.StatusCode, body, want.status, want.body)
		}
	}
}

// The debug address hands out the metrics without a login, next to the
// profiles.
func TestDebugHandler(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, config.DriverSQLite, filepath.Join(t.TempDir(), "freshgo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test"})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	refresher := refresh.New(db, client, &hooks.Registry{}, log)
	requests := requestCounter()
	requests.WithLabelValues("200").Add(3)
	server := httptest.NewServer(debugHandler(newMetrics(db, refresher, requests, log)))
	defer server.Close()

	resp, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: %d", resp.StatusCode)
	}
	for _, want := range []string{
		`freshgo_http_requests_total{code="200"} 3`,
		`freshgo_refresh_feed_duration_seconds_count{result="failed"} 0`,
		`freshgo_refresh_entries_total{change="new"} 0`,
		"freshgo_refresh_last_run_timestamp_seconds 0",
		"freshgo_database_size_bytes ",
		"go_goroutines ",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the metrics have no %q", want)
		}
	}
	if strings.Contains(string(body), "freshgo_database_size_bytes NaN") {
		t.Error("the size of the database is unknown")
	}
	if resp, err := http.Get(server.URL + "/debug/pprof/"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("GET /debug/pprof/: %v, %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}
}
