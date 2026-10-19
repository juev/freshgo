package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/events"
	"github.com/juev/freshgo/internal/store"
)

// eventLines is a stream of events the test reads line by line.
type eventLines struct {
	t      *testing.T
	resp   *http.Response
	lines  chan string
	cancel context.CancelFunc
}

// listen opens the stream of events as the browser of the test.
func (s *site) listen(server *httptest.Server) *eventLines {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+eventsPath, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, c := range s.cookies {
		r.AddCookie(c)
	}
	resp, err := server.Client().Do(r)
	if err != nil {
		s.t.Fatal(err)
	}
	st := &eventLines{t: s.t, resp: resp, lines: make(chan string, 100), cancel: cancel}
	go func() {
		defer close(st.lines)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); line != "" {
				st.lines <- line
			}
		}
	}()
	return st
}

// expect waits for a line, skipping the others.
func (st *eventLines) expect(want string) {
	st.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, open := <-st.lines:
			if !open {
				st.t.Fatalf("the stream ended before %q", want)
			}
			if line == want {
				return
			}
		case <-timeout:
			st.t.Fatalf("no %q within 5 s", want)
		}
	}
}

// ends waits for the server to end the stream.
func (st *eventLines) ends() {
	st.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, open := <-st.lines:
			if !open {
				return
			}
		case <-timeout:
			st.t.Fatal("the stream did not end within 5 s")
		}
	}
}

// eventsSite runs a test over the reference installation with events on,
// served over HTTP with the heartbeat given, with alice logged in.
func eventsSite(t *testing.T, beat time.Duration, test func(t *testing.T, s *site, broker *events.Broker, server *httptest.Server)) {
	t.Helper()
	broker := &events.Broker{}
	imported(t, Options{Events: broker}, func(t *testing.T, s *site) {
		s.h.heartbeat = beat
		s.db.EntriesChanged = func(userID int64, _ bool) { broker.Publish(userID, events.Tree) }
		server := httptest.NewServer(s.h)
		t.Cleanup(server.Close)
		s.asAlice()
		test(t, s, broker, server)
	})
}

// Only a user who logged in gets a stream; a page offers it to them alone.
func TestEventsNeedALogin(t *testing.T) {
	imported(t, Options{Events: &events.Broker{}}, func(t *testing.T, s *site) {
		if a := s.get(eventsPath); a.status != http.StatusForbidden {
			t.Errorf("GET %s without a login: status %d, want 403", eventsPath, a.status)
		}
		s.system(func(system *store.System) { system.AllowAnonymous = true })
		if a := s.get(eventsPath); a.status != http.StatusForbidden {
			t.Errorf("GET %s as a visitor: status %d, want 403", eventsPath, a.status)
		}
		if config := s.page("/"); strings.Contains(config, `&#34;events&#34;:`) {
			t.Errorf("the page of a visitor offers the stream")
		}
		s.asAlice()
		if config := s.page("/"); !strings.Contains(config, `&#34;events&#34;:&#34;/events&#34;`) {
			t.Errorf("the page of a user does not offer the stream")
		}
	})
	// Without a broker there is no stream to offer.
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		if a := s.get(eventsPath); a.status != http.StatusNotFound {
			t.Errorf("GET %s without events: status %d, want 404", eventsPath, a.status)
		}
		if config := s.page("/"); strings.Contains(config, `&#34;events&#34;:`) {
			t.Errorf("the page offers a stream there is not")
		}
	})
}

func TestEventsStream(t *testing.T) {
	eventsSite(t, 50*time.Millisecond, func(t *testing.T, s *site, _ *events.Broker, server *httptest.Server) {
		st := s.listen(server)
		for name, want := range map[string]string{
			"Content-Type": "text/event-stream", "Cache-Control": "no-store", "X-Accel-Buffering": "no",
		} {
			if got := st.resp.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		if st.resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", st.resp.StatusCode)
		}
		st.expect(": open")
		st.expect(": heartbeat")

		// An entry read on another page is told as a change of the tree.
		alice := s.user("alice")
		ids, _, err := s.db.ListPage(context.Background(), alice.ID, store.Listing{})
		if err != nil || len(ids) == 0 {
			t.Fatalf("entries of alice: %v, %v", ids, err)
		}
		if _, err := s.db.SetEntriesRead(context.Background(), alice.ID, []int64{ids[0].ID}, !ids[0].IsRead, 1); err != nil {
			t.Fatal(err)
		}
		st.expect("event: tree")
		st.expect("data: tree")
	})
}

func TestEventsEntries(t *testing.T) {
	eventsSite(t, time.Hour, func(t *testing.T, s *site, broker *events.Broker, server *httptest.Server) {
		st := s.listen(server)
		st.expect(": open")
		alice, bob := s.user("alice"), s.user("bob")
		// What is published for another user does not come.
		broker.Publish(bob.ID, events.Entries)
		broker.Publish(alice.ID, events.Entries|events.Tree)
		st.expect("event: entries")
		st.expect("data: entries")
	})
}

// A stream ends with the login it was opened under: at once on a logout,
// without waiting for a heartbeat.
func TestEventsEndWithALogout(t *testing.T) {
	eventsSite(t, time.Hour, func(t *testing.T, s *site, _ *events.Broker, server *httptest.Server) {
		st := s.listen(server)
		st.expect(": open")
		if a := s.post("/logout", nil); a.status != http.StatusSeeOther {
			t.Fatalf("logout: status %d", a.status)
		}
		st.ends()
	})
}

// A login that expires, and a user who is disabled, end the stream at the
// next heartbeat at the latest.
func TestEventsEndWithTheLogin(t *testing.T) {
	eventsSite(t, 50*time.Millisecond, func(t *testing.T, s *site, _ *events.Broker, server *httptest.Server) {
		st := s.listen(server)
		st.expect(": open")
		ctx := context.Background()
		hash := tokenHash(s.cookies[sessionCookie].Value)
		if err := s.db.TouchSession(ctx, hash, 1, 2); err != nil {
			t.Fatal(err)
		}
		st.ends()

		s.asAlice()
		st = s.listen(server)
		st.expect(": open")
		s.setting("alice", "enabled", false)
		st.ends()
	})
}

// A user who opens more streams than allowed loses the oldest.
func TestEventsPerUser(t *testing.T) {
	eventsSite(t, time.Hour, func(t *testing.T, s *site, _ *events.Broker, server *httptest.Server) {
		var streams []*eventLines
		for range events.MaxPerUser + 1 {
			st := s.listen(server)
			st.expect(": open")
			streams = append(streams, st)
		}
		streams[0].expect("event: replaced")
		streams[0].ends()
	})
}

// Closing the broker, as a server that shuts down does, ends the streams.
func TestEventsEndWithTheBroker(t *testing.T) {
	eventsSite(t, time.Hour, func(t *testing.T, s *site, broker *events.Broker, server *httptest.Server) {
		st := s.listen(server)
		st.expect(": open")
		broker.Close()
		st.ends()
	})
}
