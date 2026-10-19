package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/greader"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web"
)

// An open page hears of an entry read through the Google Reader API and of
// entries that came, and the server shuts down without waiting for the page.
func TestEventsReachOpenPages(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, config.DriverSQLite, filepath.Join(t.TempDir(), "freshgo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte("alice-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	alice := &store.User{Name: "alice", APIPasswordHash: string(hash)}
	if err := db.CreateUser(ctx, alice); err != nil {
		t.Fatal(err)
	}
	// Everybody is alice: the page needs no login.
	system := store.DefaultSystem()
	system.AuthType, system.DefaultUser = store.AuthNone, "alice"
	if err := db.SetSystem(ctx, system); err != nil {
		t.Fatal(err)
	}
	f := &store.Feed{UserID: alice.ID, URL: "https://example.org/feed", Name: "Feed"}
	if err := db.CreateFeed(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertEntries(ctx, alice.ID, []*store.Entry{{FeedID: f.ID, GUID: "1"}}); err != nil {
		t.Fatal(err)
	}

	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test"})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	registry := &hooks.Registry{}
	refresher := refresh.New(db, client, registry, log)
	broker := newEvents(db)
	api := greader.New(greader.Options{DB: db, Refresher: refresher, Hooks: registry, Log: log})
	pages, err := web.New(web.Options{DB: db, Refresher: refresher, Hooks: registry, Log: log, Version: "test", Events: broker})
	if err != nil {
		t.Fatal(err)
	}
	none := http.NotFoundHandler()
	server := newServer(ctx, routes(none, api, none, none, none, nil, pages), broker)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	base := "http://" + listener.Addr().String()

	resp, err := http.Get(base + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("GET /events: status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	expect := func(want string) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case line, open := <-lines:
				if !open {
					t.Fatalf("the stream ended before %q", want)
				}
				if line == want {
					return
				}
			case <-timeout:
				t.Fatalf("no %q within 5 s", want)
			}
		}
	}
	expect(": open")

	login, err := http.PostForm(base+"/accounts/ClientLogin", url.Values{"Email": {"alice"}, "Passwd": {"alice-password"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(login.Body)
	_ = login.Body.Close()
	_, token, found := strings.Cut(string(body), "Auth=")
	if !found {
		t.Fatalf("ClientLogin: %s", body)
	}
	ids, err := db.ListEntryIDs(ctx, alice.ID, store.EntryQuery{})
	if err != nil || len(ids) != 1 {
		t.Fatalf("entries: %v, %v", ids, err)
	}
	edit, err := http.NewRequest(http.MethodPost, base+"/reader/api/0/edit-tag",
		strings.NewReader("a=user/-/state/com.google/read&i="+strconv.FormatInt(ids[0], 10)))
	if err != nil {
		t.Fatal(err)
	}
	edit.Header.Set("Authorization", "GoogleLogin auth="+strings.TrimSpace(token))
	edit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	answer, err := http.DefaultClient.Do(edit)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(answer.Body)
	_ = answer.Body.Close()
	if answer.StatusCode != http.StatusOK {
		t.Fatalf("edit-tag: status %d, %s", answer.StatusCode, body)
	}
	expect("event: tree")

	if err := db.InsertEntries(ctx, alice.ID, []*store.Entry{{FeedID: f.ID, GUID: "2"}}); err != nil {
		t.Fatal(err)
	}
	expect("event: entries")

	stop, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	began := time.Now()
	if err := server.Shutdown(stop); err != nil {
		t.Fatalf("Shutdown: %v after %v", err, time.Since(began))
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("Serve: %v", err)
	}
	for range lines {
	}
}
