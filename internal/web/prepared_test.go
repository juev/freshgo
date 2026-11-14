package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Supplemental controls on the same server-rendered validator: no cached
// representation is accepted on changed data, identity, URL or an old cutoff.
func TestPreparedReaderInvalidation(t *testing.T) {
	for _, kind := range []string{"read", "sort", "filter", "user", "logout", "expired", "future"} {
		t.Run(kind, func(t *testing.T) {
			imported(t, Options{}, func(t *testing.T, s *site) {
				s.login("alice", "alice-web-password", nil)
				now := time.Now()
				s.h.now = func() time.Time { return now }
				prepared := s.do(http.MethodGet, "/?state=unread", map[string]string{"X-Prepare": "true"})
				target := "/?state=unread"
				switch kind {
				case "read":
					ids := s.stored("alice", showing{state: stateUnread}.listing(stream{set: mainStream()}, nil))
					if len(ids) == 0 {
						t.Fatal("fixture has no unread entries")
					}
					if _, err := s.db.SetEntriesRead(context.Background(), s.user("alice").ID, ids[:1], true, now.UnixMicro()); err != nil {
						t.Fatal(err)
					}
				case "sort":
					target += "&order=asc"
				case "filter":
					target += "&q=missing-entry-title"
				case "user":
					s.login("bob", "bob-web-password", nil)
				case "logout":
					s.post("/logout", nil)
				case "expired":
					now = now.Add(31 * time.Second)
				case "future":
					now = now.Add(-time.Second)
				}
				answer := s.do(http.MethodGet, target, map[string]string{"If-None-Match": prepared.header.Get("ETag"), "X-Prepared-At": prepared.header.Get("X-Prepared-At")})
				if answer.status == http.StatusNotModified {
					t.Fatalf("%s reused a prepared page", kind)
				}
			})
		})
	}
}

// Preparing a page is read-only, including the notice cookie. A prepared
// representation can only be reused after an authenticated conditional GET.
func TestPreparedReader(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.login("alice", "alice-web-password", nil)
		now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
		s.h.now = func() time.Time { return now }
		s.cookies[noticeCookie] = &http.Cookie{Name: noticeCookie, Value: "notice.saved:0"}
		prepared := s.do(http.MethodGet, "/?state=unread", map[string]string{"X-Prepare": "true"})
		if prepared.status != http.StatusOK {
			t.Fatalf("prepare status %d", prepared.status)
		}
		if s.cookies[noticeCookie] == nil || strings.Contains(prepared.body, ">Saved.</p>") {
			t.Fatal("preparing consumed or displayed the notice")
		}
		tag, at := prepared.header.Get("ETag"), prepared.header.Get("X-Prepared-At")
		if tag == "" || at == "" {
			t.Fatal("prepared page has no validator or preparation time")
		}
		headers := map[string]string{"If-None-Match": tag, "X-Prepared-At": at}
		notice := s.do(http.MethodGet, "/?state=unread", headers)
		if notice.status != http.StatusOK || !strings.Contains(notice.body, ">Saved.</p>") || s.cookies[noticeCookie] != nil {
			t.Fatal("the real navigation did not consume and display the notice")
		}
		now = now.Add(time.Second)
		unchanged := s.do(http.MethodGet, "/?state=unread", headers)
		if unchanged.status != http.StatusNotModified || unchanged.body != "" {
			t.Fatalf("unchanged: status %d, body %d bytes", unchanged.status, len(unchanged.body))
		}
		s.setting("alice", "darkMode", "dark")
		changed := s.do(http.MethodGet, "/?state=unread", headers)
		if changed.status != http.StatusOK || changed.header.Get("ETag") == tag {
			t.Fatal("changed preferences reused the prepared page")
		}
	})
}
