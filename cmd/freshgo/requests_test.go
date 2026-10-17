package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Every request is counted by its status. At the level debug it is logged
// without its query as well; above it none is.
func TestObserve(t *testing.T) {
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/stream":
			_, _ = io.WriteString(w, "first")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush: %v", err)
			}
		}
	})
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		requests := requestCounter()
		var logs strings.Builder
		server := httptest.NewServer(observe(page, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: level})), requests))
		for _, path := range []string{"/missing?token=secret", "/stream", "/empty"} {
			resp, err := http.Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}
		server.Close()
		if found, ok := testutil.ToFloat64(requests.WithLabelValues("404")), testutil.ToFloat64(requests.WithLabelValues("200")); found != 1 || ok != 2 {
			t.Errorf("level %s: counted %v answers 404 and %v answers 200, want 1 and 2", level, found, ok)
		}
		text := logs.String()
		if level > slog.LevelDebug {
			if text != "" {
				t.Errorf("level %s: log %q, want nothing", level, text)
			}
			continue
		}
		lines := strings.Split(strings.TrimSpace(text), "\n")
		want := []string{"method=GET path=/missing status=404", "method=GET path=/stream status=200", "method=GET path=/empty status=200"}
		if len(lines) != len(want) {
			t.Fatalf("log:\n%s\nwant %d lines", text, len(want))
		}
		for i, line := range lines {
			if !strings.Contains(line, "msg=request "+want[i]+" duration=") {
				t.Errorf("line %d: %q, want %q and a duration", i, line, want[i])
			}
		}
		if strings.Contains(text, "secret") {
			t.Errorf("the log has the query:\n%s", text)
		}
	}
}
