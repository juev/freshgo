package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"golang.org/x/crypto/hkdf"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
	"github.com/juev/freshgo/internal/web/i18n"
)

const subject = "mailto:ops@example.org"

// browser is the receiving end of a subscription: it holds the keys a
// browser makes and opens what is encrypted to them.
type browser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return &browser{key: key, auth: auth}
}

// open decrypts a body of Content-Encoding aes128gcm (RFC 8188) sent as
// Web Push does (RFC 8291).
func (b *browser) open(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body of %d bytes is shorter than the header of aes128gcm", len(body))
	}
	salt, idLen := body[:16], int(body[20])
	if len(body) < 21+idLen {
		t.Fatalf("body of %d bytes ends inside the key id", len(body))
	}
	sender, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatalf("key of the sender: %v", err)
	}
	secret, err := b.key.ECDH(sender)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), b.key.PublicKey().Bytes()...)
	info = append(info, sender.Bytes()...)
	derive := func(secret, salt, info []byte, n int) []byte {
		out := make([]byte, n)
		if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	ikm := derive(secret, b.auth, info, 32)
	block, err := aes.NewCipher(derive(ikm, salt, []byte("Content-Encoding: aes128gcm\x00"), 16))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, derive(ikm, salt, []byte("Content-Encoding: nonce\x00"), 12), body[21+idLen:], nil)
	if err != nil {
		t.Fatalf("the body is not encrypted to this browser: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatalf("the last record does not end with its delimiter: %q", plain)
	}
	return plain[:len(plain)-1]
}

// received is a request the push service got.
type received struct {
	path   string
	header http.Header
	body   []byte
}

// world is a database, a push service that records what it is sent, a site
// that serves a feed, and the notifier wired into a refresher.
type world struct {
	t         *testing.T
	db        *store.Store
	service   *httptest.Server
	site      *httptest.Server
	refresher *refresh.Refresher
	notifier  *Notifier
	public    string
	logs      *syncBuffer

	mu      sync.Mutex
	got     []received
	status  map[string]int
	hold    chan struct{}
	entries []string
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func eachEngine(t *testing.T, test func(t *testing.T, w *world)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			test(t, newWorld(t, db, true))
		})
	}
}

// newWorld builds the world; allowed says whether the fetch client may reach
// the push service, which listens on a loopback address.
func newWorld(t *testing.T, db *store.Store, allowed bool) *world {
	t.Helper()
	w := &world{t: t, db: db, status: map[string]int{}, logs: &syncBuffer{}}
	w.service = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.got = append(w.got, received{r.URL.Path, r.Header.Clone(), body})
		status, hold := w.status[r.URL.Path], w.hold
		w.mu.Unlock()
		if hold != nil {
			<-hold
		}
		if status == 0 {
			status = http.StatusCreated
		}
		rw.WriteHeader(status)
	}))
	t.Cleanup(w.service.Close)
	w.site = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/rss+xml")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>Site</title><link>https://example.org/</link><description>d</description>`)
		for _, title := range w.entries {
			fmt.Fprintf(&b, `<item><guid isPermaLink="false">%[1]s</guid><title>%[1]s</title><link>https://example.org/%[1]s</link><description>text</description></item>`, title)
		}
		b.WriteString(`</channel></rss>`)
		_, _ = io.WriteString(rw, b.String())
	}))
	t.Cleanup(w.site.Close)

	allowlist := []string{strings.TrimPrefix(w.site.URL, "http://")}
	if allowed {
		allowlist = append(allowlist, strings.TrimPrefix(w.service.URL, "http://"))
	}
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: allowlist})
	if err != nil {
		t.Fatal(err)
	}
	texts, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	w.public = public
	log := slog.New(slog.NewTextHandler(w.logs, nil))
	w.notifier = New(Options{DB: db, Fetcher: client, Texts: texts, Log: log, PublicKey: public, PrivateKey: private, Subject: subject})
	w.refresher = refresh.New(db, client, &hooks.Registry{}, log)
	w.refresher.Notify = w.notifier.Notify
	return w
}

func (w *world) user(name, settings string) *store.User {
	w.t.Helper()
	u := &store.User{Name: name, Settings: json.RawMessage(settings)}
	if err := w.db.CreateUser(context.Background(), u); err != nil {
		w.t.Fatal(err)
	}
	return u
}

func (w *world) feed(u *store.User, change func(*store.Feed)) *store.Feed {
	w.t.Helper()
	f := &store.Feed{UserID: u.ID, URL: w.site.URL + "/feed.xml", Name: "Site"}
	if change != nil {
		change(f)
	}
	if err := w.db.CreateFeed(context.Background(), f); err != nil {
		w.t.Fatal(err)
	}
	return f
}

// subscribe gives the user a browser at the path of the push service.
func (w *world) subscribe(u *store.User, path string) *browser {
	w.t.Helper()
	b := newBrowser(w.t)
	err := w.db.PutPush(context.Background(), &store.PushSubscription{
		UserID: u.ID, Endpoint: w.service.URL + path,
		P256DH: base64.RawURLEncoding.EncodeToString(b.key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(b.auth),
		Created: time.Now().Unix(),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

// publish makes the site serve the feed with the entries.
func (w *world) publish(titles ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = titles
}

// refresh runs a refresh of every feed and waits for the messages it caused.
func (w *world) refresh() {
	w.t.Helper()
	if _, err := w.refresher.Run(context.Background(), refresh.Options{Force: true}); err != nil {
		w.t.Fatalf("Run: %v", err)
	}
	w.notifier.Wait()
}

func (w *world) requests() []received {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]received(nil), w.got...)
}

func (w *world) at(path string) []received {
	var out []received
	for _, r := range w.requests() {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (w *world) subscriptions(u *store.User) int {
	w.t.Helper()
	subs, err := w.db.PushSubscriptions(context.Background(), u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return len(subs)
}

// newest is the title of the entry stored last for the feed.
func (w *world) newest(f *store.Feed) string {
	w.t.Helper()
	entries, err := w.db.EntriesByFeed(context.Background(), f.UserID, f.ID)
	if err != nil || len(entries) == 0 {
		w.t.Fatalf("entries of the feed: %v, %v", entries, err)
	}
	last := entries[0]
	for _, e := range entries {
		if e.ID > last.ID {
			last = e
		}
	}
	return last.Title
}

// opened opens a request to the push service and reads the message in it.
func opened(t *testing.T, b *browser, r received) message {
	t.Helper()
	var m message
	if err := json.Unmarshal(b.open(t, r.body), &m); err != nil {
		t.Fatalf("the message is not JSON: %v", err)
	}
	return m
}

// A refresh that brings unread entries to the reading list sends one message
// to each browser of the user, encrypted to that browser and signed with the
// VAPID keys; a refresh that brings nothing sends none.
func TestRefreshNotifiesEachBrowserOnce(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		alice := w.user("alice", `{"language":"en"}`)
		bob := w.user("bob", `{"language":"ru"}`)
		carol := w.user("carol", ``)
		feed := w.feed(alice, nil)
		w.feed(bob, nil)
		w.feed(carol, nil)
		phone, laptop := w.subscribe(alice, "/alice-phone"), w.subscribe(alice, "/alice-laptop")
		bobsPhone := w.subscribe(bob, "/bob")

		w.publish("first", "second", "third")
		w.refresh()

		for path, b := range map[string]*browser{"/alice-phone": phone, "/alice-laptop": laptop} {
			got := w.at(path)
			if len(got) != 1 {
				t.Fatalf("%s got %d messages for one refresh, want 1", path, len(got))
			}
			m := opened(t, b, got[0])
			want := "3 new entries, the latest: " + w.newest(feed)
			if m.Title != "freshgo" || m.Body != want {
				t.Errorf("%s: message %+v, want the title of the site and %q", path, m, want)
			}
		}
		// The message is in the language of the user.
		got := w.at("/bob")
		if len(got) != 1 {
			t.Fatalf("bob's browser got %d messages, want 1", len(got))
		}
		if m := opened(t, bobsPhone, got[0]); !strings.Contains(m.Body, "3 новые статьи, последняя: ") {
			t.Errorf("message to bob = %+v, want it in Russian", m)
		}

		// What the push service is told about the message.
		h := got[0].header
		if h.Get("Content-Encoding") != "aes128gcm" || h.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("Content-Encoding = %q, Content-Type = %q; want aes128gcm and application/octet-stream", h.Get("Content-Encoding"), h.Get("Content-Type"))
		}
		if ttl, err := strconv.Atoi(h.Get("TTL")); err != nil || ttl <= 0 {
			t.Errorf("TTL = %q, want seconds above zero", h.Get("TTL"))
		}
		if h.Get("Topic") == "" {
			t.Error("the message has no Topic: a browser that was off would get every message of the day")
		}
		t.Run("VAPID", func(t *testing.T) {
			auth := h.Get("Authorization")
			token, key, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
			if !strings.HasPrefix(auth, "vapid t=") || !ok || key != w.public {
				t.Fatalf("Authorization = %q, want vapid t=<token>, k=<public key>", auth)
			}
			parts := strings.Split(token, ".")
			if len(parts) != 3 {
				t.Fatalf("token %q is not a JWT", token)
			}
			claims, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			var c struct {
				Aud, Sub string
				Exp      int64
			}
			if err := json.Unmarshal(claims, &c); err != nil {
				t.Fatal(err)
			}
			if c.Aud != w.service.URL || c.Sub != subject || c.Exp <= time.Now().Unix() {
				t.Errorf("claims %+v, want the origin of the service %q, the subject %q and an expiry ahead", c, w.service.URL, subject)
			}
		})

		// Carol has no browser, so nobody is told for her.
		if n := len(w.requests()); n != 3 {
			t.Errorf("%d messages in all, want 3", n)
		}

		// Nothing new, nothing sent.
		w.refresh()
		if n := len(w.requests()); n != 3 {
			t.Errorf("a refresh that brought nothing sent %d more messages", n-3)
		}

		// One entry is said in the singular, with its title.
		w.publish("first", "second", "third", "fourth")
		w.refresh()
		if n := len(w.at("/alice-phone")); n != 2 {
			t.Fatalf("alice's phone got %d messages after the second batch, want 2", n)
		}
		if m := opened(t, phone, w.at("/alice-phone")[1]); m.Body != "1 new entry: fourth" {
			t.Errorf("message = %+v, want one new entry: fourth", m)
		}
	})
}

// Entries the user does not see in the reading list are not news: a feed
// below priority 0, entries a rule marks read.
func TestRefreshDoesNotNotifyOfHiddenEntries(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		alice := w.user("alice", `{"filters":[{"search":"intitle:boring","actions":["read"]}]}`)
		w.feed(alice, func(f *store.Feed) { f.Priority = -10 })
		w.subscribe(alice, "/alice")
		w.publish("one", "two")
		w.refresh()
		if n := len(w.requests()); n != 0 {
			t.Errorf("entries of a feed that is out of the reading list sent %d messages", n)
		}

		bob := w.user("bob", `{"filters":[{"search":"intitle:boring","actions":["read"]}]}`)
		w.feed(bob, func(f *store.Feed) { f.URL += "?bob" })
		phone := w.subscribe(bob, "/bob")
		w.publish("one", "boring one", "boring two")
		w.refresh()
		got := w.at("/bob")
		if len(got) != 1 {
			t.Fatalf("bob got %d messages, want 1", len(got))
		}
		// Only the entry no rule marked read counts.
		if m := opened(t, phone, got[0]); m.Body != "1 new entry: one" {
			t.Errorf("message to bob = %+v, want one new entry: one", m)
		}
		if n := len(w.at("/alice")); n != 0 {
			t.Errorf("alice, whose feed is out of the reading list, got %d messages", n)
		}
	})
}

// A push service that no longer knows the browser (404, 410) loses its
// subscription; one that fails, or cannot be reached, does not, and the other
// browsers of the user get their message all the same.
func TestPushServiceAnswers(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		alice := w.user("alice", ``)
		w.feed(alice, nil)
		for _, path := range []string{"/ok", "/gone", "/missing", "/broken"} {
			w.subscribe(alice, path)
		}
		w.status["/gone"], w.status["/missing"], w.status["/broken"] = http.StatusGone, http.StatusNotFound, http.StatusInternalServerError
		w.publish("one")
		w.refresh()

		subs, err := w.db.PushSubscriptions(context.Background(), alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		var left []string
		for _, s := range subs {
			left = append(left, strings.TrimPrefix(s.Endpoint, w.service.URL))
		}
		if strings.Join(left, " ") != "/ok /broken" {
			t.Errorf("subscriptions left = %v, want /ok and /broken", left)
		}
		for _, path := range []string{"/ok", "/gone", "/missing", "/broken"} {
			if n := len(w.at(path)); n != 1 {
				t.Errorf("%s got %d messages, want 1", path, n)
			}
		}
		logs := w.logs.String()
		if !strings.Contains(logs, "status=500") {
			t.Errorf("the refusal of the push service is not logged:\n%s", logs)
		}
		if strings.Contains(logs, "/broken") || strings.Contains(logs, "/gone") {
			t.Errorf("the log names the endpoint of a browser, which is a secret:\n%s", logs)
		}
	})
}

// A push service that is slow neither holds the refresh back nor fails it.
func TestSlowPushServiceDoesNotHoldRefreshBack(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		alice := w.user("alice", ``)
		w.feed(alice, nil)
		w.subscribe(alice, "/slow")
		w.hold = make(chan struct{})
		w.publish("one")

		done := make(chan error, 1)
		go func() {
			_, err := w.refresher.Run(context.Background(), refresh.Options{Force: true})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the refresh waits for the push service")
		}
		close(w.hold)
		w.notifier.Wait()
		if n := len(w.at("/slow")); n != 1 {
			t.Errorf("the message went %d times, want 1", n)
		}
	})
}

// The address of a browser is its own to name, so a message does not go to
// an internal address the installation has not allowed.
func TestInternalEndpointIsNotRequested(t *testing.T) {
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			w := newWorld(t, db, false)
			alice := w.user("alice", ``)
			w.feed(alice, nil)
			w.subscribe(alice, "/internal")
			w.publish("one")
			w.refresh()
			if n := len(w.requests()); n != 0 {
				t.Errorf("the push service at an internal address was requested %d times", n)
			}
			if w.subscriptions(alice) != 1 {
				t.Error("the subscription was removed over an address that was refused here")
			}
		})
	}
}
