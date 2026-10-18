// Package ttrsstest is a Tiny Tiny RSS server for tests: it answers the JSON
// API the importer reads, in the shapes of classes/API.php of tt-rss, from an
// account the test can change.
package ttrsstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// User and Password are the login the server accepts.
const (
	User     = "alice"
	Password = "secret"
)

// Entries is how many articles the account has: more than one page of 200,
// the most the API gives at a time.
const Entries = 230

// MaxPage is the most headlines the server gives in one answer.
const MaxPage = 200

// Server is a Tiny Tiny RSS server on this machine.
type Server struct {
	// URL is its address, as a user spells it: the installation, not the
	// API; Host is the same without the scheme, as the allowlist of a fetch
	// client names it.
	URL, Host string

	mu        sync.Mutex
	headlines []map[string]any
	disabled  bool
	failFrom  int
	requests  []Request
	logouts   int
}

// Request is a call the server received.
type Request struct {
	// Op is the lowercased name of the call.
	Op     string
	Params map[string]any
}

// New starts a server that lives as long as the test. The account:
//
//   - categories News (1), Tech (2, inside News) and Empty (3, without
//     feeds), and the feeds outside any category;
//   - feed 1 "Blog" in News; feed 2 "Sub" in Tech, refreshed every hour;
//     feed 3 "Plain" outside any category; feed 4 "Paused" in News, with
//     updates disabled;
//   - articles 1 to Entries over the four feeds: the odd ones unread, every
//     tenth starred, article 5 labelled "go" and "later", every 25th
//     labelled "go", article 3 published, article 4 with a note, article 6
//     with a tag, article 7 without a link.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= Entries; i++ {
		feed := i%4 + 1
		h := map[string]any{
			"id": i, "guid": fmt.Sprintf(`{"ver":2,"uid":1,"hash":"SHA1:%040d"}`, i),
			"unread": i%2 == 1, "marked": i%10 == 0, "published": i == 3,
			"updated": base.Add(time.Duration(i) * time.Minute).Unix(), "is_updated": false,
			"title": fmt.Sprintf("Entry %d", i), "link": fmt.Sprintf("https://site%d.example/post/%d", feed, i),
			"feed_id": feed, "tags": []string{}, "labels": []any{}, "feed_title": fmt.Sprintf("Feed %d", feed),
			"comments_count": 0, "comments_link": "", "always_display_attachments": false,
			"author": "Ann", "score": 0, "note": nil, "lang": "en", "site_url": fmt.Sprintf("https://site%d.example/", feed),
			"content": fmt.Sprintf("<p>Body %d</p>", i),
		}
		label := func(name string) []any { return []any{-11, name, "", ""} }
		if i%25 == 0 {
			h["labels"] = []any{label("go")}
		}
		if i == 5 {
			h["labels"] = []any{label("go"), label("later")}
		}
		if i == 4 {
			h["note"] = "remember"
		}
		if i == 6 {
			h["tags"] = []string{"x"}
		}
		if i == 7 {
			h["link"] = ""
		}
		s.headlines = append(s.headlines, h)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/", s.serve)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	s.URL = server.URL
	s.Host = server.Listener.Addr().String()
	return s
}

// DisableAPI makes the server refuse the login with API_DISABLED, as it does
// for a user who has not turned the API on.
func (s *Server) DisableAPI() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled = true
}

// FailHeadlinesFrom makes the server answer 500 to the nth request for
// headlines and every one after it; zero is never.
func (s *Server) FailHeadlinesFrom(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failFrom = n
}

// Requests returns the calls received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Ops returns the names of the calls received so far, in order.
func (s *Server) Ops() []string {
	var ops []string
	for _, r := range s.Requests() {
		ops = append(ops, r.Op)
	}
	return ops
}

// Logouts is how many times a session was closed.
func (s *Server) Logouts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logouts
}

// Change edits the article with the id: whether it is unread and starred,
// and its labels.
func (s *Server) Change(id int, unread, marked bool, labels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.headlines[id-1]
	h["unread"], h["marked"] = unread, marked
	list := []any{}
	for _, name := range labels {
		list = append(list, []any{-11, name, "", ""})
	}
	h["labels"] = list
}

const session = "0123456789abcdef"

var jsonType = regexp.MustCompile(`(?i)^(?:application|text)/json(?:\s*;|$)`)

func reply(w http.ResponseWriter, status int, content any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"seq": 0, "status": status, "content": content})
}

func failure(w http.ResponseWriter, code string) { reply(w, 1, map[string]any{"error": code}) }

// serve is api/index.php: the call is a JSON object with "op", the session
// in "sid", and the arguments beside them. Errors are answers with status 1.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if !jsonType.MatchString(r.Header.Get("Content-Type")) {
		failure(w, "INCORRECT_USAGE")
		return
	}
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		in = map[string]any{}
	}
	op, _ := in["op"].(string)
	// The API dispatches on the lowercased name, as api/index.php does.
	op = strings.ToLower(op)
	s.mu.Lock()
	s.requests = append(s.requests, Request{Op: op, Params: in})
	headlines := 0
	for _, q := range s.requests {
		if q.Op == "getheadlines" {
			headlines++
		}
	}
	failing := op == "getheadlines" && s.failFrom > 0 && headlines >= s.failFrom
	s.mu.Unlock()

	if op != "login" && op != "isloggedin" && in["sid"] != session {
		failure(w, "NOT_LOGGED_IN")
		return
	}
	switch op {
	case "login":
		s.login(w, in)
	case "logout":
		s.mu.Lock()
		s.logouts++
		s.mu.Unlock()
		reply(w, 0, map[string]any{"status": "OK"})
	case "getcategories":
		categories(w, in)
	case "getfeeds":
		feeds(w, in)
	case "getheadlines":
		if failing {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.page(w, in)
	default:
		failure(w, "UNKNOWN_METHOD")
	}
}

func (s *Server) login(w http.ResponseWriter, in map[string]any) {
	if in["user"] != User || in["password"] != Password {
		failure(w, "LOGIN_ERROR")
		return
	}
	s.mu.Lock()
	disabled := s.disabled
	s.mu.Unlock()
	if disabled {
		failure(w, "API_DISABLED")
		return
	}
	reply(w, 0, map[string]any{
		"session_id": session, "api_level": 21,
		"config": map[string]any{"icons_url": "feed-icons", "num_feeds": 4},
	})
}

// flag reads a boolean argument the way PHP's clean() and sql_bool_to_bool do.
func flag(in map[string]any, name string) bool {
	switch v := in[name].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v == "1" || v == "true"
	}
	return false
}

// categories answers getCategories: all of them, the nested ones too, unless
// enable_nested restricts the list to the top level; then the virtual ones
// (Special -1, Labels -2, Uncategorized 0).
func categories(w http.ResponseWriter, in map[string]any) {
	list := []map[string]any{
		{"id": 1, "title": "News", "unread": 0, "order_id": 1},
		{"id": 3, "title": "Empty", "unread": 0, "order_id": 2},
	}
	if !flag(in, "enable_nested") {
		list = append(list, map[string]any{"id": 2, "title": "Tech", "unread": 0, "order_id": 1})
	}
	if !flag(in, "include_empty") {
		list = slices.DeleteFunc(list, func(c map[string]any) bool { return c["id"] == 3 })
	}
	list = append(list,
		map[string]any{"id": -1, "title": "Special", "unread": 0},
		map[string]any{"id": -2, "title": "Labels", "unread": 0},
		map[string]any{"id": 0, "title": "Uncategorized", "unread": 0},
	)
	reply(w, 0, list)
}

// feeds answers getFeeds. cat_id -3 is every real feed; -4 adds the virtual
// ones (labels and the special feeds), which the importer must not take.
func feeds(w http.ResponseWriter, in map[string]any) {
	cat, ok := in["cat_id"].(float64)
	if !ok {
		failure(w, "INCORRECT_USAGE")
		return
	}
	all := []map[string]any{
		{"id": 1, "title": "Blog", "feed_url": "https://site1.example/feed.xml", "cat_id": 1, "unread": 0, "has_icon": true, "last_updated": 0, "order_id": 0, "last_error": "", "update_interval": 0},
		{"id": 2, "title": "Sub", "feed_url": "https://site2.example/feed.xml", "cat_id": 2, "unread": 0, "has_icon": false, "last_updated": 0, "order_id": 0, "last_error": "", "update_interval": 60},
		{"id": 3, "title": "Plain", "feed_url": "https://site3.example/feed.xml", "cat_id": 0, "unread": 0, "has_icon": false, "last_updated": 0, "order_id": 0, "last_error": "", "update_interval": 0},
		{"id": 4, "title": "Paused", "feed_url": "https://site4.example/feed.xml", "cat_id": 1, "unread": 0, "has_icon": false, "last_updated": 0, "order_id": 0, "last_error": "", "update_interval": -1},
	}
	list := []map[string]any{}
	if cat == -4 {
		list = append(list,
			map[string]any{"id": -1, "title": "Starred articles", "unread": 0, "cat_id": -1},
			map[string]any{"id": -4, "title": "All articles", "unread": 0, "cat_id": -1},
			map[string]any{"id": -1027, "title": "go", "unread": 0, "cat_id": -2},
		)
	}
	if cat == -3 || cat == -4 {
		list = append(list, all...)
	} else {
		for _, f := range all {
			if f["cat_id"] == int(cat) {
				list = append(list, f)
			}
		}
	}
	reply(w, 0, list)
}

// page answers getHeadlines for the one request the importer is expected to
// make: every article (feed -4, not a category), whatever its state, with
// the text, oldest first, in pages of at most MaxPage by skip.
func (s *Server) page(w http.ResponseWriter, in map[string]any) {
	feed, _ := in["feed_id"].(float64)
	if feed != -4 || flag(in, "is_cat") || in["view_mode"] != "all_articles" || !flag(in, "show_content") || in["order_by"] != "date_reverse" {
		failure(w, "INCORRECT_USAGE")
		return
	}
	limit, _ := in["limit"].(float64)
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}
	skip, _ := in["skip"].(float64)
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []map[string]any{}
	for i := int(skip); i < len(s.headlines) && len(list) < int(limit); i++ {
		list = append(list, s.headlines[i])
	}
	reply(w, 0, list)
}
