// Package minifluxtest is a Miniflux server for tests: it answers the REST
// API the importer reads, in the shapes of https://miniflux.app/docs/api.html,
// from an account the test can change.
package minifluxtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Token is the API token the server accepts.
const Token = "test-token"

// Entries is how many entries the account has: more than two pages of the
// importer.
const Entries = 230

// Server is a Miniflux server on this machine.
type Server struct {
	// URL is its address; Host is the same without the scheme, as the
	// allowlist of a fetch client names it.
	URL, Host string

	mu       sync.Mutex
	feeds    []map[string]any
	entries  []map[string]any
	failFrom int
	requests []string
}

// New starts a server that lives as long as the test. The account:
//
//   - categories All (1), News (2), Quiet (3, hidden from the global lists)
//     and Empty (4, without feeds);
//   - feed 1 "Blog" in News with the crawler, scraper rules, a user agent, a
//     cookie and credentials; feed 2 "Plain" in All with rewrite, block and
//     keep rules, which freshgo has no equivalent for; feed 3 "Paused",
//     disabled, in Quiet, with the crawler and no scraper rules;
//   - entries 1 to Entries over the three feeds: the even ones read, every
//     tenth starred, entry 5 tagged "go" and "rss", every 25th tagged "go",
//     entry 7 without an address; entry Entries+1 is removed.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	categories := map[int]map[string]any{}
	for id, c := range map[int]struct {
		title  string
		hidden bool
	}{1: {"All", false}, 2: {"News", false}, 3: {"Quiet", true}, 4: {"Empty", false}} {
		categories[id] = map[string]any{"id": id, "user_id": 1, "title": c.title, "hide_globally": c.hidden}
	}
	feed := func(id int, title, category string, extra map[string]any) map[string]any {
		cat := 1
		switch category {
		case "News":
			cat = 2
		case "Quiet":
			cat = 3
		}
		f := map[string]any{
			"id": id, "user_id": 1, "title": title, "site_url": fmt.Sprintf("https://site%d.example/", id),
			"feed_url": fmt.Sprintf("https://site%d.example/feed.xml", id), "checked_at": "2026-10-01T10:00:00Z",
			"etag_header": "", "last_modified_header": "", "parsing_error_message": "", "parsing_error_count": 0,
			"scraper_rules": "", "rewrite_rules": "", "blocklist_rules": "", "keeplist_rules": "", "user_agent": "",
			"username": "", "password": "", "crawler": false, "disabled": false, "ignore_http_cache": false,
			"fetch_via_proxy": false, "category": categories[cat], "icon": nil,
		}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	s.feeds = []map[string]any{
		feed(1, "Blog", "News", map[string]any{
			"crawler": true, "scraper_rules": "article.post", "user_agent": "Bot/1", "cookie": "a=b",
			"username": "reader", "password": "secret",
		}),
		feed(2, "Plain", "All", map[string]any{
			"rewrite_rules": "add_dynamic_image", "blocklist_rules": "spam", "keeplist_rules": "news", "fetch_via_proxy": true,
		}),
		feed(3, "Paused", "Quiet", map[string]any{"disabled": true, "crawler": true}),
	}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.FixedZone("", 3*3600))
	for i := 1; i <= Entries+1; i++ {
		feedID := i%3 + 1
		e := map[string]any{
			"id": i, "user_id": 1, "feed_id": feedID, "status": "unread", "hash": fmt.Sprintf("hash%03d", i),
			"title": fmt.Sprintf("Entry %d", i), "url": fmt.Sprintf("https://site%d.example/post/%d", feedID, i),
			"comments_url": "", "published_at": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"created_at": "2026-10-01T10:00:00Z", "changed_at": "2026-10-01T10:00:00Z", "content": fmt.Sprintf("<p>Body %d</p>", i),
			"author": "Ann", "share_code": "", "starred": false, "reading_time": 1, "enclosures": nil, "tags": []string{},
			"feed": s.feeds[feedID-1],
		}
		if i%2 == 0 {
			e["status"] = "read"
		}
		if i%10 == 0 {
			e["starred"] = true
		}
		if i%25 == 0 {
			e["tags"] = []string{"go"}
		}
		if i == 5 {
			e["tags"] = []string{"go", "rss"}
		}
		if i == 7 {
			e["url"] = ""
		}
		if i == Entries+1 {
			e["status"] = "removed"
		}
		s.entries = append(s.entries, e)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", s.handle(func(http.ResponseWriter, *http.Request) any {
		return map[string]any{"id": 1, "username": "alice", "is_admin": false, "entries_per_page": 100}
	}))
	mux.HandleFunc("GET /v1/categories", s.handle(func(http.ResponseWriter, *http.Request) any {
		list := []map[string]any{}
		for id := 1; id <= len(categories); id++ {
			list = append(list, categories[id])
		}
		return list
	}))
	mux.HandleFunc("GET /v1/feeds", s.handle(func(http.ResponseWriter, *http.Request) any { return s.feeds }))
	mux.HandleFunc("GET /v1/entries", s.handle(s.page))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	s.URL = server.URL
	s.Host = server.Listener.Addr().String()
	return s
}

// FailEntriesFrom makes the server answer 500 to the nth request for
// entries and every one after it; zero is never.
func (s *Server) FailEntriesFrom(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failFrom = n
}

// Requests returns the paths and queries asked for so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.requests...)
}

// Change edits the entry with the id: its state, whether it is starred, and
// its tags.
func (s *Server) Change(id int, status string, starred bool, tags ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id-1]
	e["status"], e["starred"], e["tags"] = status, starred, append([]string{}, tags...)
}

func (s *Server) handle(answer func(http.ResponseWriter, *http.Request) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Auth-Token") != Token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error_message":"Access Unauthorized"}`))
			return
		}
		body := answer(w, r)
		if body == nil {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(body)
	}
}

// page answers a request for entries the way the API does: by the filters
// of the query, in the order asked for, with the total over all pages.
func (s *Server) page(w http.ResponseWriter, r *http.Request) any {
	s.mu.Lock()
	n := 0
	for _, path := range s.requests {
		if len(path) >= 11 && path[:11] == "/v1/entries" {
			n++
		}
	}
	failing := s.failFrom > 0 && n >= s.failFrom
	s.mu.Unlock()
	if failing {
		http.Error(w, `{"error_message":"Internal error"}`, http.StatusInternalServerError)
		return nil
	}
	q := r.URL.Query()
	if q.Get("order") != "id" || q.Get("direction") != "asc" {
		http.Error(w, `{"error_message":"the importer pages by id, ascending"}`, http.StatusBadRequest)
		return nil
	}
	wanted := map[string]bool{}
	for _, status := range q["status"] {
		wanted[status] = true
	}
	after, _ := strconv.Atoi(q.Get("after_entry_id"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []map[string]any{}
	total := 0
	for _, e := range s.entries {
		if !wanted[e["status"].(string)] {
			continue
		}
		total++
		if e["id"].(int) > after && len(list) < limit {
			list = append(list, e)
		}
	}
	return map[string]any{"total": total, "entries": list}
}
