package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// The reference installation: alice has feeds 1 to 7 in the main stream,
// with 19 entries of which one is read, and feed 8, shown on its own page
// only, with three read entries; two entries are starred, three labelled.

var (
	listedEntry = regexp.MustCompile(`<article class="entry[^"]*" id="e(\d+)"`)
	nextPage    = regexp.MustCompile(`<a href="([^"]+)" rel="next">`)
	messages    = regexp.MustCompile(`(?s)<div class="messages"[^>]*>(.*?)</div>`)
)

// listed returns the identifiers of the entries a page lists, in order.
func listed(body string) []int64 {
	var ids []int64
	for _, m := range listedEntry.FindAllStringSubmatch(body, -1) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		ids = append(ids, id)
	}
	return ids
}

func (s *site) user(name string) *store.User {
	s.t.Helper()
	u, err := s.db.UserByName(context.Background(), name)
	if err != nil {
		s.t.Fatal(err)
	}
	return u
}

// stored returns the identifiers the store lists for a user.
func (s *site) stored(name string, l store.Listing) []int64 {
	s.t.Helper()
	entries, _, err := s.db.ListPage(context.Background(), s.user(name).ID, l)
	if err != nil {
		s.t.Fatal(err)
	}
	var ids []int64
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// page asks for a page that has to be there.
func (s *site) page(target string) string {
	s.t.Helper()
	a := s.get(target)
	if a.status != http.StatusOK {
		s.t.Fatalf("GET %s: status %d\n%s", target, a.status, a.body)
	}
	return a.body
}

// asAlice logs alice of the reference installation in.
func (s *site) asAlice() {
	s.t.Helper()
	if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusSeeOther {
		s.t.Fatalf("login: status %d", a.status)
	}
}

func ptr[T any](v T) *T { return &v }

func mainStream() store.EntrySet { return store.EntrySet{MinPriority: ptr(priorityMain)} }

// R4: the tree names every stream with its count of unread entries, and a
// stream lists its entries.
func TestReadingScreen(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		body := s.page("/")
		for _, want := range []string{
			`<a href="/" aria-current="page">Reading</a>`,
			`<a href="/" aria-current="page">Unread</a> <span class="count" title="18 unread entries">18</span>`,
			`<a href="/all">All entries</a> <span class="count" title="18 unread entries">18</span>`,
			`<a href="/starred">Starred</a> <span class="count" title="2 unread entries">2</span>`,
			`<a href="/categories/2">Blogs</a> <span class="count" title="8 unread entries">8</span>`,
			`<a href="/categories/3">Scraped &amp; parsed</a> <span class="count" title="10 unread entries">10</span>`,
			`<a href="/feeds/1">Atom corpus</a> <span class="count" title="4 unread entries">4</span>`,
			`<a href="/labels/2">work &amp; play</a> <span class="count" title="1 unread entry">1</span>`,
			`<h1 id="stream-heading">Unread</h1>`, `<input type="search" id="q" name="q" value="">`,
			`<a href="/?state=unread" aria-current="true">Unread</a>`, `<a href="/?state=all">All</a>`,
			`<option value="added" selected>Time received</option>`, `<option value="desc" selected>`,
			`action="/read-all"`, `>Mark as read</button>`, `>Star</button>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /: no %q in\n%s", want, body)
			}
		}
		// The feed with nothing unread is left out of the tree while unread
		// entries are listed, and is there otherwise.
		if strings.Contains(body, `href="/feeds/8"`) {
			t.Error("GET /: the tree shows a feed without unread entries")
		}
		if all := s.page("/?state=all"); !strings.Contains(all, `<a href="/feeds/8?state=all">No identifiers</a>`) {
			t.Errorf("GET /?state=all: the tree lacks the feed without unread entries\n%s", all)
		}

		// The feed being read stays in the tree with nothing unread in it.
		if own := s.page("/feeds/8?state=unread"); !strings.Contains(own, `<a href="/feeds/8?state=unread" aria-current="page">No identifiers</a>`) {
			t.Errorf("GET /feeds/8?state=unread: the tree lacks the feed being read\n%s", own)
		}

		unread := false
		for target, want := range map[string]store.Listing{
			"/":                           {Set: mainStream(), Read: &unread},
			"/?state=all":                 {Set: mainStream()},
			"/all":                        {Set: mainStream()},
			"/?state=starred":             {Set: mainStream(), Favorite: ptr(true)},
			"/?state=unread-or-starred":   {Set: mainStream(), UnreadOrFavorite: true},
			"/starred":                    {Set: store.EntrySet{OnlyFavorite: true}, Read: &unread},
			"/feeds/2":                    {Set: store.EntrySet{FeedID: 2}, Read: &unread},
			"/feeds/8":                    {Set: store.EntrySet{FeedID: 8}},
			"/categories/2?state=all":     {Set: store.EntrySet{CategoryID: 2, MinPriority: ptr(0)}},
			"/categories/1?state=all":     {Set: store.EntrySet{FeedID: 99}},
			"/labels/1":                   {Set: store.EntrySet{LabelID: 1}, Read: &unread},
			"/?sort=published&order=asc":  {Set: mainStream(), Read: &unread, Order: store.OrderPublished, Ascending: true},
			"/all?sort=title":             {Set: mainStream(), Order: store.OrderTitle},
			"/all?sort=feed&order=asc":    {Set: mainStream(), Order: store.OrderFeed, Ascending: true},
			"/all?sort=nonsense&state=no": {Set: mainStream()},
		} {
			got, wantIDs := listed(s.page(target)), s.stored("alice", want)
			if !reflect.DeepEqual(got, wantIDs) {
				t.Errorf("GET %s lists %v, want %v", target, got, wantIDs)
			}
		}
		if n := len(listed(s.page("/"))); n != 18 {
			t.Errorf("GET / lists %d entries, want the 18 unread of the main stream", n)
		}
		if n := len(listed(s.page("/all?sort=random"))); n != 19 {
			t.Errorf("GET /all?sort=random lists %d entries, want 19", n)
		}

		for _, target := range []string{"/feeds/99", "/categories/99", "/labels/99", "/feeds/x", "/entries/5", "/?after=nonsense"} {
			if a := s.get(target); a.status != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", target, a.status)
			}
		}
	})
}

// R4: a stream is read page by page, each entry once; the size of the page
// and how a stream is listed by default are the user's settings, and a feed
// or a category may have an order of its own.
func TestReadingSettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		s.setting("alice", "posts_per_page", 5)
		var got []int64
		pages := 0
		for target := "/all?sort=published"; target != ""; pages++ {
			body := s.page(target)
			got = append(got, listed(body)...)
			target = ""
			if m := nextPage.FindStringSubmatch(body); m != nil {
				target = strings.ReplaceAll(m[1], "&amp;", "&")
				if !strings.Contains(target, "sort=published") {
					t.Fatalf("the next page %q forgets the order", target)
				}
			}
		}
		if want := s.stored("alice", store.Listing{Set: mainStream(), Order: store.OrderPublished}); pages != 4 || !reflect.DeepEqual(got, want) {
			t.Errorf("19 entries five a page: %d pages with %v, want 4 pages with %v", pages, got, want)
		}
		s.setting("alice", "posts_per_page", 20)

		unread := false
		check := func(what, target string, want store.Listing) {
			t.Helper()
			if got, wantIDs := listed(s.page(target)), s.stored("alice", want); !reflect.DeepEqual(got, wantIDs) {
				t.Errorf("%s: GET %s lists %v, want %v", what, target, got, wantIDs)
			}
		}
		s.setting("alice", "default_view", "all")
		check("default_view all", "/", store.Listing{Set: mainStream()})
		s.setting("alice", "default_view", "unread_or_favorite")
		check("default_view unread_or_favorite", "/", store.Listing{Set: mainStream(), UnreadOrFavorite: true})
		s.setting("alice", "default_view", "unread")
		check("default_view unread, nothing unread", "/feeds/8", store.Listing{Set: store.EntrySet{FeedID: 99}})
		s.setting("alice", "default_view", "adaptive")
		check("adaptive, nothing unread", "/feeds/8", store.Listing{Set: store.EntrySet{FeedID: 8}})

		s.setting("alice", "show_fav_unread", true)
		check("show_fav_unread", "/starred", store.Listing{Set: store.EntrySet{OnlyFavorite: true}})
		check("show_fav_unread", "/labels/1", store.Listing{Set: store.EntrySet{LabelID: 1}})
		check("show_fav_unread and a state asked for", "/labels/1?state=unread", store.Listing{Set: store.EntrySet{LabelID: 1}, Read: &unread})

		s.setting("alice", "sort", "date")
		s.setting("alice", "sort_order", "ASC")
		check("sort and sort_order", "/", store.Listing{Set: mainStream(), Read: &unread, Order: store.OrderPublished, Ascending: true})
		check("an order asked for", "/?sort=added&order=desc", store.Listing{Set: mainStream(), Read: &unread})

		ctx := context.Background()
		alice := s.user("alice")
		feed, err := s.db.FeedByID(ctx, alice.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		feed.Attributes = json.RawMessage(`{"defaultSort":"title","defaultOrder":"DESC"}`)
		if err := s.db.UpdateFeed(ctx, feed); err != nil {
			t.Fatal(err)
		}
		check("the order of a feed", "/feeds/2", store.Listing{Set: store.EntrySet{FeedID: 2}, Read: &unread, Order: store.OrderTitle})
		check("the order of the user elsewhere", "/feeds/1", store.Listing{Set: store.EntrySet{FeedID: 1}, Read: &unread, Order: store.OrderPublished, Ascending: true})

		// An order of a feed that freshgo lacks is the time received, not the
		// order of the user.
		s.setting("alice", "sort", "title")
		if err := s.db.InsertEntries(ctx, alice.ID, []*store.Entry{{FeedID: 3, GUID: "z", Title: "zz came first"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.db.InsertEntries(ctx, alice.ID, []*store.Entry{{FeedID: 3, GUID: "a", Title: "Aa came second"}}); err != nil {
			t.Fatal(err)
		}
		if feed, err = s.db.FeedByID(ctx, alice.ID, 3); err != nil {
			t.Fatal(err)
		}
		feed.Attributes = json.RawMessage(`{"defaultSort":"link"}`)
		if err := s.db.UpdateFeed(ctx, feed); err != nil {
			t.Fatal(err)
		}
		check("an order of a feed freshgo lacks", "/feeds/3", store.Listing{Set: store.EntrySet{FeedID: 3}, Read: &unread, Ascending: true})
		check("the order of the user elsewhere", "/feeds/4", store.Listing{Set: store.EntrySet{FeedID: 4}, Read: &unread, Order: store.OrderTitle, Ascending: true})
		byTitle := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 3}, Read: &unread, Order: store.OrderTitle, Ascending: true})
		if reflect.DeepEqual(listed(s.page("/feeds/3")), byTitle) {
			t.Error("the feed with an order freshgo lacks is listed in the order of the user")
		}

		s.setting("alice", "hide_read_feeds", false)
		if body := s.page("/"); !strings.Contains(body, `href="/feeds/8"`) {
			t.Error("hide_read_feeds off: the tree lacks the feed without unread entries")
		}
	})
}

// R4: an entry shows its title, feed, authors, date in the time zone of the
// user, text, tags and labels; what a feed could not have sent safely is
// not shown.
func TestEntryIsShown(t *testing.T) {
	seen := 0
	registry := &hooks.Registry{}
	registry.EntryBeforeDisplay.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
		seen++
		return e, e.Title != "Hidden by a handler"
	})
	imported(t, Options{Hooks: registry}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice := s.user("alice")
		entries := []*store.Entry{
			{
				FeedID: 1, GUID: "shown", Title: `Tom & "Jerry" <b>`, Authors: []string{"Ann", "Bo"}, Link: "https://example.org/a?b=1&c=2",
				Content: `<p onclick="steal()">Text <a href="/relative">link</a></p><script>alert(1)</script><form action="/logout"><input name="x"></form>` +
					`<img src="https://example.org/inline.png">`,
				Published: time.Date(2026, 3, 1, 23, 30, 0, 0, time.UTC).Unix(), Tags: []string{"cats", "mice"},
				Attributes: json.RawMessage(`{"enclosures":[
					{"url":"https://example.org/s.mp3","type":"audio/mpeg","title":"Episode"},
					{"url":"https://example.org/pic.png"},
					{"url":"https://example.org/inline.png","type":"image/png"},
					{"url":"javascript:alert(1)","type":"image/png"},
					{"url":"https://example.org/doc.pdf","type":"application/pdf"}]}`),
			},
			{FeedID: 1, GUID: "hidden", Title: "Hidden by a handler"},
			{ID: time.Date(2026, 3, 5, 1, 0, 0, 0, time.UTC).UnixMicro(), FeedID: 1, GUID: "untitled", Link: "javascript:alert(1)"},
		}
		if err := s.db.InsertEntries(ctx, alice.ID, entries); err != nil {
			t.Fatal(err)
		}
		if err := s.db.TagEntry(ctx, alice.ID, 2, entries[0].ID); err != nil {
			t.Fatal(err)
		}
		s.setting("alice", "timezone", "Asia/Tokyo")
		s.asAlice()

		id := strconv.FormatInt(entries[0].ID, 10)
		for _, target := range []string{"/feeds/1", "/entries/" + id} {
			body := s.page(target)
			for _, want := range []string{
				`Tom &amp; &#34;Jerry&#34; &lt;b&gt;`, `<a href="/feeds/1">Atom corpus</a>`, `Ann, Bo`,
				`<time datetime="2026-03-02T08:30:00&#43;09:00">2026-03-02 08:30</time>`,
				`<p>Text <a href="https://example.org/relative"`, `cats, mice`, `work &amp; play`,
				`href="https://example.org/a?b=1&amp;c=2" rel="noopener noreferrer"`,
				`<audio controls preload="none" src="https://example.org/s.mp3"></audio>`, `>Episode</a>`,
				`<img src="https://example.org/pic.png" alt="example.org/pic.png" loading="lazy">`,
				`<a href="https://example.org/doc.pdf" rel="noopener noreferrer" type="application/pdf">`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("GET %s: no %q in\n%s", target, want, body)
				}
			}
			// The picture the text shows is not shown again as an enclosure.
			if n := strings.Count(body, "example.org/inline.png"); n != 1 {
				t.Errorf("GET %s shows the picture of the text %d times, want once", target, n)
			}
			for _, unwanted := range []string{"onclick", "<script>alert", "steal()", `action="/logout"><input`, "javascript:alert"} {
				if strings.Contains(body, unwanted) {
					t.Errorf("GET %s shows %q", target, unwanted)
				}
			}
		}

		// What a handler of EntryBeforeDisplay drops is neither listed nor shown.
		list := s.page("/feeds/1")
		if strings.Contains(list, "Hidden by a handler") || !strings.Contains(list, "Untitled entry") {
			t.Errorf("GET /feeds/1: the hidden entry is listed or the untitled one is not\n%s", list)
		}
		// An entry without a date shows the time it was received.
		if body := s.page("/entries/" + strconv.FormatInt(entries[2].ID, 10)); !strings.Contains(body, `<time datetime="2026-03-05T10:00:00&#43;09:00">2026-03-05 10:00</time>`) {
			t.Errorf("the entry without a date does not show when it was received\n%s", body)
		}
		if a := s.get("/entries/" + strconv.FormatInt(entries[1].ID, 10)); a.status != http.StatusNotFound {
			t.Errorf("the page of an entry a handler hides: status %d", a.status)
		}
		if seen == 0 {
			t.Error("EntryBeforeDisplay was not called")
		}

		// Looking at an entry changes nothing.
		if e, err := s.db.EntryByID(ctx, alice.ID, entries[0].ID); err != nil || e.IsRead || e.LastUserModified != 0 {
			t.Errorf("after GET the entry is read %v, changed at %d, %v", e.IsRead, e.LastUserModified, err)
		}

		// An entry of another user is not there.
		bob, _, err := s.db.ListPage(ctx, s.user("bob").ID, store.Listing{Limit: 1})
		if err != nil || len(bob) != 1 {
			t.Fatal(err)
		}
		if _, err := s.db.EntryByID(ctx, alice.ID, bob[0].ID); err == nil {
			t.Skip("the reference users share an entry identifier")
		}
		if a := s.get("/entries/" + strconv.FormatInt(bob[0].ID, 10)); a.status != http.StatusNotFound {
			t.Errorf("the page of an entry of another user: status %d", a.status)
		}
	})
}

// R6: the search line takes the search language; what it finds is what
// Match picks, inside the stream and the state shown; a search that cannot
// be read says so.
func TestSearchLine(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		s.setting("alice", "queries", []map[string]string{{"name": "guids", "search": "intitle:guid"}})
		opts := search.Options{Queries: []search.SavedQuery{{Name: "guids", Search: "intitle:guid"}}, Now: time.Now(), Labels: true}
		unread := false
		for _, tc := range []struct {
			query string
			path  string
			l     store.Listing
			some  bool
		}{
			{"intitle:guid", "/all", store.Listing{Set: mainStream()}, true},
			{"GUID -intitle:markup", "/", store.Listing{Set: mainStream(), Read: &unread}, true},
			{"L:1", "/all", store.Listing{Set: mainStream()}, true},
			{"-L:* f:2", "/all", store.Listing{Set: mainStream()}, true},
			{`labels:"work & play" OR c:3`, "/all", store.Listing{Set: mainStream()}, true},
			{"search:guids", "/feeds/2", store.Listing{Set: store.EntrySet{FeedID: 2}, Read: &unread}, true},
			{"S:0", "/categories/2", store.Listing{Set: store.EntrySet{CategoryID: 2, MinPriority: ptr(0)}, Read: &unread}, true},
			{"/^guid .* DOMAIN/i", "/all", store.Listing{Set: mainStream()}, true},
			{"nothing-has-this-word", "/all", store.Listing{Set: mainStream()}, false},
		} {
			q, err := search.Parse(tc.query, opts)
			if err != nil {
				t.Fatal(err)
			}
			tc.l.Search = q
			target := tc.path + "?q=" + url.QueryEscape(tc.query)
			body := s.page(target)
			got, want := listed(body), s.stored("alice", tc.l)
			if !reflect.DeepEqual(got, want) || tc.some == (len(got) == 0) {
				t.Errorf("GET %s lists %v, want %v", target, got, want)
			}
			if !strings.Contains(body, `name="q" value="`+strings.NewReplacer("&", "&amp;", `"`, "&#34;").Replace(tc.query)+`"`) {
				t.Errorf("GET %s: the search line does not keep the search", target)
			}
		}

		// The state and the order links keep the search; the tree drops it.
		body := s.page("/all?q=guid&sort=title")
		for _, want := range []string{`href="/all?q=guid&amp;sort=title&amp;state=unread"`, `<a href="/feeds/1?sort=title">`, `<input type="hidden" name="q" value="guid">`} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /all?q=guid&sort=title: no %q in\n%s", want, body)
			}
		}

		for query, problem := range map[string]string{
			`/(?=a)b/`:                    "regular expression that is not supported",
			strings.Repeat("(", 40) + "a": "too many nested parentheses",
			strings.Repeat("word ", 4000): "The search is too long.",
		} {
			a := s.get("/all?q=" + url.QueryEscape(query))
			if a.status != http.StatusBadRequest || !strings.Contains(a.body, `role="alert"`) || !strings.Contains(a.body, problem) || len(listed(a.body)) != 0 {
				t.Errorf("GET /all?q=%.20s…: status %d, want 400 and %q\n%.600s", query, a.status, problem, a.body)
			}
		}
	})
}

// R5: an entry is made read and unread, starred and unstarred by a form;
// the handlers of EntriesRead and EntriesFavorite hear of it, and the
// reader is back at the entry.
func TestEntryActions(t *testing.T) {
	var calls []string
	registry := &hooks.Registry{}
	registry.EntriesRead.Add(0, func(_ context.Context, e hooks.EntriesRead) bool {
		calls = append(calls, "read "+strconv.FormatBool(e.IsRead)+" "+strconv.Itoa(len(e.IDs)))
		return false
	})
	registry.EntriesFavorite.Add(0, func(_ context.Context, e hooks.EntriesFavorite) bool {
		calls = append(calls, "star "+strconv.FormatBool(e.IsFavorite)+" "+strconv.Itoa(len(e.IDs)))
		return false
	})
	imported(t, Options{Hooks: registry}, func(t *testing.T, s *site) {
		calls = nil
		ctx := context.Background()
		alice := s.user("alice")
		clock := s.clock()
		s.asAlice()
		id := listed(s.page("/"))[0]
		entry := func() *store.Entry {
			t.Helper()
			e, err := s.db.EntryByID(ctx, alice.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
		path := "/entries/" + strconv.FormatInt(id, 10)
		back := "/all#e" + strconv.FormatInt(id, 10)
		form := func(more ...string) url.Values {
			v := url.Values{"next": {"/all"}}
			for i := 0; i+1 < len(more); i += 2 {
				v.Set(more[i], more[i+1])
			}
			return v
		}

		a := s.post(path+"/read", form("read", "1"))
		if e := entry(); a.status != http.StatusSeeOther || a.header.Get("Location") != back || !e.IsRead || e.LastUserModified != clock.Unix() {
			t.Errorf("mark read: status %d, Location %q, read %v at %d", a.status, a.header.Get("Location"), e.IsRead, e.LastUserModified)
		}
		// Once more changes nothing and tells nobody.
		s.post(path+"/read", form("read", "1"))
		if body := s.page("/all"); !strings.Contains(body, `<article class="entry read" id="e`+strconv.FormatInt(id, 10)+`"`) {
			t.Error("the entry is not shown as read")
		}
		s.post(path+"/read", form("read", "0"))
		if entry().IsRead {
			t.Error("mark unread: the entry is still read")
		}
		s.post(path+"/star", form("starred", "1"))
		if !entry().IsFavorite {
			t.Error("star: the entry is not starred")
		}
		s.post(path+"/star", form("starred", "0"))
		if entry().IsFavorite {
			t.Error("unstar: the entry is still starred")
		}
		if want := []string{"read true 1", "read false 1", "star true 1", "star false 1"}; !reflect.DeepEqual(calls, want) {
			t.Errorf("handlers heard %v, want %v", calls, want)
		}

		// Nowhere but to a page of the interface.
		if a := s.post(path+"/read", url.Values{"next": {"https://evil.example/"}}); a.header.Get("Location") != "/#e"+strconv.FormatInt(id, 10) {
			t.Errorf("next on another site: Location %q", a.header.Get("Location"))
		}
		for _, target := range []string{"/entries/5/read", "/entries/x/star", "/entries/5/labels"} {
			if a := s.post(target, form()); a.status != http.StatusNotFound {
				t.Errorf("POST %s: status %d, want 404", target, a.status)
			}
		}
		if a := s.get(path + "/read"); a.status != http.StatusMethodNotAllowed {
			t.Errorf("GET %s/read: status %d, want 405", path, a.status)
		}
	})
}

// R5: the labels of an entry are set by a form that can also make a new one.
func TestEntryLabels(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice := s.user("alice")
		s.asAlice()
		id := listed(s.page("/feeds/3"))[0]
		path := "/entries/" + strconv.FormatInt(id, 10)
		labels := func() []string {
			t.Helper()
			names, err := s.db.EntryLabels(ctx, alice.ID, []int64{id})
			if err != nil {
				t.Fatal(err)
			}
			return names[id]
		}
		if body := s.page(path); !strings.Contains(body, `<input type="checkbox" id="label-1" name="label" value="1">`) ||
			!strings.Contains(body, `<label for="label-2">work &amp; play</label>`) {
			t.Errorf("GET %s: no form of labels\n%s", path, body)
		}

		a := s.post(path+"/labels", url.Values{"label": {"1", "2", "99", "x"}, "new": {"  to read  "}, "next": {path}})
		if a.status != http.StatusSeeOther || a.header.Get("Location") != path+"#e"+strconv.FormatInt(id, 10) {
			t.Fatalf("set labels: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if got, want := labels(), []string{"later", "work & play", "to read"}; !reflect.DeepEqual(got, want) {
			t.Errorf("labels = %v, want %v", got, want)
		}
		body := s.page(path)
		if m := messages.FindStringSubmatch(body); m == nil || !strings.Contains(m[1], "Labels saved.") {
			t.Errorf("no notice after saving labels\n%s", body)
		}
		if !strings.Contains(body, `name="label" value="2" checked>`) {
			t.Error("the form does not show the labels the entry has")
		}
		if m := messages.FindStringSubmatch(s.page(path)); m == nil || strings.TrimSpace(m[1]) != "" {
			t.Errorf("the notice is shown twice: %q", m)
		}

		// The name of an existing label is that label; a category's name is taken.
		s.post(path+"/labels", url.Values{"label": {"2"}, "new": {"later"}, "next": {path}})
		if got, want := labels(), []string{"later", "work & play"}; !reflect.DeepEqual(got, want) {
			t.Errorf("labels = %v, want %v", got, want)
		}
		s.post(path+"/labels", url.Values{"new": {"Blogs"}, "next": {path}})
		if got := labels(); got != nil {
			t.Errorf("labels = %v, want none", got)
		}
		if body := s.page(path); !strings.Contains(body, "A category has this name already") {
			t.Errorf("no notice about the taken name\n%s", body)
		}
		s.post(path+"/labels", url.Values{"new": {strings.Repeat("я", 192)}, "next": {path}})
		if body := s.page(path); !strings.Contains(body, "too long") {
			t.Errorf("no notice about the long name\n%s", body)
		}
		// 191 characters, whatever bytes they take, are not too long.
		longest := strings.Repeat("я", 191)
		s.post(path+"/labels", url.Values{"new": {longest}, "next": {path}})
		if got := labels(); !reflect.DeepEqual(got, []string{longest}) {
			t.Errorf("labels = %v, want the one of 191 characters", got)
		}
		tags, err := s.db.Tags(ctx, alice.ID)
		if err != nil || len(tags) != 4 {
			t.Errorf("%d labels, %v; want the two of the reference and two new", len(tags), err)
		}

		// A cookie names a notice and nothing else among the texts.
		s.cookies[noticeCookie] = &http.Cookie{Name: noticeCookie, Value: "error.404.text:0"}
		if m := messages.FindStringSubmatch(s.page(path)); m == nil || strings.TrimSpace(m[1]) != "" {
			t.Errorf("a cookie that names another text is shown: %q", m)
		}
	})
}

// R5: "mark as read" covers what the page listed when it was made, or what
// is older than a day or a week, and tells how many entries it was.
func TestMarkAllRead(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice := s.user("alice")
		clock := s.clock()
		// The entries of the reference were added in 2026-10; go well past them.
		*clock = time.Date(2027, 1, 10, 12, 0, 0, 0, time.UTC)
		// A login that lasts through the days the test lets pass.
		s.login("alice", "alice-web-password", url.Values{"remember": {"1"}})
		unread := func(set store.EntrySet) []int64 {
			t.Helper()
			return s.stored("alice", store.Listing{Set: set, Read: ptr(false)})
		}
		arrive := func(feed int64, guid, title string) int64 {
			t.Helper()
			e := &store.Entry{ID: clock.UnixMicro(), FeedID: feed, GUID: guid, Title: title}
			if err := s.db.InsertEntries(ctx, alice.ID, []*store.Entry{e}); err != nil {
				t.Fatal(err)
			}
			return e.ID
		}
		form := func(body string) url.Values {
			t.Helper()
			v := url.Values{}
			for _, m := range regexp.MustCompile(`<input type="hidden" name="(stream|state|q|before|next)" value="([^"]*)">`).FindAllStringSubmatch(body, -1) {
				v.Set(m[1], strings.ReplaceAll(m[2], "&amp;", "&"))
			}
			if v.Get("stream") == "" || v.Get("before") == "" {
				t.Fatalf("no form to mark all as read in\n%s", body)
			}
			return v
		}
		notice := func(target string) string {
			t.Helper()
			m := messages.FindStringSubmatch(s.page(target))
			if m == nil {
				return ""
			}
			return strings.TrimSpace(m[1])
		}

		// Older than a week: only what arrived long ago, the reference.
		recent := arrive(1, "recent", "Arrived three days ago")
		*clock = clock.Add(3 * 24 * time.Hour)
		today := arrive(1, "today", "Arrived less than a day ago")
		*clock = clock.Add(23 * time.Hour)
		v := form(s.page("/feeds/1"))
		v.Set("older", "week")
		if a := s.post("/read-all", v); a.status != http.StatusSeeOther || a.header.Get("Location") != "/feeds/1" {
			t.Fatalf("mark older than a week: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if got := unread(store.EntrySet{FeedID: 1}); !reflect.DeepEqual(got, []int64{today, recent}) {
			t.Errorf("unread of the feed after marking what is older than a week: %v, want %v", got, []int64{today, recent})
		}
		if got := notice("/feeds/1"); got != "<p>4 entries marked as read.</p>" {
			t.Errorf("notice %q", got)
		}
		if got := len(unread(store.EntrySet{FeedID: 2})); got != 4 {
			t.Errorf("%d unread entries in another feed, want 4", got)
		}
		v.Set("older", "day")
		s.post("/read-all", v)
		if got := unread(store.EntrySet{FeedID: 1}); !reflect.DeepEqual(got, []int64{today}) {
			t.Errorf("unread of the feed after marking what is older than a day: %v, want %v", got, []int64{today})
		}
		if got := notice("/feeds/1"); got != "<p>1 entry marked as read.</p>" {
			t.Errorf("notice %q", got)
		}

		// Everything listed: an entry that arrives after the page was made stays.
		v = form(s.page("/categories/2"))
		*clock = clock.Add(time.Minute)
		late := arrive(2, "late", "Arrived after the page was made")
		s.post("/read-all", v)
		if got := unread(store.EntrySet{CategoryID: 2}); !reflect.DeepEqual(got, []int64{late}) {
			t.Errorf("unread of the category: %v, want only the late entry %d", got, late)
		}

		// With a search, only what the search finds.
		*clock = clock.Add(time.Minute)
		before := unread(store.EntrySet{CategoryID: 3})
		body := s.page("/categories/3?q=" + url.QueryEscape("f:3 OR intitle:second"))
		found := listed(body)
		if len(found) == 0 || len(found) == len(before) {
			t.Fatalf("the search finds %d of %d entries: the case proves nothing", len(found), len(before))
		}
		s.post("/read-all", form(body))
		left := unread(store.EntrySet{CategoryID: 3})
		for _, id := range found {
			if slices.Contains(left, id) {
				t.Errorf("entry %d was found and is still unread", id)
			}
		}
		if len(left) != len(before)-len(found) {
			t.Errorf("%d unread entries left of %d with %d found", len(left), len(before), len(found))
		}

		// With a search too, what arrives after the page was made stays.
		body = s.page("/feeds/5?q=" + url.QueryEscape("intitle:arrived OR f:5"))
		*clock = clock.Add(time.Minute)
		late = arrive(5, "late-found", "Arrived after the search")
		s.post("/read-all", form(body))
		if got := unread(store.EntrySet{FeedID: 5}); !reflect.DeepEqual(got, []int64{late}) {
			t.Errorf("unread of the feed after marking what a search found: %v, want only the late entry %d", got, late)
		}
		// "before" is never later than now, with a search and without.
		*clock = clock.Add(time.Hour)
		ahead := arrive(5, "ahead", "Arrived by a clock that is ahead")
		*clock = clock.Add(-30 * time.Minute)
		for _, q := range []string{"", "intitle:arrived"} {
			s.post("/read-all", url.Values{"stream": {"/feeds/5"}, "q": {q}, "before": {strconv.FormatInt(ahead+1, 10)}})
			if got := unread(store.EntrySet{FeedID: 5}); !reflect.DeepEqual(got, []int64{ahead}) {
				t.Errorf("unread of the feed after marking up to a time yet to come, search %q: %v, want only %d", q, got, ahead)
			}
			if _, err := s.db.SetEntriesRead(ctx, alice.ID, []int64{late}, false, 1); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.SetEntriesRead(ctx, alice.ID, []int64{late, ahead}, true, 1); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(time.Hour)

		// Starred only: the rest of the main stream stays unread.
		starred := arrive(4, "starred", "Starred")
		if err := s.db.SetEntriesFavorite(ctx, alice.ID, []int64{starred}, true, 1); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(time.Minute)
		mainUnread := len(unread(mainStream()))
		s.post("/read-all", form(s.page("/?state=starred")))
		if got := len(unread(mainStream())); got != mainUnread-1 {
			t.Errorf("%d unread entries in the main stream, want %d: only the starred one read", got, mainUnread-1)
		}

		if a := s.post("/read-all", url.Values{"stream": {"/feeds/99"}}); a.status != http.StatusNotFound {
			t.Errorf("mark all of an unknown feed: status %d", a.status)
		}
		// Another user is untouched.
		if got := s.stored("bob", store.Listing{Read: ptr(false)}); len(got) == 0 {
			t.Error("the other user has no unread entries left")
		}
	})
}

// R3, R5: a visitor reads and can change nothing.
func TestVisitorOnlyReads(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.system(func(system *store.System) { system.AllowAnonymous = true })
		body := s.page("/")
		if n := len(listed(body)); n != 18 {
			t.Errorf("a visitor sees %d entries, want the 18 of the default user", n)
		}
		for _, unwanted := range []string{`action="/read-all"`, "/star\"", "/read\"", ">Mark as read<"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("the page of a visitor has %q", unwanted)
			}
		}
		id := strconv.FormatInt(listed(body)[0], 10)
		if page := s.page("/entries/" + id); strings.Contains(page, "<form") && strings.Contains(page, "/labels") {
			t.Error("the entry page of a visitor has the form of labels")
		}
		for _, target := range []string{"/entries/" + id + "/read", "/entries/" + id + "/star", "/entries/" + id + "/labels", "/read-all"} {
			if a := s.post(target, url.Values{"stream": {"/"}}); a.status != http.StatusForbidden {
				t.Errorf("POST %s by a visitor: status %d, want 403", target, a.status)
			}
		}
		if got := len(s.stored("alice", store.Listing{Read: ptr(false)})); got != 18 {
			t.Errorf("%d unread entries after the visitor's requests, want 18", got)
		}
	})
}

// R4: the counts of the tree go by where a feed is shown: the main stream
// counts the feeds shown there, a category those not kept to their own page.
func TestTreeCountsByPriority(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice := s.user("alice")
		s.asAlice()
		counts := func(feed *store.Feed) [3]string {
			t.Helper()
			body := s.page("/?state=all")
			var got [3]string
			for i, link := range []string{`<a href="/?state=all" aria-current="page">Unread</a>`, `<a href="/categories/` + strconv.FormatInt(feed.CategoryID, 10) + `?state=all">`, `<a href="/feeds/` + strconv.FormatInt(feed.ID, 10) + `?state=all">`} {
				m := regexp.MustCompile(regexp.QuoteMeta(link) + `(?:[^<]*</a>)? <span class="count" title="(\d+) `).FindStringSubmatch(body)
				if m != nil {
					got[i] = m[1]
				}
			}
			return got
		}
		// Feed 8 is shown on its own page only: its unread entry counts there.
		own, err := s.db.FeedByID(ctx, alice.ID, 8)
		if err != nil {
			t.Fatal(err)
		}
		before := counts(own)
		if err := s.db.InsertEntries(ctx, alice.ID, []*store.Entry{{FeedID: 8, GUID: "unread"}}); err != nil {
			t.Fatal(err)
		}
		if got, want := counts(own), [3]string{before[0], before[1], "1"}; got != want || before[0] != "18" {
			t.Errorf("counts of the main stream, the category and a feed kept to its page: %v, want %v", got, want)
		}
		// Feed 2 moves out of the main stream into its category only.
		feed, err := s.db.FeedByID(ctx, alice.ID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := counts(feed), [3]string{"18", "8", "4"}; got != want {
			t.Fatalf("counts of the main stream, the category and the feed: %v, want %v", got, want)
		}
		feed.Priority = priorityCategory
		if err := s.db.UpdateFeed(ctx, feed); err != nil {
			t.Fatal(err)
		}
		if got, want := counts(feed), [3]string{"14", "8", "4"}; got != want {
			t.Errorf("counts with the feed in its category only: %v, want %v", got, want)
		}
	})
}

// R4, R5: a shuffled stream stays as it was shuffled through its pages and
// when an action brings the reader back to it.
func TestShuffleIsKept(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		s.setting("alice", "posts_per_page", 5)
		next := regexp.MustCompile(`<input type="hidden" name="next" value="([^"]+)">`)
		for range 5 {
			body := s.page("/all?sort=random")
			first := listed(body)
			m := next.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("no form of an action in\n%s", body)
			}
			here := strings.ReplaceAll(m[1], "&amp;", "&")
			a := s.post("/entries/"+strconv.FormatInt(first[2], 10)+"/star", url.Values{"next": {here}, "starred": {"1"}})
			back, _, _ := strings.Cut(a.header.Get("Location"), "#")
			if a.status != http.StatusSeeOther || !strings.Contains(back, "seed=") {
				t.Fatalf("an action on a shuffled page: status %d, Location %q", a.status, a.header.Get("Location"))
			}
			if again := listed(s.page(back)); !reflect.DeepEqual(again, first) {
				t.Fatalf("back on the shuffled page it lists %v, it listed %v", again, first)
			}

			seen := map[int64]bool{}
			for target := back; target != ""; {
				body := s.page(target)
				for _, id := range listed(body) {
					if seen[id] {
						t.Fatalf("entry %d is on two pages of one shuffle", id)
					}
					seen[id] = true
				}
				target = ""
				if m := nextPage.FindStringSubmatch(body); m != nil {
					target = strings.ReplaceAll(m[1], "&amp;", "&")
				}
			}
			if len(seen) != 19 {
				t.Fatalf("the pages of a shuffle list %d entries, want 19", len(seen))
			}
		}
		// A shuffle is an order as any other: another seed, another order.
		if reflect.DeepEqual(listed(s.page("/all?sort=random&seed=123456789")), listed(s.page("/all?sort=random&seed=987654321"))) {
			t.Error("two seeds shuffle the same way")
		}
	})
}

// R2: text no database keeps, with a NUL or bytes that are not UTF-8, is
// refused the same on both engines and never breaks the server.
func TestUnreadableText(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		s.asAlice()
		id := strconv.FormatInt(listed(s.page("/"))[0], 10)
		for _, bad := range []string{"a\x00b", "a\xffb"} {
			for _, target := range []string{
				"/all?q=" + url.QueryEscape(bad), "/all?q=" + url.QueryEscape(`labels:"`+bad+`"`),
				"/feeds/1?state=" + url.QueryEscape(bad), "/entries/" + id + "?x=" + url.QueryEscape(bad),
			} {
				if a := s.get(target); a.status != http.StatusBadRequest {
					t.Errorf("GET %q: status %d, want 400", target, a.status)
				}
			}
			for target, form := range map[string]url.Values{
				"/entries/" + id + "/labels": {"new": {bad}},
				"/entries/" + id + "/read":   {"next": {"/?q=" + bad}},
				"/read-all":                  {"stream": {"/"}, "q": {`labels:"` + bad + `"`}},
			} {
				if a := s.post(target, form); a.status != http.StatusBadRequest {
					t.Errorf("POST %s with %q: status %d, want 400", target, bad, a.status)
				}
			}
			if a := s.login(bad, "alice-web-password", nil); a.status >= http.StatusInternalServerError {
				t.Errorf("login as %q: status %d", bad, a.status)
			}
		}
		if tags, err := s.db.Tags(ctx, s.user("alice").ID); err != nil || len(tags) != 2 {
			t.Errorf("%d labels, %v; want the two of the reference", len(tags), err)
		}
		// A place in a listing with a title no entry has is no place.
		after := base64.RawURLEncoding.EncodeToString([]byte(`{"i":1,"t":"a\u0000b"}`))
		if a := s.get("/all?sort=title&after=" + after); a.status != http.StatusNotFound {
			t.Errorf("GET /all?sort=title&after=<a title with a NUL>: status %d, want 404", a.status)
		}
	})
}

// R5: forms that name the same new label at once all get it.
func TestEntryLabelsAtOnce(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice := s.user("alice")
		s.asAlice()
		ids := listed(s.page("/all"))[:8]
		var wg sync.WaitGroup
		answers := make([]*httptest.ResponseRecorder, len(ids))
		for i, id := range ids {
			r := httptest.NewRequest(http.MethodPost, "/entries/"+strconv.FormatInt(id, 10)+"/labels",
				strings.NewReader(url.Values{"new": {"at once"}, "next": {"/"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			for _, c := range s.cookies {
				r.AddCookie(c)
			}
			answers[i] = httptest.NewRecorder()
			wg.Go(func() { s.h.ServeHTTP(answers[i], r) })
		}
		wg.Wait()
		for i, w := range answers {
			if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Set-Cookie"), "notice.labels:") {
				t.Errorf("form %d: status %d, cookie %q", i, w.Code, w.Header().Get("Set-Cookie"))
			}
		}
		labels, err := s.db.EntryLabels(ctx, alice.ID, ids)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if !slices.Contains(labels[id], "at once") {
				t.Errorf("entry %d has the labels %v, want the new one among them", id, labels[id])
			}
		}
		if tags, err := s.db.Tags(ctx, alice.ID); err != nil || len(tags) != 3 {
			t.Errorf("%d labels, %v; want the two of the reference and one new", len(tags), err)
		}
	})
}
