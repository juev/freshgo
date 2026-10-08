package refresh

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/websub"
)

const publicBase = "https://rss.example.net/reader"

// hubWorld is a world whose server is also a WebSub hub at /hub, and whose
// feed at /feed.xml names that hub.
type hubWorld struct {
	*world
	service *websub.Service
	topic   string

	mu sync.Mutex
	// asked are the forms the hub got; status is what it answers.
	asked  []url.Values
	status int
}

func newHubWorld(t *testing.T, w *world) *hubWorld {
	t.Helper()
	h := &hubWorld{world: w, topic: "http://blog.example/feed.xml", status: http.StatusAccepted}
	client, err := fetch.New(fetch.Options{Allowlist: []string{strings.TrimPrefix(w.server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	h.service, err = websub.New(w.db, client, slog.New(slog.NewTextHandler(w.logs, nil)), publicBase)
	if err != nil {
		t.Fatal(err)
	}
	h.service.Now = func() time.Time { return w.clock }
	h.service.Pusher = w.r
	w.r.WebSub = h.service
	w.serve("/hub", func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(body))
		h.mu.Lock()
		h.asked = append(h.asked, form)
		status := h.status
		h.mu.Unlock()
		rw.WriteHeader(status)
	})
	h.publish("a", "b")
	return h
}

// document is the feed with the given entries, as the site and the hub
// serve it.
func (h *hubWorld) document(self string, guids ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title>` +
		`<link rel="hub" href="` + h.server.URL + `/hub"/><link rel="self" href="` + self + `"/>`)
	for _, guid := range guids {
		b.WriteString(`<entry><id>urn:` + guid + `</id><title>` + guid + `</title><updated>2026-10-06T10:00:00Z</updated></entry>`)
	}
	b.WriteString(`</feed>`)
	return b.String()
}

// publish makes the site serve the feed with the given entries.
func (h *hubWorld) publish(guids ...string) {
	h.serveBody("/feed.xml", "application/atom+xml", h.document(h.topic, guids...))
}

func (h *hubWorld) requests() []url.Values {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]url.Values(nil), h.asked...)
}

func (h *hubWorld) subscription() *store.WebSub {
	h.t.Helper()
	sub, err := h.db.WebSubByTopic(context.Background(), h.topic)
	if err != nil {
		h.t.Fatalf("subscription: %v", err)
	}
	return sub
}

// call sends the service what a hub would.
func (h *hubWorld) call(method, target, body string, header ...string) (int, string) {
	h.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Add(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.service.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// confirm plays the hub checking that the subscription is wanted.
func (h *hubWorld) confirm(lease int) {
	h.t.Helper()
	target := fmt.Sprintf("/websub/%s?hub.mode=subscribe&hub.topic=%s&hub.challenge=c-123&hub.lease_seconds=%d",
		h.subscription().Key, url.QueryEscape(h.topic), lease)
	if status, body := h.call(http.MethodGet, target, ""); status != http.StatusOK || body != "c-123" {
		h.t.Fatalf("confirmation: status %d, body %q; want the challenge back", status, body)
	}
}

// push plays the hub sending a document, signed with the given secret.
func (h *hubWorld) push(document, secret string, header ...string) (int, string) {
	h.t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(document))
	header = append(header, "Content-Type", "application/atom+xml", "X-Hub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return h.call(http.MethodPost, "/websub/"+h.subscription().Key, document, header...)
}

func (h *hubWorld) titles(f *store.Feed) []string {
	h.t.Helper()
	var titles []string
	for _, e := range h.entries(f) {
		titles = append(titles, e.Title)
	}
	return titles
}

// R14: a feed that names a hub is subscribed to at the hub after its
// refresh, with a callback under the public address and a secret; the hub's
// question is answered with its challenge and the lease is recorded.
func TestWebSubSubscribes(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		f := w.feed(u, "/feed.xml", nil)
		plain := w.feed(u, "/plain.xml", nil)
		w.serveBody("/plain.xml", "application/rss+xml", blogFeed)

		w.runOne()
		asked := h.requests()
		if len(asked) != 1 {
			t.Fatalf("the hub was asked %d times, want once: %v", len(asked), asked)
		}
		sub := h.subscription()
		form := asked[0]
		if form.Get("hub.mode") != "subscribe" || form.Get("hub.topic") != h.topic ||
			form.Get("hub.callback") != publicBase+"/websub/"+sub.Key || form.Get("hub.secret") != sub.Secret {
			t.Errorf("subscription request = %v, want the topic, a callback under %s and the secret", form, publicBase)
		}
		if len(sub.Key) != 64 || len(sub.Secret) != 64 || sub.Key == sub.Secret || !sub.Error || sub.LeaseEnd != 0 || sub.LeaseStart != start.Unix() {
			t.Errorf("subscription = %+v; want random key and secret, not trusted and not confirmed yet", sub)
		}
		if got := w.storedFeed(f).WebSubTopic; got != h.topic {
			t.Errorf("topic of the feed = %q, want %q", got, h.topic)
		}
		if got := w.storedFeed(plain).WebSubTopic; got != "" {
			t.Errorf("topic of a feed without a hub = %q, want none", got)
		}

		h.confirm(864000)
		sub = h.subscription()
		if sub.LeaseEnd != start.Unix()+864000 || !sub.Error {
			t.Errorf("subscription after the confirmation = %+v; want the lease recorded and no trust before the first push", sub)
		}
		// The hub is not asked again while the lease is far from its end.
		w.later()
		w.runOne()
		if n := len(h.requests()); n != 1 {
			t.Errorf("the hub was asked %d times after the next refresh, want still once", n)
		}

		for target, want := range map[string]int{
			"/websub/" + sub.Key + "?hub.mode=subscribe&hub.challenge=x&hub.topic=http%3A%2F%2Fother.example%2F":                       http.StatusNotFound,
			"/websub/" + strings.Repeat("0", 64) + "?hub.mode=subscribe&hub.challenge=x":                                               http.StatusNotFound,
			"/websub/" + sub.Key + "?hub.mode=unsubscribe&hub.challenge=x&hub.topic=" + url.QueryEscape(h.topic):                       http.StatusNotFound,
			"/websub/" + sub.Key + "?hub.challenge=x":                                                                                  http.StatusBadRequest,
			"/websub/" + sub.Key + "?hub.mode=subscribe&hub.challenge=x&hub.topic=" + url.QueryEscape("https://blog.example/feed.xml"): http.StatusOK,
		} {
			if status, body := h.call(http.MethodGet, target, ""); status != want {
				t.Errorf("GET %s: status %d (%q), want %d", target, status, body, want)
			}
		}
		if status, _ := h.call(http.MethodPut, "/websub/"+sub.Key, ""); status != http.StatusMethodNotAllowed {
			t.Errorf("PUT: status %d, want 405", status)
		}
	})
}

// R14: a signed push stores the new entry for every user who reads the
// feed, without a request to the feed; what the push does not list is left
// alone.
func TestWebSubPush(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		// alice wants entries that leave the feed marked read; a push must
		// not be taken for the whole feed.
		alice := w.user("alice", `{"mark_when":{"gone":true}}`)
		bob := w.user("bob", "")
		carol := w.user("carol", `{"enabled":false}`)
		mine := w.feed(alice, "/feed.xml", nil)
		theirs := w.feed(bob, "/feed.xml", nil)
		muted := w.feed(bob, "/feed.xml", func(f *store.Feed) { f.URL += "?again"; f.TTL = -3600; f.WebSubTopic = h.topic })
		disabled := w.feed(carol, "/feed.xml", func(f *store.Feed) { f.WebSubTopic = h.topic })
		w.serve("/feed.xml", nil)
		h.publish("a", "b")
		w.run(Options{})
		h.confirm(864000)
		polled := w.hitCount("/feed.xml")
		lastUpdate := w.storedFeed(mine).LastUpdate

		w.clock = w.clock.Add(time.Minute)
		secret := h.subscription().Secret
		status, body := h.push(h.document(h.topic, "c"), secret)
		if status != http.StatusOK || body != "Done: 2\n" {
			t.Fatalf("push: status %d, body %q; want it stored for the two feeds that take it", status, body)
		}
		for name, f := range map[string]*store.Feed{"alice": mine, "bob": theirs} {
			if got := h.titles(f); len(got) != 3 || got[2] != "c" {
				t.Errorf("entries of %s after the push = %v, want a, b and c", name, got)
			}
		}
		for _, e := range w.entries(mine) {
			if e.IsRead {
				t.Errorf("entry %q of alice became read: the push was taken for the whole feed", e.Title)
			}
		}
		if len(w.entries(muted)) != 0 || len(w.entries(disabled)) != 0 {
			t.Error("a muted feed or the feed of a disabled user took the push")
		}
		if w.hitCount("/feed.xml") != polled {
			t.Errorf("the feed was requested %d more times for the push", w.hitCount("/feed.xml")-polled)
		}
		if got := w.storedFeed(mine); got.LastUpdate != lastUpdate {
			t.Errorf("last poll of the feed moved from %d to %d by a push", lastUpdate, got.LastUpdate)
		}
		if h.subscription().Error {
			t.Error("the hub is still not trusted after its first push")
		}

		// The same document again changes nothing; a signature by SHA-1 is
		// as good.
		mac := hmac.New(sha1.New, []byte(secret))
		document := h.document(h.topic, "c")
		mac.Write([]byte(document))
		status, body = h.call(http.MethodPost, "/websub/"+h.subscription().Key, document,
			"Content-Type", "application/atom+xml", "X-Hub-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
		if status != http.StatusOK || body != "Done: 2\n" || len(w.entries(mine)) != 3 {
			t.Errorf("repeated push: status %d, body %q, %d entries", status, body, len(w.entries(mine)))
		}
	})
}

// R14: a push without the right signature, for an unknown key, or about
// another feed changes nothing.
func TestWebSubRefusesPushes(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		f := w.feed(u, "/feed.xml", nil)
		w.runOne()
		h.confirm(864000)
		sub := h.subscription()
		document := h.document(h.topic, "c")
		target := "/websub/" + sub.Key

		for name, tc := range map[string]struct {
			status int
			call   func() (int, string)
		}{
			"no signature": {http.StatusForbidden, func() (int, string) {
				return h.call(http.MethodPost, target, document, "Content-Type", "application/atom+xml")
			}},
			"a signature with another secret": {http.StatusForbidden, func() (int, string) { return h.push(document, "guess") }},
			"a signature of another document": {http.StatusForbidden, func() (int, string) {
				mac := hmac.New(sha256.New, []byte(sub.Secret))
				mac.Write([]byte("other"))
				return h.call(http.MethodPost, target, document, "X-Hub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			}},
			"an unknown method": {http.StatusForbidden, func() (int, string) {
				return h.call(http.MethodPost, target, document, "X-Hub-Signature", "md5=00")
			}},
			"an unknown key": {http.StatusGone, func() (int, string) {
				return h.call(http.MethodPost, "/websub/"+strings.Repeat("f", 64), document, "X-Hub-Signature", "sha256=00")
			}},
			"another feed in the document": {http.StatusUnprocessableEntity, func() (int, string) {
				return h.push(h.document("http://other.example/feed.xml", "c"), sub.Secret)
			}},
			"another feed in the Link header": {http.StatusUnprocessableEntity, func() (int, string) {
				return h.push(document, sub.Secret, "Link", `<http://hub.example/>; rel="hub", <http://other.example/feed.xml>; rel="self"`)
			}},
			"an empty body": {http.StatusUnprocessableEntity, func() (int, string) { return h.push("", sub.Secret) }},
			"a body too large": {http.StatusRequestEntityTooLarge, func() (int, string) {
				return h.push(document+strings.Repeat(" ", 3<<20), sub.Secret)
			}},
		} {
			if status, body := tc.call(); status != tc.status {
				t.Errorf("%s: status %d (%q), want %d", name, status, body, tc.status)
			}
		}
		if got := h.titles(f); len(got) != 2 {
			t.Errorf("entries after the refused pushes = %v, want only a and b", got)
		}
		if !h.subscription().Error {
			t.Error("the hub became trusted by pushes that were refused")
		}
		// The address in the Link header may differ from the topic by its scheme only.
		if status, body := h.push(document, sub.Secret, "Link", `<https://blog.example/feed.xml>; rel="self"`); status != http.StatusOK {
			t.Errorf("push with the https form of the topic: status %d (%q), want 200", status, body)
		}
	})
}

// R14: the subscription is renewed when less than 23 hours of the lease are
// left; a hub that fails is left alone for 23 hours.
func TestWebSubRenews(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		w.feed(u, "/feed.xml", nil)
		w.runOne()
		h.confirm(2 * 24 * 3600)
		key := h.subscription().Key
		// A hub that has not pushed yet is asked again every 23 hours; this
		// one has delivered.
		if status, body := h.push(h.document(h.topic, "c"), h.subscription().Secret); status != http.StatusOK {
			t.Fatalf("push: status %d (%q)", status, body)
		}
		h.publish("a", "b", "c")

		w.clock = w.clock.Add(24 * time.Hour)
		w.runOne()
		if n := len(h.requests()); n != 1 {
			t.Fatalf("the hub was asked %d times with a day of lease left, want once", n)
		}
		w.clock = w.clock.Add(2 * time.Hour)
		w.runOne()
		asked := h.requests()
		if len(asked) != 2 || asked[1].Get("hub.callback") != publicBase+"/websub/"+key {
			t.Fatalf("with 22 hours of lease left the hub was asked %d times; want a renewal under the same key: %v", len(asked), asked)
		}

		// The hub starts failing: asked once, then left alone for 23 hours.
		h.mu.Lock()
		h.status = http.StatusInternalServerError
		h.mu.Unlock()
		w.clock = w.clock.Add(22 * time.Hour) // the lease has run out
		for range 3 {
			w.later()
			w.runOne()
		}
		if n := len(h.requests()); n != 3 {
			t.Errorf("a failing hub was asked %d times in six hours, want once more (3 in all)", n)
		}
		if !h.subscription().Error {
			t.Error("a hub that failed is trusted")
		}
		w.clock = w.clock.Add(24 * time.Hour)
		w.runOne()
		if n := len(h.requests()); n != 4 {
			t.Errorf("a failing hub was asked %d times after a day, want once more (4 in all)", n)
		}
	})
}

// R14: a feed whose hub delivers is polled once a day; an entry found by
// that poll means the hub let it slip, and the feed is polled as before.
func TestWebSubPollsLessOften(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		f := w.feed(u, "/feed.xml", nil)
		w.runOne()
		h.confirm(30 * 24 * 3600)

		// Confirmed but silent: no trust yet, polled by its own period.
		w.later()
		w.runOne()
		if hits := w.hitCount("/feed.xml"); hits != 2 {
			t.Fatalf("%d requests before the first push, want 2: an untried hub does not spare polls", hits)
		}
		if status, body := h.push(h.document(h.topic, "c"), h.subscription().Secret); status != http.StatusOK {
			t.Fatalf("push: status %d (%q)", status, body)
		}
		h.publish("a", "b", "c")
		for range 5 {
			w.later()
			w.runOne()
		}
		if hits := w.hitCount("/feed.xml"); hits != 2 {
			t.Errorf("%d requests in the ten hours after a push, want none: the hub delivers", hits-2)
		}
		// A forced run does not wait.
		w.run(Options{Force: true})
		if hits := w.hitCount("/feed.xml"); hits != 3 {
			t.Errorf("a forced run made %d requests, want one", hits-2)
		}

		// A day later the feed is polled, and holds an entry the hub never sent.
		h.publish("a", "b", "c", "d")
		w.clock = w.clock.Add(25 * time.Hour)
		w.runOne()
		if got := h.titles(f); len(got) != 4 {
			t.Fatalf("entries after the daily poll = %v, want the missed one too", got)
		}
		if !h.subscription().Error {
			t.Error("the hub is still trusted after a poll found an entry it did not push")
		}
		before := w.hitCount("/feed.xml")
		w.later()
		w.runOne()
		if w.hitCount("/feed.xml") != before+1 {
			t.Error("a feed whose hub let it down is not polled by its own period again")
		}
	})
}

// R14: nobody reads the feed any more → the hub is told to stop, and the
// subscription goes.
func TestWebSubEndsWithItsReaders(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		f := w.feed(u, "/feed.xml", nil)
		w.runOne()
		h.confirm(864000)
		sub := h.subscription()
		if err := w.db.DeleteFeed(ctx, u.ID, f.ID); err != nil {
			t.Fatal(err)
		}

		// A confirmation that comes late is refused.
		target := "/websub/" + sub.Key + "?hub.challenge=x&hub.topic=" + url.QueryEscape(h.topic)
		if status, _ := h.call(http.MethodGet, target+"&hub.mode=subscribe", ""); status != http.StatusNotFound {
			t.Errorf("confirmation of a subscription nobody needs: status %d, want 404", status)
		}
		if status, body := h.push(h.document(h.topic, "c"), sub.Secret); status != http.StatusGone {
			t.Errorf("push for a feed nobody reads: status %d (%q), want 410", status, body)
		}
		if _, err := w.db.WebSubByTopic(ctx, h.topic); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("subscription after the push: error = %v, want it deleted", err)
		}
	})
}

// unchanged makes the site answer that the feed is as it was.
func (h *hubWorld) unchanged() {
	h.serve("/feed.xml", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNotModified) })
}

// W3: a server that read its feeds with WebSub off subscribes to their hubs
// in the first refresh with WebSub on, although no feed is read whole; a
// muted feed is not subscribed for.
func TestWebSubSwitchedOn(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		w.r.WebSub = nil
		u := w.user("alice", "")
		w.feed(u, "/feed.xml", nil)
		w.serveBody("/muted.xml", "application/atom+xml", h.document("http://blog.example/muted.xml", "m"))
		muted := w.feed(u, "/muted.xml", nil)
		w.runOne()
		muted = w.storedFeed(muted)
		muted.TTL = -3600
		if err := w.db.UpdateFeed(context.Background(), muted); err != nil {
			t.Fatal(err)
		}

		w.r.WebSub = h.service
		h.unchanged()
		w.later()
		w.runOne()
		asked := h.requests()
		if len(asked) != 1 || asked[0].Get("hub.topic") != h.topic || asked[0].Get("hub.mode") != "subscribe" {
			t.Fatalf("the hub was asked %v; want one subscription, to %s", asked, h.topic)
		}
		w.later()
		w.runOne()
		if n := len(h.requests()); n != 1 {
			t.Errorf("the hub was asked %d times in two refreshes, want once", n)
		}
	})
}

// W3: a lease is renewed when it is about to run out, although the feed is
// not due or answers that nothing has changed.
func TestWebSubRenewsWithoutADocument(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		w.feed(u, "/feed.xml", nil)
		w.runOne()
		h.confirm(2 * 24 * 3600)
		if status, body := h.push(h.document(h.topic, "c"), h.subscription().Secret); status != http.StatusOK {
			t.Fatalf("push: status %d (%q)", status, body)
		}
		h.unchanged()

		// The daily poll of a feed whose hub delivers.
		w.clock = w.clock.Add(25 * time.Hour)
		w.runOne()
		polls := w.hitCount("/feed.xml")
		if n := len(h.requests()); n != 1 || polls != 2 {
			t.Fatalf("with 23 hours of lease left the hub was asked %d times and the feed polled %d times, want once and twice", n, polls)
		}
		w.clock = w.clock.Add(2 * time.Hour)
		w.runOne()
		if n := len(h.requests()); n != 2 || w.hitCount("/feed.xml") != polls {
			t.Errorf("with 21 hours of lease left the hub was asked %d times and the feed polled %d times more; want a renewal without a poll",
				n, w.hitCount("/feed.xml")-polls)
		}
	})
}

// W2: the hub and the own address of a feed are taken from the Link headers
// of the answer, which overrule the document; an address there may be
// relative to that of the feed.
func TestWebSubLinkHeaders(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		u := w.user("alice", "")
		plain := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title>` +
			`<entry><id>urn:a</id><title>a</title><updated>2026-10-06T10:00:00Z</updated></entry></feed>`
		w.serve("/headers.xml", func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "application/atom+xml")
			rw.Header().Add("Link", `</hub>; rel="hub", <http://blog.example/headers.xml>; type="application/atom+xml"; rel=self`)
			_, _ = io.WriteString(rw, plain)
		})
		w.serve("/overruled.xml", func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "application/atom+xml")
			rw.Header().Add("Link", `<http://blog.example/moved.xml>; rel="self"`)
			rw.Header().Add("Link", `<http://blog.example/about>; rel="alternate"`)
			_, _ = io.WriteString(rw, h.document("http://blog.example/overruled.xml", "a"))
		})
		fromHeaders, overruled := w.feed(u, "/headers.xml", nil), w.feed(u, "/overruled.xml", nil)
		w.runOne()

		if got := w.storedFeed(fromHeaders); got.WebSubTopic != "http://blog.example/headers.xml" || got.WebSubHub != h.server.URL+"/hub" {
			t.Errorf("a feed that names its hub in headers: topic %q, hub %q", got.WebSubTopic, got.WebSubHub)
		}
		if got := w.storedFeed(overruled); got.WebSubTopic != "http://blog.example/moved.xml" || got.WebSubHub != h.server.URL+"/hub" {
			t.Errorf("a feed whose header names another address: topic %q, hub %q", got.WebSubTopic, got.WebSubHub)
		}
		topics := map[string]bool{}
		for _, form := range h.requests() {
			topics[form.Get("hub.topic")] = true
		}
		if len(topics) != 2 || !topics["http://blog.example/headers.xml"] || !topics["http://blog.example/moved.xml"] {
			t.Errorf("the hub was asked for %v; want the two topics of the headers", topics)
		}
	})
}

// R14: without WebSub, or with an address hubs cannot reach, nothing is
// subscribed to and feeds are polled as always.
func TestWebSubOff(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		h := newHubWorld(t, w)
		w.r.WebSub = nil
		u := w.user("alice", "")
		f := w.feed(u, "/feed.xml", nil)
		w.runOne()
		w.later()
		w.runOne()
		if n := len(h.requests()); n != 0 {
			t.Errorf("the hub was asked %d times with WebSub off", n)
		}
		// What the feed announces is recorded for the day WebSub is on.
		if got := w.storedFeed(f); got.WebSubTopic != h.topic || got.WebSubHub != h.server.URL+"/hub" || w.hitCount("/feed.xml") != 2 {
			t.Errorf("topic %q, hub %q, %d requests; want the topic and the hub recorded and a poll per period",
				got.WebSubTopic, got.WebSubHub, w.hitCount("/feed.xml"))
		}
	})
	for _, base := range []string{
		"", "rss.example.net", "ftp://rss.example.net", "http://localhost:8080", "http://app.localhost", "http://intranet",
		"http://127.0.0.1:8080", "http://[::1]/", "http://192.168.1.10", "http://10.0.0.5/rss", "http://169.254.1.1",
	} {
		if _, err := websub.New(nil, nil, nil, base); !errors.Is(err, websub.ErrNotPublic) {
			t.Errorf("websub.New with the base %q: error = %v, want ErrNotPublic", base, err)
		}
	}
	for _, base := range []string{"https://rss.example.net", "http://rss.example.net:8080/sub/", "http://203.0.113.7"} {
		if _, err := websub.New(nil, nil, nil, base); err != nil {
			t.Errorf("websub.New with the base %q: %v", base, err)
		}
	}
}
