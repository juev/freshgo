package miniflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/miniflux"
	"github.com/juev/freshgo/internal/miniflux/minifluxtest"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
	"github.com/juev/freshgo/internal/transfer"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func client(t *testing.T, s *minifluxtest.Server) *fetch.Client {
	t.Helper()
	c, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{s.Host}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// R1: the account is read in full: every page of entries, the states, the
// tags, and the settings that have an equivalent.
func TestRead(t *testing.T) {
	s := minifluxtest.New(t)
	remote, err := miniflux.Read(context.Background(), client(t, s), s.URL+"/", minifluxtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"All", "News", "Quiet", "Empty"}; !slices.Equal(remote.Categories, want) {
		t.Errorf("categories = %v, want %v", remote.Categories, want)
	}

	// Pages of a hundred, until one is short; the removed entry stays out.
	var pages []string
	for _, r := range s.Requests() {
		if strings.HasPrefix(r, "/v1/entries") {
			pages = append(pages, r)
		}
	}
	if len(pages) != 3 || len(remote.Entries) != minifluxtest.Entries {
		t.Fatalf("%d pages, %d entries; want 3 and %d", len(pages), len(remote.Entries), minifluxtest.Entries)
	}
	for _, want := range []string{"after_entry_id=100", "after_entry_id=200"} {
		if !strings.Contains(pages[1]+pages[2], want) {
			t.Errorf("no page asks for %s: %v", want, pages)
		}
	}

	blog := remote.Feeds[0]
	want := transfer.RemoteFeed{
		Key: "1", URL: "https://site1.example/feed.xml", Site: "https://site1.example/", Title: "Blog", Category: "News",
		HTTPAuth: "reader:secret", UserAgent: "Bot/1", Cookie: "a=b", Selector: "article.post",
	}
	if blog != want {
		t.Errorf("feed Blog = %+v\nwant %+v", blog, want)
	}
	if plain := remote.Feeds[1]; plain.Selector != "" || plain.Automatic || plain.Muted || plain.CategoryOnly || plain.HTTPAuth != "" {
		t.Errorf("feed Plain = %+v, want one without settings", plain)
	}
	paused := remote.Feeds[2]
	if !paused.Muted || !paused.CategoryOnly || !paused.Automatic || paused.Selector != "" {
		t.Errorf("feed Paused = %+v, want muted, in its category only, with the page found without a selector", paused)
	}
	if len(remote.Warnings) != 4 {
		t.Fatalf("warnings = %q, want one for each of rewrite, block, keep rules and the proxy", remote.Warnings)
	}
	for _, w := range []string{"rewrite rules not carried over, set on 1 feeds: Plain", "block rules", "keep rules", "proxy of Miniflux"} {
		if !slices.ContainsFunc(remote.Warnings, func(x string) bool { return strings.Contains(x, w) }) {
			t.Errorf("no warning with %q in %q", w, remote.Warnings)
		}
	}

	byID := map[string]transfer.RemoteEntry{}
	for _, e := range remote.Entries {
		byID[e.ID] = e
	}
	e5 := byID["hash005"]
	if e5.Read || e5.Starred || !slices.Equal(e5.Labels, []string{"go", "rss"}) || e5.Feed != "3" || e5.Title != "Entry 5" ||
		e5.Author != "Ann" || e5.URL != "https://site3.example/post/5" || e5.Content != "<p>Body 5</p>" ||
		e5.Published != time.Date(2026, 9, 1, 12, 5, 0, 0, time.FixedZone("", 3*3600)).Unix() {
		t.Errorf("entry 5 = %+v", e5)
	}
	if e10 := byID["hash010"]; !e10.Read || !e10.Starred || e10.Labels != nil {
		t.Errorf("entry 10 = %+v, want read and starred", e10)
	}
	if e7 := byID["hash007"]; e7.URL != "" {
		t.Errorf("entry 7 = %+v, want one without an address", e7)
	}
	if _, removed := byID["hash231"]; removed {
		t.Error("the removed entry was read")
	}
}

// R2: the server is asked with the token, and a refusal, a bad address, and
// a failure half way are told apart; nothing is returned of a failed read.
func TestReadFailures(t *testing.T) {
	s := minifluxtest.New(t)
	ctx := context.Background()
	if _, err := miniflux.Read(ctx, client(t, s), s.URL, "wrong"); !errors.Is(err, miniflux.ErrToken) {
		t.Errorf("wrong token: %v, want ErrToken", err)
	}
	for _, address := range []string{"", "ftp://x.example", "not a url", "https://user:pw@x.example"} {
		if _, err := miniflux.Read(ctx, client(t, s), address, minifluxtest.Token); !errors.Is(err, miniflux.ErrAddress) {
			t.Errorf("address %q: %v, want ErrAddress", address, err)
		}
	}
	s.FailEntriesFrom(2)
	remote, err := miniflux.Read(ctx, client(t, s), s.URL, minifluxtest.Token)
	var status *fetch.StatusError
	if !errors.As(err, &status) || status.Code != 500 || errors.Is(err, miniflux.ErrToken) || remote != nil {
		t.Errorf("failure on the second page: %v, %v; want a 500 and no account", remote, err)
	}
	// The guard of the client holds: this server is not on its allowlist.
	guarded, err := fetch.New(fetch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := miniflux.Read(ctx, guarded, s.URL, minifluxtest.Token); !errors.Is(err, fetch.ErrForbiddenAddress) {
		t.Errorf("server on a private address: %v, want ErrForbiddenAddress", err)
	}
}

type world struct {
	t  *testing.T
	db *store.Store
	u  *store.User
}

func eachEngine(t *testing.T, test func(t *testing.T, w *world)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			u := &store.User{Name: "alice", Settings: json.RawMessage(`{}`)}
			if err := db.CreateUser(context.Background(), u); err != nil {
				t.Fatal(err)
			}
			test(t, &world{t, db, u})
		})
	}
}

func (w *world) read(s *minifluxtest.Server) *transfer.Remote {
	w.t.Helper()
	remote, err := miniflux.Read(context.Background(), client(w.t, s), s.URL, minifluxtest.Token)
	if err != nil {
		w.t.Fatal(err)
	}
	return remote
}

// state is what the account holds, to tell whether an import changed it.
type state struct {
	Categories []string
	Feeds      []string
	Entries    int
	Labels     []string
}

func (w *world) state() state {
	w.t.Helper()
	ctx := context.Background()
	var st state
	categories, err := w.db.Categories(ctx, w.u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, c := range categories {
		st.Categories = append(st.Categories, c.Name)
	}
	feeds, err := w.db.Feeds(ctx, w.u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, f := range feeds {
		st.Feeds = append(st.Feeds, f.URL)
		entries, err := w.db.EntriesByFeed(ctx, w.u.ID, f.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		st.Entries += len(entries)
	}
	tags, err := w.db.Tags(ctx, w.u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, tag := range tags {
		st.Labels = append(st.Labels, tag.Name)
	}
	return st
}

func (w *world) entries() map[string]*store.Entry {
	w.t.Helper()
	ctx := context.Background()
	byLink := map[string]*store.Entry{}
	feeds, err := w.db.Feeds(ctx, w.u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, f := range feeds {
		entries, err := w.db.EntriesByFeed(ctx, w.u.ID, f.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, e := range entries {
			byLink[e.Title] = e
		}
	}
	return byLink
}

var options = transfer.Options{Now: now}

// R3: an account is written into an existing user with its categories, the
// settings freshgo has, the states and the tags as labels; a second run
// adds nothing twice and catches up with the changed states.
func TestImport(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		s := minifluxtest.New(t)
		report, err := transfer.ImportRemote(ctx, w.db, &hooks.Registry{}, w.u, w.read(s), options)
		if err != nil {
			t.Fatal(err)
		}
		if report.Feeds != 3 || report.Entries != minifluxtest.Entries || report.Updated != 0 || report.Incomplete {
			t.Errorf("report = %+v, want 3 feeds and %d entries", report, minifluxtest.Entries)
		}
		if len(report.Warnings) != 4 {
			t.Errorf("warnings = %q, want the four of the read", report.Warnings)
		}

		st := w.state()
		for _, name := range []string{"News", "Quiet", "Empty"} {
			if !slices.Contains(st.Categories, name) {
				t.Errorf("categories %v: no %s", st.Categories, name)
			}
		}
		feeds, err := w.db.Feeds(ctx, w.u.ID)
		if err != nil {
			t.Fatal(err)
		}
		byURL := map[string]*store.Feed{}
		for _, f := range feeds {
			byURL[f.URL] = f
		}
		blog, plain, paused := byURL["https://site1.example/feed.xml"], byURL["https://site2.example/feed.xml"], byURL["https://site3.example/feed.xml"]
		if blog == nil || plain == nil || paused == nil {
			t.Fatalf("feeds = %v", st.Feeds)
		}
		if blog.Name != "Blog" || blog.Website != "https://site1.example/" || blog.PathEntries != "article.post" ||
			blog.HTTPAuth != "reader:secret" || blog.Priority != store.PriorityMain || blog.TTL != 0 {
			t.Errorf("Blog = %+v", blog)
		}
		params, err := fetch.FeedParams(blog.HTTPAuth, blog.Attributes)
		if err != nil || params.UserAgent != "Bot/1" || params.Cookie != "a=b" || params.BasicAuth != "reader:secret" {
			t.Errorf("request settings of Blog = %+v, %v", params, err)
		}
		var attrs map[string]any
		_ = json.Unmarshal(blog.Attributes, &attrs)
		if attrs["unicityCriteria"] != "link" || attrs["unicityCriteriaForced"] != true {
			t.Errorf("attributes of Blog = %s, want entries told by their links", blog.Attributes)
		}
		if paused.TTL >= 0 || paused.Priority != 0 || paused.PathEntries != "" || !strings.Contains(string(paused.Attributes), `"path_entries_auto":true`) {
			t.Errorf("Paused = %+v %s, want muted, in its category only, with the page found without a selector", paused, paused.Attributes)
		}
		if plain.TTL != 0 || plain.CategoryID != store.DefaultCategoryID && plain.Priority != store.PriorityMain {
			t.Errorf("Plain = %+v", plain)
		}

		entries := w.entries()
		if len(entries) != minifluxtest.Entries {
			t.Fatalf("%d entries stored, want %d", len(entries), minifluxtest.Entries)
		}
		if e := entries["Entry 10"]; !e.IsRead || !e.IsFavorite || e.GUID != "https://site2.example/post/10" || e.Link != e.GUID || e.Authors[0] != "Ann" {
			t.Errorf("Entry 10 = %+v", e)
		}
		if e := entries["Entry 5"]; e.IsRead || e.IsFavorite {
			t.Errorf("Entry 5 = %+v, want unread and not starred", e)
		}
		if e := entries["Entry 7"]; e.GUID != "hash007" || e.Link != "" {
			t.Errorf("Entry 7 = %+v, want the hash for the identifier", e)
		}
		labels, err := w.db.Tags(ctx, w.u.ID)
		if err != nil || len(labels) != 2 {
			t.Fatalf("labels = %v, %v; want go and rss", labels, err)
		}
		for _, l := range labels {
			tagged, err := w.db.ListEntries(ctx, w.u.ID, store.EntryQuery{Set: store.EntrySet{LabelID: l.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if want := map[string]int{"go": 10, "rss": 1}[l.Name]; len(tagged) != want {
				t.Errorf("label %s is on %d entries, want %d", l.Name, len(tagged), want)
			}
		}

		// Run again after the account changed: nothing is added, the states follow.
		s.Change(5, "read", true, "go", "later")
		s.Change(10, "unread", true)
		before := w.state()
		report, err = transfer.ImportRemote(ctx, w.db, &hooks.Registry{}, w.u, w.read(s), options)
		if err != nil {
			t.Fatal(err)
		}
		if report.Feeds != 0 || report.Entries != 0 || report.Updated != minifluxtest.Entries {
			t.Errorf("second report = %+v, want %d entries updated and none added", report, minifluxtest.Entries)
		}
		after := w.state()
		if after.Entries != before.Entries || !slices.Equal(after.Feeds, before.Feeds) {
			t.Errorf("second import changed the account: %+v, was %+v", after, before)
		}
		entries = w.entries()
		if e := entries["Entry 5"]; !e.IsRead || !e.IsFavorite {
			t.Errorf("Entry 5 after the change = %+v, want read and starred", e)
		}
		if e := entries["Entry 10"]; e.IsRead {
			t.Errorf("Entry 10 after the change = %+v, want unread", e)
		}
		if !slices.Contains(after.Labels, "later") {
			t.Errorf("labels %v: no later", after.Labels)
		}
	})
}

// R4: the limits of the user hold, and what was left out is told.
func TestImportLimits(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		s := minifluxtest.New(t)
		report, err := transfer.ImportRemote(context.Background(), w.db, &hooks.Registry{}, w.u, w.read(s), transfer.Options{
			Now: now, MaxFeeds: 2, MaxCategories: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		st := w.state()
		if report.Feeds != 2 || len(st.Feeds) != 2 || !report.Incomplete {
			t.Errorf("report = %+v, feeds %v; want two feeds and an incomplete import", report, st.Feeds)
		}
		// The default category and one more.
		if len(st.Categories) != 2 {
			t.Errorf("categories = %v, want 2", st.Categories)
		}
		joined := strings.Join(report.Warnings, "\n")
		for _, want := range []string{"1 feeds not imported, the limit of feeds is reached", "not made, the limit of categories is reached", "entries not imported"} {
			if !strings.Contains(joined, want) {
				t.Errorf("no %q in the warnings:\n%s", want, joined)
			}
		}
		// Entries of the feed past the limit (every third, from the second) are not imported.
		if st.Entries != 153 || !strings.Contains(joined, "77 entries not imported") {
			t.Errorf("%d entries stored, want 153; warnings:\n%s", st.Entries, joined)
		}
	})
}

// R5: a failure while writing leaves the account as it was.
func TestImportFailureChangesNothing(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		s := minifluxtest.New(t)
		remote := w.read(s)
		before := w.state()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		registry := &hooks.Registry{}
		// The second feed is stored when the context is gone: the database refuses.
		stored := 0
		registry.FeedBeforeInsert.Add(0, func(_ context.Context, f *store.Feed) (*store.Feed, bool) {
			if stored++; stored == 2 {
				cancel()
			}
			return f, true
		})
		if _, err := transfer.ImportRemote(ctx, w.db, registry, w.u, remote, options); err == nil {
			t.Fatal("import with a context gone half way did not fail")
		}
		if after := w.state(); !equal(after, before) {
			t.Errorf("a failed import changed the account:\n%+v\nwas %+v", after, before)
		}

		// A failure of the server while reading never reaches the account.
		s.FailEntriesFrom(2)
		if _, err := miniflux.Read(context.Background(), client(t, s), s.URL, minifluxtest.Token); err == nil {
			t.Fatal("read of a failing server did not fail")
		}
		if after := w.state(); !equal(after, before) {
			t.Errorf("a failed read changed the account: %+v", after)
		}
	})
}

func equal(a, b state) bool {
	return slices.Equal(a.Categories, b.Categories) && slices.Equal(a.Feeds, b.Feeds) && a.Entries == b.Entries && slices.Equal(a.Labels, b.Labels)
}

// R6: a feed the user has already keeps its settings and takes the entries;
// when its entries are told by something other than links, the user is told
// that a refresh may add them again.
func TestImportIntoSubscribedFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		mine := &store.Feed{UserID: w.u.ID, URL: "https://site2.example/feed.xml", Name: "My name", Priority: 20}
		if err := w.db.CreateFeed(ctx, mine); err != nil {
			t.Fatal(err)
		}
		s := minifluxtest.New(t)
		report, err := transfer.ImportRemote(ctx, w.db, &hooks.Registry{}, w.u, w.read(s), options)
		if err != nil {
			t.Fatal(err)
		}
		if report.Feeds != 2 {
			t.Errorf("%d feeds added, want the two the user did not have", report.Feeds)
		}
		kept, err := w.db.FeedByID(ctx, w.u.ID, mine.ID)
		if err != nil || kept.Name != "My name" || kept.Priority != 20 {
			t.Errorf("feed of the user = %+v, %v; want it as it was", kept, err)
		}
		entries, err := w.db.EntriesByFeed(ctx, w.u.ID, mine.ID)
		if err != nil || len(entries) == 0 {
			t.Errorf("%d entries in the feed of the user, %v", len(entries), err)
		}
		if !slices.ContainsFunc(report.Warnings, func(x string) bool { return strings.Contains(x, "feed My name: already subscribed") }) {
			t.Errorf("no warning about the identifiers in %q", report.Warnings)
		}
	})
}
