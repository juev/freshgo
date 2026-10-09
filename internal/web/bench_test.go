package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// Pages of alice of the reference installation with 84 more feeds and 6023
// more entries of about 6 KB, three images each, one in 107 unread: the
// size of the library the comparison with FreshRSS was measured on. Run by
// `make bench`.
func BenchmarkPages(b *testing.B) {
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
	cookies := answer.Result().Cookies()

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
