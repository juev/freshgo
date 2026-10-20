package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// referenceSite is the interface over the reference installation, and the
// cookies of alice logged in to it.
func referenceSite(b *testing.B) (*store.Store, *Handler, *store.User, []*http.Cookie) {
	b.Helper()
	ctx := context.Background()
	driver, dsn := storetest.Engines()[0].New(b)
	db, err := store.Open(ctx, driver, dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if _, err := importer.Run(ctx, db, importer.Options{DataDir: referenceData}); err != nil {
		b.Fatal(err)
	}
	u, err := db.UserByName(ctx, "alice")
	if err != nil {
		b.Fatal(err)
	}
	client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
	if err != nil {
		b.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := &hooks.Registry{}
	h, err := New(Options{DB: db, Log: log, Version: "1.2.3", Hooks: registry, Refresher: refresh.New(db, client, registry, log)})
	if err != nil {
		b.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"username": {"alice"}, "password": {"alice-web-password"}}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Sec-Fetch-Site", "same-origin")
	answer := httptest.NewRecorder()
	h.ServeHTTP(answer, login)
	if answer.Code != http.StatusSeeOther {
		b.Fatalf("login: status %d", answer.Code)
	}
	return db, h, u, answer.Result().Cookies()
}

// Pages of alice of the reference installation with 84 more feeds and 6023
// more entries of about 6 KB, three images each, one in 107 unread: the
// size of the library the comparison with FreshRSS was measured on. Run by
// `make bench`.
func BenchmarkPages(b *testing.B) {
	ctx := context.Background()
	db, h, u, cookies := referenceSite(b)
	feeds := make([]*store.Feed, 84)
	for i := range feeds {
		feeds[i] = &store.Feed{UserID: u.ID, URL: fmt.Sprintf("https://bench.example.org/%d", i), Name: fmt.Sprintf("Feed %d", i), Priority: priorityMain}
		if err := db.CreateFeed(ctx, feeds[i]); err != nil {
			b.Fatal(err)
		}
	}
	paragraphs := strings.Repeat(`<p>Сегодня вышла новая версия, <a href="https://example.org/a?x=1&amp;y=2">подробности</a> — "в статье". The quick brown fox jumps over the lazy dog and more text here.</p>`+"\n", 9)
	text := strings.Repeat(paragraphs+`<p><img src="https://example.org/i.png" alt="x" width="600"></p>`+"\n", 3)
	entries := make([]*store.Entry, 6023)
	for i := range entries {
		address := fmt.Sprintf("https://bench.example.org/e/%d", i)
		entries[i] = &store.Entry{
			FeedID: feeds[i%len(feeds)].ID, GUID: address, Title: fmt.Sprintf("Entry %d", i), Authors: []string{"Somebody"},
			Content: text, Link: address, Published: 1_760_000_000 + int64(i), IsRead: i%107 != 0, Tags: []string{"news", "go"},
		}
	}
	if err := db.InsertEntries(ctx, u.ID, entries); err != nil {
		b.Fatal(err)
	}

	for _, tc := range []struct{ name, path string }{
		{"main page of 20 entries", "/?state=all"},
		{"one entry", fmt.Sprintf("/entries/%d", entries[len(entries)-1].ID)},
		{"subscriptions", "/subscriptions"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				r := httptest.NewRequest(http.MethodGet, tc.path, nil)
				for _, c := range cookies {
					r.AddCookie(c)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					b.Fatalf("status %d: %.300s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// Exports of feeds of 1000 and of 3000 entries of 30 KB, one document each.
// What the heap grows by stays the same however many entries go out: they
// are written to a file, not kept in memory. Run by `make bench`.
func BenchmarkExport(b *testing.B) {
	ctx := context.Background()
	db, h, u, cookies := referenceSite(b)
	text := strings.Repeat("<p>The quick brown fox jumps over the lazy dog, and more text here.</p>\n", 410)
	for _, n := range []int{1000, 3000} {
		feed := &store.Feed{UserID: u.ID, URL: fmt.Sprintf("https://bench.example.org/%d", n), Name: fmt.Sprintf("Feed %d", n), Priority: priorityMain}
		if err := db.CreateFeed(ctx, feed); err != nil {
			b.Fatal(err)
		}
		entries := make([]*store.Entry, n)
		for i := range entries {
			address := fmt.Sprintf("https://bench.example.org/%d/%d", n, i)
			entries[i] = &store.Entry{FeedID: feed.ID, GUID: address, Title: fmt.Sprintf("Entry %d", i), Content: text, Link: address, Published: 1_760_000_000 + int64(i)}
		}
		if err := db.InsertEntries(ctx, u.ID, entries); err != nil {
			b.Fatal(err)
		}
		form := url.Values{"feed": {strconv.FormatInt(feed.ID, 10)}}.Encode()
		b.Run(fmt.Sprintf("%d entries", n), func(b *testing.B) {
			var growth uint64
			for b.Loop() {
				runtime.GC()
				done, measured := make(chan struct{}), make(chan uint64)
				go func() { measured <- heapGrowth(done) }()
				r := httptest.NewRequest(http.MethodPost, "/subscriptions/export", strings.NewReader(form))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Sec-Fetch-Site", "same-origin")
				for _, c := range cookies {
					r.AddCookie(c)
				}
				w := &counted{header: http.Header{}}
				h.ServeHTTP(w, r)
				close(done)
				growth = max(growth, <-measured)
				if w.status != http.StatusOK || w.bytes < int64(n)*30_000 {
					b.Fatalf("status %d, %d bytes", w.status, w.bytes)
				}
			}
			b.ReportMetric(float64(growth)/(1<<20), "heap-growth-MB")
		})
	}
}

// heapGrowth is the most the heap grew by until done is closed.
func heapGrowth(done <-chan struct{}) uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	base, peak := sample[0].Value.Uint64(), uint64(0)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		metrics.Read(sample)
		peak = max(peak, sample[0].Value.Uint64())
		select {
		case <-done:
			return peak - min(base, peak)
		case <-tick.C:
		}
	}
}

// counted is an answer that keeps only how long it is: a recorder would
// hold the whole export in memory, and the peak would be its own.
type counted struct {
	header http.Header
	status int
	bytes  int64
}

func (c *counted) Header() http.Header { return c.header }

func (c *counted) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *counted) Write(p []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	c.bytes += int64(len(p))
	return len(p), nil
}
