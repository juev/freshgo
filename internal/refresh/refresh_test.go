package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// start is the clock of a test at its beginning; every feed created by the
// helpers has never been refreshed and is therefore due.
var start = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// world is a database, a web server and a Refresher over them.
type world struct {
	t        *testing.T
	db       *store.Store
	registry *hooks.Registry
	r        *Refresher
	clock    time.Time

	server *httptest.Server
	mu     sync.Mutex
	pages  map[string]http.HandlerFunc
	hits   map[string]int
}

// eachEngine runs the test against every available database engine.
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
			test(t, newWorld(t, db))
		})
	}
}

func newWorld(t *testing.T, db *store.Store) *world {
	t.Helper()
	w := &world{t: t, db: db, registry: &hooks.Registry{}, clock: start,
		pages: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		w.mu.Lock()
		w.hits[req.URL.Path]++
		page := w.pages[req.URL.Path]
		w.mu.Unlock()
		if page == nil {
			http.NotFound(rw, req)
			return
		}
		page(rw, req)
	}))
	t.Cleanup(w.server.Close)
	w.r = w.refresher()
	return w
}

// refresher returns one more Refresher over the same database and clock, as
// a second process would have.
func (w *world) refresher() *Refresher {
	w.t.Helper()
	host := strings.TrimPrefix(w.server.URL, "http://")
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{host}})
	if err != nil {
		w.t.Fatal(err)
	}
	r := New(w.db, client, w.registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return w.clock }
	return r
}

// serve makes the server answer path with the handler.
func (w *world) serve(path string, h http.HandlerFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pages[path] = h
}

// serveBody makes the server answer path with a document.
func (w *world) serveBody(path, contentType, body string) {
	w.serve(path, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(rw, body)
	})
}

func (w *world) hitCount(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits[path]
}

func (w *world) user(name string, settings string) *store.User {
	w.t.Helper()
	u := &store.User{Name: name, Settings: json.RawMessage(settings)}
	if err := w.db.CreateUser(context.Background(), u); err != nil {
		w.t.Fatal(err)
	}
	return u
}

// feed subscribes the user to a path of the server.
func (w *world) feed(u *store.User, path string, change func(*store.Feed)) *store.Feed {
	w.t.Helper()
	f := &store.Feed{UserID: u.ID, URL: w.server.URL + path, Name: "feed " + path}
	if change != nil {
		change(f)
	}
	if err := w.db.CreateFeed(context.Background(), f); err != nil {
		w.t.Fatal(err)
	}
	return f
}

// run refreshes and fails the test on an error of the run itself.
func (w *world) run(o Options) []Stats {
	w.t.Helper()
	stats, err := w.r.Run(context.Background(), o)
	if err != nil {
		w.t.Fatalf("Run: %v", err)
	}
	return stats
}

// runOne refreshes with a single user in the database and returns their stats.
func (w *world) runOne() Stats {
	w.t.Helper()
	stats := w.run(Options{})
	if len(stats) != 1 {
		w.t.Fatalf("Run returned stats for %d users, want 1", len(stats))
	}
	return stats[0]
}

// later moves the clock past the default refresh period.
func (w *world) later() {
	w.clock = w.clock.Add(2 * time.Hour)
}

func (w *world) storedFeed(f *store.Feed) *store.Feed {
	w.t.Helper()
	got, err := w.db.FeedByID(context.Background(), f.UserID, f.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

func (w *world) entries(f *store.Feed) []*store.Entry {
	w.t.Helper()
	entries, err := w.db.EntriesByFeed(context.Background(), f.UserID, f.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return entries
}

// entry returns the entry of the feed with the given guid.
func (w *world) entry(f *store.Feed, guid string) *store.Entry {
	w.t.Helper()
	for _, e := range w.entries(f) {
		if e.GUID == guid {
			return e
		}
	}
	w.t.Fatalf("feed %d has no entry %q", f.ID, guid)
	return nil
}

func titles(entries []*store.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Title)
	}
	return out
}

type item struct {
	guid, title, link, date, body string
}

// rss builds an RSS 2.0 document. Items are written as given: the last one
// is the one FreshRSS, and freshgo, stores first.
func rss(title string, items ...item) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>%s</title>`+
		`<link>https://example.org/</link><description>About %s</description>`, title, title)
	for _, it := range items {
		b.WriteString("<item>")
		if it.guid != "" {
			fmt.Fprintf(&b, `<guid isPermaLink="false">%s</guid>`, it.guid)
		}
		fmt.Fprintf(&b, "<title>%s</title>", it.title)
		if it.link != "" {
			fmt.Fprintf(&b, "<link>%s</link>", it.link)
		}
		if it.date != "" {
			fmt.Fprintf(&b, "<pubDate>%s</pubDate>", it.date)
		}
		fmt.Fprintf(&b, "<description>%s</description></item>", it.body)
	}
	b.WriteString("</channel></rss>")
	return b.String()
}

const rssType = "application/rss+xml; charset=utf-8"

func TestNewFeedIsFilledIn(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serveBody("/feed", rssType, rss("The Blog",
			item{guid: "b", title: "Second", link: "https://example.org/b", date: "Tue, 06 Oct 2026 10:00:00 GMT", body: "two"},
			item{guid: "a", title: "First", link: "https://example.org/a", date: "Mon, 05 Oct 2026 10:00:00 GMT", body: "one"},
			item{guid: "c", title: "Undated", link: "https://example.org/c", body: "three"},
		))
		f := w.feed(u, "/feed", func(f *store.Feed) { f.Name = "" })

		st := w.runOne()
		if want := (Stats{User: "alice", Refreshed: 1, NewEntries: 3}); st != want {
			t.Errorf("stats = %+v, want %+v", st, want)
		}
		got := w.storedFeed(f)
		if got.Name != "The Blog" || got.Website != "https://example.org/" || got.Description != "About The Blog" {
			t.Errorf("feed = name %q, website %q, description %q", got.Name, got.Website, got.Description)
		}
		if got.LastUpdate != start.Unix() || got.Error != 0 {
			t.Errorf("feed: last update %d, error %d; want %d, 0", got.LastUpdate, got.Error, start.Unix())
		}

		// Identifiers grow with the date; the undated entry is dated by the
		// refresh and so comes last.
		entries := w.entries(f)
		if want := []string{"First", "Second", "Undated"}; !reflect.DeepEqual(titles(entries), want) {
			t.Fatalf("entries in id order = %q, want %q", titles(entries), want)
		}
		for _, e := range entries {
			if e.LastSeen != start.Unix() || e.Hash == nil || e.IsRead || e.IsFavorite || e.LastModified != 0 {
				t.Errorf("entry %q: %+v", e.Title, e)
			}
		}
		if entries[0].Published != time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC).Unix() || entries[2].Published != start.Unix() {
			t.Errorf("dates: %d, %d", entries[0].Published, entries[2].Published)
		}
		if entries[0].Link != "https://example.org/a" || entries[0].Content != "one" {
			t.Errorf("entry: link %q, content %q", entries[0].Link, entries[0].Content)
		}
	})
}

// R3 in the small: refreshing an unchanged feed adds nothing, a new item adds
// exactly one entry with a greater identifier.
func TestRefreshAddsOnlyWhatIsNew(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		a := item{guid: "a", title: "First", link: "https://example.org/a", date: "Mon, 05 Oct 2026 10:00:00 GMT", body: "one"}
		b := item{guid: "b", title: "Second", link: "https://example.org/b", date: "Tue, 06 Oct 2026 10:00:00 GMT", body: "two"}
		w.serveBody("/feed", rssType, rss("Blog", b, a))
		f := w.feed(u, "/feed", nil)
		w.runOne()
		before := w.entries(f)

		w.later()
		if st := w.runOne(); st.Refreshed != 1 || st.NewEntries != 0 || st.UpdatedEntries != 0 {
			t.Errorf("unchanged feed: stats %+v", st)
		}
		after := w.entries(f)
		for _, e := range after {
			if e.LastSeen != w.clock.Unix() {
				t.Errorf("entry %q: last seen %d, want %d", e.Title, e.LastSeen, w.clock.Unix())
			}
			e.LastSeen = start.Unix()
		}
		if !reflect.DeepEqual(after, before) {
			t.Errorf("entries changed:\n got %+v\nwant %+v", after, before)
		}

		// An older item appears: it still gets the greatest identifier.
		old := item{guid: "old", title: "Old", link: "https://example.org/old", date: "Thu, 01 Oct 2026 10:00:00 GMT", body: "zero"}
		w.serveBody("/feed", rssType, rss("Blog", old, b, a))
		w.later()
		if st := w.runOne(); st.NewEntries != 1 || st.UpdatedEntries != 0 {
			t.Errorf("one new item: stats %+v", st)
		}
		entries := w.entries(f)
		if len(entries) != 3 || entries[2].Title != "Old" || entries[2].ID <= before[1].ID {
			t.Errorf("entries = %q, ids %d then %d", titles(entries), before[1].ID, entries[len(entries)-1].ID)
		}
	})
}

// R4: a changed entry is rewritten in place.
func TestChangedEntryIsUpdated(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		a := item{guid: "a", title: "First", link: "https://example.org/a", date: "Mon, 05 Oct 2026 10:00:00 GMT", body: "one"}
		b := item{guid: "b", title: "Second", link: "https://example.org/b", date: "Tue, 06 Oct 2026 10:00:00 GMT", body: "two"}
		w.serveBody("/feed", rssType, rss("Blog", b, a))
		f := w.feed(u, "/feed", nil)
		w.runOne()

		// The user reads and stars the first entry.
		first := w.entry(f, "a")
		first.IsRead, first.IsFavorite, first.LastUserModified = true, true, start.Unix()+60
		if err := w.db.UpdateEntry(ctx, first); err != nil {
			t.Fatal(err)
		}
		second := w.entry(f, "b")

		a.title, a.body = "First, corrected", "one and a half"
		w.serveBody("/feed", rssType, rss("Blog", b, a))
		w.later()
		if st := w.runOne(); st.NewEntries != 0 || st.UpdatedEntries != 1 {
			t.Errorf("stats %+v, want one updated entry", st)
		}
		got := w.entry(f, "a")
		if got.ID != first.ID || got.Title != "First, corrected" || got.Content != "one and a half" {
			t.Errorf("updated entry: id %d (was %d), title %q, content %q", got.ID, first.ID, got.Title, got.Content)
		}
		if !got.IsFavorite || !got.IsRead || got.LastUserModified != first.LastUserModified {
			t.Errorf("updated entry lost the user's state: %+v", got)
		}
		if got.LastModified != w.clock.Unix() || got.Published != first.Published {
			t.Errorf("updated entry: last modified %d, published %d", got.LastModified, got.Published)
		}
		if other := w.entry(f, "b"); other.LastModified != 0 || !reflect.DeepEqual(other.Hash, second.Hash) {
			t.Errorf("the unchanged entry was rewritten: %+v", other)
		}
	})
}

func TestChangedEntryBecomesUnreadWhenAsked(t *testing.T) {
	cases := []struct {
		name               string
		userSettings       string
		feedAttributes     string
		wantRead, wantHook bool
	}{
		{"by default it stays read", `{}`, `{}`, true, false},
		{"user setting", `{"mark_updated_article_unread":true}`, `{}`, false, true},
		{"feed attribute", `{}`, `{"mark_updated_article_unread":true}`, false, true},
		{"feed attribute wins over the user setting", `{"mark_updated_article_unread":true}`, `{"mark_updated_article_unread":false}`, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				var reported []string
				w.registry.EntryAutoUnread.Add(0, func(_ context.Context, a hooks.EntryAuto) bool {
					reported = append(reported, a.Entry.GUID+":"+a.Why)
					return false
				})
				u := w.user("alice", c.userSettings)
				a := item{guid: "a", title: "First", link: "https://example.org/a", body: "one"}
				w.serveBody("/feed", rssType, rss("Blog", a))
				f := w.feed(u, "/feed", func(f *store.Feed) { f.Attributes = json.RawMessage(c.feedAttributes) })
				w.runOne()
				e := w.entry(f, "a")
				e.IsRead, e.IsFavorite = true, true
				if err := w.db.UpdateEntry(context.Background(), e); err != nil {
					t.Fatal(err)
				}

				a.title = "First, corrected"
				w.serveBody("/feed", rssType, rss("Blog", a))
				w.later()
				w.runOne()
				got := w.entry(f, "a")
				if got.Title != "First, corrected" || got.IsRead != c.wantRead || !got.IsFavorite {
					t.Errorf("entry: title %q, read %v, favorite %v; want read %v", got.Title, got.IsRead, got.IsFavorite, c.wantRead)
				}
				if hooked := len(reported) > 0; hooked != c.wantHook {
					t.Errorf("EntryAutoUnread calls: %v", reported)
				} else if hooked && !reflect.DeepEqual(reported, []string{"a:updated_article"}) {
					t.Errorf("EntryAutoUnread calls: %v", reported)
				}
			})
		})
	}
}

// R4: an imported entry has no hash to compare with. The first refresh takes
// it as it is; from then on changes are noticed.
func TestImportedEntryIsNotTakenAsChanged(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		a := item{guid: "a", title: "Title in the feed", link: "https://example.org/a", body: "one"}
		w.serveBody("/feed", rssType, rss("Blog", a))
		f := w.feed(u, "/feed", nil)
		imported := &store.Entry{FeedID: f.ID, GUID: "a", Title: "Title as imported", Content: "as imported",
			ID: 1700000000000000, Published: 1700000000, LastSeen: 1700000000, IsRead: true}
		if err := w.db.InsertEntries(ctx, u.ID, []*store.Entry{imported}); err != nil {
			t.Fatal(err)
		}

		if st := w.runOne(); st.NewEntries != 0 || st.UpdatedEntries != 0 {
			t.Errorf("first refresh after import: stats %+v", st)
		}
		got := w.entry(f, "a")
		if got.ID != imported.ID || got.Title != "Title as imported" || got.Content != "as imported" || !got.IsRead || got.LastModified != 0 {
			t.Errorf("the imported entry was rewritten: %+v", got)
		}
		if got.Hash == nil || got.LastSeen != start.Unix() {
			t.Errorf("the imported entry got hash %x, last seen %d", got.Hash, got.LastSeen)
		}

		w.later()
		if st := w.runOne(); st.UpdatedEntries != 0 {
			t.Errorf("second refresh of the same feed: stats %+v", st)
		}
		a.title = "Title changed in the feed"
		w.serveBody("/feed", rssType, rss("Blog", a))
		w.later()
		if st := w.runOne(); st.UpdatedEntries != 1 {
			t.Errorf("refresh after a change: stats %+v", st)
		}
		if got := w.entry(f, "a"); got.ID != imported.ID || got.Title != "Title changed in the feed" {
			t.Errorf("entry after a change: %+v", got)
		}
	})
}

func TestRepeatedGUIDs(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)

		// One repeat among twenty items is tolerated: the item nearest to the
		// end of the document stands.
		var many []item
		for i := range 19 {
			many = append(many, item{guid: fmt.Sprintf("g%d", i), title: fmt.Sprintf("T%d", i), link: fmt.Sprintf("https://example.org/%d", i), body: "x"})
		}
		many = append(many, item{guid: "g0", title: "Repeat", link: "https://example.org/repeat", body: "x"})
		w.serveBody("/tolerated", rssType, rss("Blog", many...))
		tolerated := w.feed(u, "/tolerated", func(f *store.Feed) { f.Attributes = json.RawMessage(`{"keep":1}`) })

		// Every item with the same identifier: the feed is keyed by link
		// and date from now on.
		w.serveBody("/broken", rssType, rss("Blog",
			item{guid: "same", title: "A", link: "https://example.org/a", date: "Mon, 05 Oct 2026 10:00:00 GMT", body: "x"},
			item{guid: "same", title: "B", link: "https://example.org/b", date: "Mon, 05 Oct 2026 11:00:00 GMT", body: "x"},
			item{guid: "same", title: "C", link: "https://example.org/c", date: "Mon, 05 Oct 2026 12:00:00 GMT", body: "x"},
		))
		broken := w.feed(u, "/broken", func(f *store.Feed) { f.Attributes = json.RawMessage(`{"keep":1}`) })
		// The same feed with the key fixed by the user.
		forced := w.feed(u, "/broken", func(f *store.Feed) { f.Attributes = json.RawMessage(`{"unicityCriteriaForced":true}`) })
		// A feed FreshRSS once marked as keyed by link, whose links repeat too.
		w.serveBody("/same-links", rssType, rss("Blog",
			item{guid: "same", title: "A", link: "https://example.org/", date: "Mon, 05 Oct 2026 10:00:00 GMT", body: "x"},
			item{guid: "same", title: "B", link: "https://example.org/", date: "Mon, 05 Oct 2026 11:00:00 GMT", body: "x"},
		))
		legacy := w.feed(u, "/same-links", func(f *store.Feed) { f.Attributes = json.RawMessage(`{"hasBadGuids":true}`) })

		if st := w.runOne(); st.Failed != 0 || st.NewEntries != 19+3+1+2 {
			t.Errorf("stats %+v", st)
		}
		if got := string(w.storedFeed(legacy).Attributes); got != `{"unicityCriteria":"sha1:link_published"}` || len(w.entries(legacy)) != 2 {
			t.Errorf("legacy feed: attributes %s, %d entries", got, len(w.entries(legacy)))
		}
		entries := w.entries(tolerated)
		if len(entries) != 19 {
			t.Fatalf("tolerated feed: %d entries, want 19", len(entries))
		}
		if e := w.entry(tolerated, "g0"); e.Title != "Repeat" {
			t.Errorf("of two items with one GUID %q was stored, want the last in the document", e.Title)
		}
		if got := string(w.storedFeed(tolerated).Attributes); got != `{"keep":1}` {
			t.Errorf("tolerated feed: attributes %s", got)
		}

		if got := titles(w.entries(broken)); !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
			t.Errorf("broken feed: entries %q", got)
		}
		var attrs map[string]any
		if err := json.Unmarshal(w.storedFeed(broken).Attributes, &attrs); err != nil {
			t.Fatal(err)
		}
		if want := map[string]any{"unicityCriteria": "sha1:link_published", "keep": float64(1)}; !reflect.DeepEqual(attrs, want) {
			t.Errorf("broken feed: attributes %v, want %v", attrs, want)
		}
		if got := titles(w.entries(forced)); !reflect.DeepEqual(got, []string{"C"}) {
			t.Errorf("feed with a forced key: entries %q", got)
		}

		// The new key is stable: nothing is added on the next refresh.
		w.later()
		if st := w.runOne(); st.NewEntries != 0 || st.UpdatedEntries != 0 {
			t.Errorf("second refresh: stats %+v", st)
		}
	})
}

func TestWhichFeedsAreRefreshed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		var order []string
		w.registry.FeedsListBeforeActualize.Add(0, func(_ context.Context, feeds []*store.Feed) ([]*store.Feed, bool) {
			for _, f := range feeds {
				order = append(order, f.Name)
			}
			return feeds, true
		})
		var maintained []string
		w.registry.UserMaintenance.Add(0, func(_ context.Context, u *store.User) { maintained = append(maintained, u.Name) })

		now := start.Unix()
		u := w.user("alice", `{"ttl_default":1800}`)
		for _, path := range []string{"/never", "/fresh", "/stale", "/own-ttl-fresh", "/own-ttl-stale", "/muted", "/failed", "/off"} {
			w.serveBody(path, rssType, rss("Blog", item{guid: "a", title: "A", body: "x"}))
		}
		w.feed(u, "/never", func(f *store.Feed) { f.Name = "never" })
		w.feed(u, "/fresh", func(f *store.Feed) { f.Name, f.LastUpdate = "fresh", now-1800 })
		w.feed(u, "/stale", func(f *store.Feed) { f.Name, f.LastUpdate = "stale", now-1801 })
		w.feed(u, "/own-ttl-fresh", func(f *store.Feed) { f.Name, f.LastUpdate, f.TTL = "own-ttl-fresh", now-5000, 7200 })
		w.feed(u, "/own-ttl-stale", func(f *store.Feed) { f.Name, f.LastUpdate, f.TTL = "own-ttl-stale", now-7201, 7200 })
		w.feed(u, "/muted", func(f *store.Feed) { f.Name, f.TTL = "muted", -3600 })
		// A failed feed is tried again on every run, whatever its period.
		w.feed(u, "/failed", func(f *store.Feed) { f.Name, f.LastUpdate, f.Error = "failed", now-90000, now-60 })
		disabled := w.user("bob", `{"enabled":false}`)
		w.feed(disabled, "/off", nil)

		stats := w.run(Options{})
		if len(stats) != 1 || stats[0].User != "alice" || stats[0].Refreshed != 4 {
			t.Errorf("stats = %+v, want 4 feeds of alice", stats)
		}
		for path, want := range map[string]int{"/never": 1, "/fresh": 0, "/stale": 1, "/own-ttl-fresh": 0,
			"/own-ttl-stale": 1, "/muted": 0, "/failed": 1, "/off": 0} {
			if got := w.hitCount(path); got != want {
				t.Errorf("%s was requested %d times, want %d", path, got, want)
			}
		}
		// Longest without an attempt first.
		if want := []string{"never", "muted", "own-ttl-stale", "own-ttl-fresh", "stale", "fresh", "failed"}; !reflect.DeepEqual(order, want) {
			t.Errorf("feeds were offered as %q, want %q", order, want)
		}
		if !reflect.DeepEqual(maintained, []string{"alice"}) {
			t.Errorf("UserMaintenance ran for %q", maintained)
		}

		// Forced: every feed that is not muted.
		if stats := w.run(Options{Force: true}); stats[0].Refreshed != 6 {
			t.Errorf("forced run: stats %+v, want 6 feeds", stats)
		}
		if w.hitCount("/fresh") != 1 || w.hitCount("/muted") != 0 || w.hitCount("/off") != 0 {
			t.Errorf("forced run requested: fresh %d, muted %d, disabled user's %d", w.hitCount("/fresh"), w.hitCount("/muted"), w.hitCount("/off"))
		}
	})
}

func TestUnchangedFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		body := rss("Blog",
			item{guid: "b", title: "Second", link: "https://example.org/b", body: "two"},
			item{guid: "a", title: "First", link: "https://example.org/a", body: "one"})
		var conditional atomic.Int32
		w.serve("/feed", func(rw http.ResponseWriter, req *http.Request) {
			rw.Header().Set("ETag", `"v1"`)
			if req.Header.Get("If-None-Match") == `"v1"` && req.Header.Get("If-Modified-Since") == "Tue, 06 Oct 2026 09:00:00 GMT" {
				conditional.Add(1)
				rw.WriteHeader(http.StatusNotModified)
				return
			}
			rw.Header().Set("Last-Modified", "Tue, 06 Oct 2026 09:00:00 GMT")
			rw.Header().Set("Content-Type", rssType)
			_, _ = io.WriteString(rw, body)
		})
		f := w.feed(u, "/feed", nil)
		w.runOne()
		if got := w.storedFeed(f); got.HTTPETag != `"v1"` || got.HTTPLastModified != "Tue, 06 Oct 2026 09:00:00 GMT" {
			t.Fatalf("validators were not kept: %q, %q", got.HTTPETag, got.HTTPLastModified)
		}
		// An entry the feed no longer listed at the last refresh.
		gone := &store.Entry{FeedID: f.ID, GUID: "gone", Title: "Gone", LastSeen: start.Unix() - 86400}
		if err := w.db.InsertEntries(context.Background(), u.ID, []*store.Entry{gone}); err != nil {
			t.Fatal(err)
		}
		before := w.entries(f)

		w.later()
		if st := w.runOne(); st.Refreshed != 1 || st.Failed != 0 || st.NewEntries != 0 || st.UpdatedEntries != 0 {
			t.Errorf("stats %+v", st)
		}
		if conditional.Load() != 1 {
			t.Fatalf("the server saw %d conditional requests, want 1", conditional.Load())
		}
		got := w.storedFeed(f)
		if got.LastUpdate != w.clock.Unix() || got.Error != 0 || got.HTTPETag != `"v1"` || got.HTTPLastModified == "" {
			t.Errorf("feed after 304: %+v", got)
		}
		// The listed entries are seen again; nothing else about them changes.
		after := w.entries(f)
		for i, e := range after {
			wantSeen := w.clock.Unix()
			if e.GUID == "gone" {
				wantSeen = start.Unix() - 86400
			}
			if e.LastSeen != wantSeen {
				t.Errorf("entry %q: last seen %d, want %d", e.GUID, e.LastSeen, wantSeen)
			}
			e.LastSeen = before[i].LastSeen
		}
		if !reflect.DeepEqual(after, before) {
			t.Errorf("entries changed on 304:\n got %+v\nwant %+v", after, before)
		}
	})
}

func TestFeedAddressFollowsPermanentRedirect(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serveBody("/new", rssType, rss("Blog", item{guid: "a", title: "A", body: "x"}))
		w.serve("/moved", func(rw http.ResponseWriter, req *http.Request) {
			http.Redirect(rw, req, "/moved-again", http.StatusMovedPermanently)
		})
		w.serve("/moved-again", func(rw http.ResponseWriter, req *http.Request) {
			http.Redirect(rw, req, "/new", http.StatusPermanentRedirect)
		})
		w.serve("/elsewhere-for-now", func(rw http.ResponseWriter, req *http.Request) {
			http.Redirect(rw, req, "/new", http.StatusFound)
		})
		moved := w.feed(u, "/moved", nil)
		temporary := w.feed(u, "/elsewhere-for-now", nil)
		// An address that is written differently once parsed, with
		// credentials in it: not a redirect, so it stays as the user gave it.
		odd := strings.Replace(w.server.URL, "http://", "HTTP://bob:pa!ss@", 1) + "/new|x"
		w.serveBody("/new|x", rssType, rss("Blog", item{guid: "a", title: "A", body: "x"}))
		spelled := w.feed(u, "/", func(f *store.Feed) { f.URL = odd })

		if st := w.runOne(); st.Refreshed != 3 || st.NewEntries != 3 {
			t.Errorf("stats %+v", st)
		}
		if got := w.storedFeed(spelled).URL; got != odd {
			t.Errorf("feed without a redirect: URL %q, want %q", got, odd)
		}
		if got := w.storedFeed(moved).URL; got != w.server.URL+"/new" {
			t.Errorf("feed behind 301 and 308: URL %q, want %q", got, w.server.URL+"/new")
		}
		if got := w.storedFeed(temporary).URL; got != w.server.URL+"/elsewhere-for-now" {
			t.Errorf("feed behind 302: URL %q changed", got)
		}
	})
}

func TestFailedFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{"ttl_default":1800}`)
		good := rss("Blog", item{guid: "a", title: "A", body: "x"})
		w.serve("/broken", func(rw http.ResponseWriter, _ *http.Request) { http.Error(rw, "boom", http.StatusInternalServerError) })
		w.serveBody("/not-a-feed", "text/html", "<html><body>Hello</body></html>")
		w.serveBody("/no-items", "text/html", "<html><body>Hello</body></html>")
		w.serve("/gone", func(rw http.ResponseWriter, _ *http.Request) { http.Error(rw, "gone", http.StatusGone) })
		w.serve("/gone-own-ttl", func(rw http.ResponseWriter, _ *http.Request) { http.Error(rw, "gone", http.StatusGone) })
		w.serveBody("/fine", rssType, good)

		broken := w.feed(u, "/broken", nil)
		notFeed := w.feed(u, "/not-a-feed", nil)
		noItems := w.feed(u, "/no-items", func(f *store.Feed) {
			f.Kind, f.Attributes = 10, json.RawMessage(`{"xpath":{"item":"//article","itemTitle":"h2"}}`)
		})
		badSettings := w.feed(u, "/fine", func(f *store.Feed) {
			f.Attributes = json.RawMessage(`{"curl_params":{"10004":"proxy.example","101":4}}`)
		})
		gone := w.feed(u, "/gone", nil)
		goneOwnTTL := w.feed(u, "/gone-own-ttl", func(f *store.Feed) { f.TTL = 7200 })
		fine := w.feed(u, "/fine", nil)

		// Feeds are refreshed in parallel, so a handler guards what it shares.
		var mu sync.Mutex
		var parsed []string
		w.registry.ParseAfter.Add(0, func(_ context.Context, p hooks.Parsed) bool {
			mu.Lock()
			defer mu.Unlock()
			if p.Document == nil {
				parsed = append(parsed, fmt.Sprintf("%d failed", p.Feed.ID))
			} else {
				parsed = append(parsed, fmt.Sprintf("%d ok", p.Feed.ID))
			}
			return false
		})

		st := w.runOne()
		if st.Refreshed != 1 || st.Failed != 6 || st.NewEntries != 1 {
			t.Errorf("stats %+v, want 1 refreshed and 6 failed", st)
		}
		for name, f := range map[string]*store.Feed{"500": broken, "not a feed": notFeed, "no items": noItems, "bad settings": badSettings} {
			if got := w.storedFeed(f); got.Error != start.Unix() || got.LastUpdate != 0 || got.TTL != f.TTL {
				t.Errorf("%s: error %d, last update %d, ttl %d", name, got.Error, got.LastUpdate, got.TTL)
			}
		}
		if got := w.storedFeed(gone); got.Error != start.Unix() || got.TTL != -1800 {
			t.Errorf("410: error %d, ttl %d; want the feed muted with the user's period", got.Error, got.TTL)
		}
		if got := w.storedFeed(goneOwnTTL); got.TTL != -7200 {
			t.Errorf("410 with a period of its own: ttl %d, want -7200", got.TTL)
		}
		if got := w.storedFeed(fine); got.Error != 0 || got.LastUpdate != start.Unix() {
			t.Errorf("the good feed: %+v", got)
		}
		if len(parsed) != 7 {
			t.Errorf("ParseAfter calls: %q", parsed)
		}

		// The failed feeds are tried on the next run although their period
		// has not passed; the muted ones are not.
		w.serveBody("/broken", rssType, good)
		w.clock = w.clock.Add(time.Minute)
		st = w.runOne()
		if st.Refreshed != 1 || st.Failed != 3 {
			t.Errorf("next run: stats %+v, want 1 recovered and 3 failed", st)
		}
		if got := w.storedFeed(broken); got.Error != 0 || got.LastUpdate != w.clock.Unix() || len(w.entries(broken)) != 1 {
			t.Errorf("recovered feed: %+v", got)
		}
		if w.hitCount("/gone") != 1 || w.hitCount("/gone-own-ttl") != 1 {
			t.Errorf("muted feeds were requested again: %d, %d", w.hitCount("/gone"), w.hitCount("/gone-own-ttl"))
		}
	})
}

// R8 in the pipeline.
func TestHooksInThePipeline(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		a := item{guid: "a", title: "Keep", link: "https://example.org/a", body: "x"}
		b := item{guid: "b", title: "Drop on insert", link: "https://example.org/b", body: "x"}
		c := item{guid: "c", title: "Drop on add", link: "https://example.org/c", body: "x"}
		w.serve("/feed", func(rw http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-From-Hook") != "yes" {
				http.Error(rw, "no header", http.StatusForbidden)
				return
			}
			rw.Header().Set("Content-Type", rssType)
			_, _ = io.WriteString(rw, rss("Blog", c, b, a))
		})
		w.serveBody("/skipped", rssType, rss("Blog", a))
		f := w.feed(u, "/feed", nil)
		w.feed(u, "/skipped", func(f *store.Feed) { f.Name = "skip me" })

		var mu sync.Mutex
		var calls []string
		note := func(s string) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, s)
		}
		w.registry.FeedBeforeActualize.Add(0, func(_ context.Context, f *store.Feed) (*store.Feed, bool) {
			return f, f.Name != "skip me"
		})
		w.registry.FetchBefore.Add(0, func(_ context.Context, a hooks.Fetch) bool {
			a.Request.Params.Header = http.Header{"X-From-Hook": {"yes"}}
			return false
		})
		w.registry.ParseAfter.Add(0, func(_ context.Context, p hooks.Parsed) bool {
			for _, it := range p.Document.Items {
				note("parsed " + it.GUID)
				it.Tags = append(it.Tags, "from-hook")
			}
			return false
		})
		// Two handlers of different priority, added in reverse: the second
		// gets what the first returned.
		w.registry.EntryBeforeInsert.Add(5, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
			note("insert-late " + e.Title)
			return e, true
		})
		w.registry.EntryBeforeInsert.Add(1, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
			if e.Title == "Drop on insert" {
				return nil, false
			}
			e.Title += "!"
			return e, true
		})
		w.registry.EntryBeforeAdd.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
			note("add " + e.Title)
			return e, e.Title != "Drop on add!"
		})
		w.registry.EntryBeforeUpdate.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
			note("update " + e.Title)
			e.Content += " (seen by the hook)"
			return e, true
		})

		st := w.runOne()
		if st.Refreshed != 1 || st.Failed != 0 || st.NewEntries != 1 {
			t.Errorf("stats %+v, want one feed and one new entry", st)
		}
		if w.hitCount("/skipped") != 0 {
			t.Error("a feed taken out by FeedBeforeActualize was requested")
		}
		entries := w.entries(f)
		if len(entries) != 1 || entries[0].Title != "Keep!" || !reflect.DeepEqual(entries[0].Tags, []string{"from-hook"}) {
			t.Fatalf("stored entries: %+v", entries)
		}
		want := []string{"parsed a", "parsed b", "parsed c", "insert-late Keep!", "add Keep!", "insert-late Drop on add!", "add Drop on add!"}
		if !reflect.DeepEqual(calls, want) {
			t.Errorf("hook calls:\n got %q\nwant %q", calls, want)
		}

		// A changed entry goes through EntryBeforeInsert and EntryBeforeUpdate.
		// The dropped ones are offered again, as they are not stored.
		calls = nil
		a.body = "changed"
		w.later()
		if st := w.runOne(); st.NewEntries != 0 || st.UpdatedEntries != 1 {
			t.Errorf("stats %+v, want one updated entry", st)
		}
		if got := w.entry(f, "a"); got.Content != "changed (seen by the hook)" || got.Title != "Keep!" {
			t.Errorf("updated entry: %+v", got)
		}
		want = []string{"parsed a", "parsed b", "parsed c", "insert-late Keep!", "update Keep!", "insert-late Drop on add!", "add Drop on add!"}
		if !reflect.DeepEqual(calls, want) {
			t.Errorf("hook calls on update:\n got %q\nwant %q", calls, want)
		}
	})
}

// Two processes refreshing the same new feed at once store each entry once.
func TestConcurrentRefreshOfOneFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		var items []item
		for i := range 30 {
			items = append(items, item{guid: fmt.Sprintf("g%d", i), title: fmt.Sprintf("T%d", i), link: fmt.Sprintf("https://example.org/%d", i), body: "x"})
		}
		w.serveBody("/feed", rssType, rss("Blog", items...))
		f := w.feed(u, "/feed", nil)

		const processes = 4
		var wg sync.WaitGroup
		results := make(chan []Stats, processes)
		for range processes {
			r := w.refresher()
			wg.Add(1)
			go func() {
				defer wg.Done()
				stats, err := r.Run(context.Background(), Options{})
				if err != nil {
					t.Errorf("Run: %v", err)
				}
				results <- stats
			}()
		}
		wg.Wait()
		close(results)
		added := 0
		for stats := range results {
			for _, st := range stats {
				if st.Failed != 0 {
					t.Errorf("stats %+v", st)
				}
				added += st.NewEntries
			}
		}
		if n := len(w.entries(f)); n != 30 || added != 30 {
			t.Errorf("%d entries stored, %d reported as new; want 30 and 30", n, added)
		}
		if got := w.storedFeed(f); got.Error != 0 {
			t.Errorf("feed is marked as failing: %+v", got)
		}
	})
}

func TestScheduleRunsUntilCancelled(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serveBody("/feed", rssType, rss("Blog", item{guid: "a", title: "A", body: "x"}))
		w.feed(u, "/feed", nil)
		// Every look at the clock finds the feed due again.
		var ticks atomic.Int64
		w.r.now = func() time.Time { return start.Add(time.Duration(ticks.Add(1)) * 2 * time.Hour) }

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			w.r.Schedule(ctx, 10*time.Millisecond)
			close(done)
		}()
		deadline := time.After(10 * time.Second)
		for w.hitCount("/feed") < 3 {
			select {
			case <-deadline:
				t.Fatalf("the feed was requested %d times in 10 s, want 3", w.hitCount("/feed"))
			case <-time.After(5 * time.Millisecond):
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Schedule did not return after the context was cancelled")
		}
	})
}

func TestScrapedFeedKinds(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serve("/page", func(rw http.ResponseWriter, req *http.Request) {
			if !strings.HasPrefix(req.Header.Get("Accept"), "text/html") {
				http.Error(rw, "wrong Accept", http.StatusNotAcceptable)
				return
			}
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(rw, `<html><body><article><h2>One</h2><a href="/one">read</a></article>`+
				`<article><h2>Two</h2><a href="/two">read</a></article></body></html>`)
		})
		w.serve("/api", func(rw http.ResponseWriter, req *http.Request) {
			if !strings.HasPrefix(req.Header.Get("Accept"), "application/json") {
				http.Error(rw, "wrong Accept", http.StatusNotAcceptable)
				return
			}
			rw.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(rw, `{"posts":[{"t":"J1","u":"https://example.org/j1"},{"t":"J2","u":"https://example.org/j2"}]}`)
		})
		page := w.feed(u, "/page", func(f *store.Feed) {
			f.Kind, f.Attributes = 10, json.RawMessage(`{"xpath":{"item":"//article","itemTitle":"descendant::h2","itemUri":"descendant::a/@href"}}`)
		})
		api := w.feed(u, "/api", func(f *store.Feed) {
			f.Kind, f.Attributes = 30, json.RawMessage(`{"json_dotnotation":{"item":"posts","itemTitle":"t","itemUri":"u"}}`)
		})

		if st := w.runOne(); st.Failed != 0 || st.NewEntries != 4 {
			t.Fatalf("stats %+v", st)
		}
		pageURL, _ := url.Parse(w.server.URL)
		got := map[string]string{}
		for _, e := range append(w.entries(page), w.entries(api)...) {
			got[e.Title] = e.Link
		}
		want := map[string]string{"One": "http://" + pageURL.Host + "/one", "Two": "http://" + pageURL.Host + "/two",
			"J1": "https://example.org/j1", "J2": "https://example.org/j2"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entries %v, want %v", got, want)
		}
		w.later()
		if st := w.runOne(); st.NewEntries != 0 || st.UpdatedEntries != 0 {
			t.Errorf("second refresh: stats %+v", st)
		}
	})
}
