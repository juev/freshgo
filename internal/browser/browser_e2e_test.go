//go:build e2e

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// chrome starts a headless Chrome that listens for the DevTools protocol
// and returns the address to name it by.
func chrome(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	at := listener.Addr().String()
	_, port, _ := net.SplitHostPort(at)
	_ = listener.Close()
	options := append(chromedp.DefaultExecAllocatorOptions[:], remote.WebSocket, chromedp.Flag("remote-debugging-port", port))
	allocator, cancel := chromedp.NewExecAllocator(context.Background(), options...)
	t.Cleanup(cancel)
	ctx, cancel := chromedp.NewContext(allocator)
	t.Cleanup(cancel)
	// Chrome is asked to close before its context is cancelled. A Chrome that
	// is only killed leaves the clone of its application on the disk, 1.4 GB
	// of it at every start on macOS.
	t.Cleanup(func() {
		if err := chromedp.Cancel(ctx); err != nil {
			t.Logf("Chrome does not close: %v", err)
		}
	})
	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("Chrome does not start: %v", err)
	}
	return "ws://" + at
}

// A page is read as a browser leaves it: scripts run, where it sends on is
// followed, a check that lets the browser through is waited for, and one
// that does not is an error of its own. Every page has a tab that knows
// nothing of the pages before it and is closed after it.
func TestE2EPage(t *testing.T) {
	mux := http.NewServeMux()
	page := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(body))
		})
	}
	page("/article", `<html><head><title>Boats</title></head><body><article><p>From the server.</p></article>`+
		`<script>document.querySelector('article').insertAdjacentHTML('beforeend', '<p>Added by a script.</p>')</script></body></html>`)
	page("/moved", `<html><body><script>location.replace('/article')</script></body></html>`)
	page("/check", `<html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt = {};`+
		`setTimeout(() => location.replace('/article'), 700)</script></body></html>`)
	page("/stuck", `<html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt = {}</script></body></html>`)
	mux.HandleFunc("/visit", func(w http.ResponseWriter, r *http.Request) {
		said := "First visit."
		if _, err := r.Cookie("seen"); err == nil {
			said = "Seen before."
		}
		http.SetCookie(w, &http.Cookie{Name: "seen", Value: "1", Path: "/", MaxAge: 600})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><p>` + said + `</p></body></html>`))
	})
	page("/large", `<html><body><p>`+strings.Repeat("word ", 400)+`</p></body></html>`)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	address := chrome(t)
	b, err := New(address)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, path := range []string{"/article", "/moved", "/check"} {
		final, markup, err := b.Page(ctx, server.URL+path)
		if err != nil || final != server.URL+"/article" || !strings.Contains(markup, "From the server.") || !strings.Contains(markup, "Added by a script.") {
			t.Errorf("%s: ended at %q, %v\n%s", path, final, err, markup)
		}
	}
	// A page finds nothing that an earlier one left in the browser.
	for visit := 1; visit <= 2; visit++ {
		if _, markup, err := b.Page(ctx, server.URL+"/visit"); err != nil || !strings.Contains(markup, "First visit.") {
			t.Errorf("visit %d: %v\n%s", visit, err, markup)
		}
	}
	// The time of a page is cut short for the one that never ends.
	b.timeout = 3 * time.Second
	started := time.Now()
	if _, _, err := b.Page(ctx, server.URL+"/stuck"); !errors.Is(err, ErrChallenge) || time.Since(started) < b.timeout {
		t.Errorf("a page that stays behind its check: %v after %s", err, time.Since(started))
	}
	b.timeout = pageTimeout
	b.limit = 1000
	if _, _, err := b.Page(ctx, server.URL+"/large"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a page over the limit: %v", err)
	}
	b.limit = maxPage
	if _, _, err := b.Page(ctx, "http://127.0.0.1:1/nothing"); err == nil || errors.Is(err, ErrChallenge) {
		t.Errorf("a page that cannot be reached: %v", err)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := b.Page(gone, server.URL+"/article"); !errors.Is(err, context.Canceled) {
		t.Errorf("a request that was given up: %v", err)
	}

	// No tab is left open.
	resp, err := http.Get(strings.Replace(address, "ws://", "http://", 1) + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var targets []struct{ Type, URL string }
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.Type == "page" && strings.HasPrefix(target.URL, "http") {
			t.Errorf("a tab is left open: %s", fmt.Sprint(target))
		}
	}
}
