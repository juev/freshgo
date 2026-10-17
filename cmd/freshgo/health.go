package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// healthPath answers whether the server works, without a login.
const healthPath = "/healthz"

// pingTimeout is how long the database has to answer a health check: an
// orchestrator gives the whole check a few seconds.
const pingTimeout = 2 * time.Second

// health is what healthPath answers.
type health struct {
	// Database is "ok" or "unreachable". What went wrong is in the log:
	// the answer is for anybody, and the error may name the host and the
	// user of the database.
	Database string `json:"database"`
	// LastRefresh is when the last refresh run that reached every user
	// ended, absent before the first.
	LastRefresh *time.Time `json:"lastRefresh,omitempty"`
}

// healthHandler answers 200 while the database answers ping, 503 when it
// does not. lastRun says when the last refresh run ended.
func healthHandler(ping func(context.Context) error, lastRun func() (time.Time, bool), log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		h := health{Database: "ok"}
		if at, ok := lastRun(); ok {
			at = at.UTC()
			h.LastRefresh = &at
		}
		status := http.StatusOK
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()
		if err := ping(ctx); err != nil {
			log.Error("health check: the database does not answer", "error", err)
			h.Database = "unreachable"
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(h)
	})
}

// healthcheckTimeout is how long the healthcheck command waits for the
// answer of the server.
const healthcheckTimeout = 5 * time.Second

// runHealthcheck asks the server listening at -listen whether it works, for
// the HEALTHCHECK of the image: it has no other program to ask with. An
// address that listens on every interface is asked over loopback.
func runHealthcheck(ctx context.Context, e env, args []string) error {
	fs, conf := newFlagSet(e, "healthcheck")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	host, port, err := net.SplitHostPort(conf.Listen)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", conf.Listen, err)
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip != nil && ip.To4() == nil {
			host = "::1"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, healthcheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+healthPath, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if _, err := e.stdout.Write(body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return nil
}
