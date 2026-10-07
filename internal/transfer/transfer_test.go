package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/opml"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// world is a database with what a test puts into it.
type world struct {
	t  *testing.T
	db *store.Store
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
			test(t, &world{t, db})
		})
	}
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) user(name, settings string) *store.User {
	w.t.Helper()
	u := &store.User{Name: name, Settings: json.RawMessage(settings)}
	w.must(w.db.CreateUser(context.Background(), u))
	return u
}

func (w *world) feeds(u *store.User) map[string]*store.Feed {
	w.t.Helper()
	feeds, err := w.db.Feeds(context.Background(), u.ID)
	w.must(err)
	byURL := map[string]*store.Feed{}
	for _, f := range feeds {
		byURL[f.URL] = f
	}
	return byURL
}

// shown is what a reader sees of an entry, whatever identifier it has.
type shown struct {
	Feed, GUID, Title, Content, Link string
	Authors, Tags, Labels            []string
	Published                        int64
	Read, Starred                    bool
}

func (w *world) entries(u *store.User) []shown {
	w.t.Helper()
	ctx := context.Background()
	entries, _, err := w.db.ListPage(ctx, u.ID, store.Listing{Ascending: true})
	w.must(err)
	feeds, err := w.db.Feeds(ctx, u.ID)
	w.must(err)
	address := map[int64]string{}
	for _, f := range feeds {
		address[f.ID] = f.URL
	}
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	labels, err := w.db.EntryLabels(ctx, u.ID, ids)
	w.must(err)
	var out []shown
	for _, e := range entries {
		out = append(out, shown{address[e.FeedID], e.GUID, e.Title, e.Content, e.Link, e.Authors, e.Tags, labels[e.ID], e.Published, e.IsRead, e.IsFavorite})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Feed+out[a].GUID < out[b].Feed+out[b].GUID })
	return out
}

func (w *world) write(u *store.User, d Document) []byte {
	w.t.Helper()
	var b bytes.Buffer
	w.must(Write(context.Background(), &b, w.db, &hooks.Registry{}, u, d))
	return b.Bytes()
}

// library gives a user two feeds with entries in every state a document
// has to carry.
func (w *world) library(u *store.User) (blog, news *store.Feed) {
	w.t.Helper()
	ctx := context.Background()
	blog = &store.Feed{UserID: u.ID, URL: "https://reader:secret@blog.example/feed.xml", Name: "Blog & more", Website: "https://blog.example/", Priority: 20}
	news = &store.Feed{UserID: u.ID, URL: "https://news.example/rss", Name: "News", Priority: -10}
	w.must(w.db.CreateFeed(ctx, blog))
	w.must(w.db.CreateFeed(ctx, news))
	entries := []*store.Entry{
		{FeedID: blog.ID, GUID: "a", Title: `Q&A: "1 < 2"`, Authors: []string{"Ann", "Bob & Co"}, Content: `<p>Text &amp; <b>more</b></p>`,
			Link: "https://blog.example/a?x=1&y=2", Published: 1791000000, IsFavorite: true, Tags: []string{"go", "c & d"},
			Attributes: json.RawMessage(`{"enclosures":[{"url":"https://blog.example/a.mp3","type":"audio/mpeg","length":"123"},{"url":"https://blog.example/a.png","medium":"image"}]}`)},
		{FeedID: blog.ID, GUID: "b", Title: "", Content: "<p>Untitled</p>", Link: "https://blog.example/b", Published: 1791000100, IsRead: true},
		{FeedID: news.ID, GUID: "n1", Title: "Plain", Content: "<p>News</p>", Link: "https://news.example/1", Published: 1791000200},
		{FeedID: news.ID, GUID: "n2", Title: "Labelled", Content: "<p>News 2</p>", Link: "https://news.example/2", Published: 1791000300, IsRead: true},
	}
	w.must(w.db.InsertEntries(ctx, u.ID, entries))
	for name, ids := range map[string][]int64{"later": {entries[0].ID, entries[3].ID}, "work/play": {entries[3].ID}} {
		l := &store.Tag{UserID: u.ID, Name: name}
		w.must(w.db.CreateTag(ctx, l))
		w.must(w.db.TagEntries(ctx, u.ID, l.ID, ids))
	}
	return blog, news
}

var options = Options{Now: now}

// R10: what is exported comes back the same in an empty account.
func TestRoundTrip(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		alice, bob := w.user("alice", ""), w.user("bob", "")
		blog, news := w.library(alice)

		var archive bytes.Buffer
		z := zip.NewWriter(&archive)
		document, err := opml.Export(ctx, w.db, alice, now)
		w.must(err)
		for name, content := range map[string][]byte{
			"feeds_2026-10-07.opml.xml":   document,
			"starred_2026-10-07.json":     w.write(alice, Document{Kind: "starred", Title: "Starred", Set: store.EntrySet{FavoriteOrLabeled: true}}),
			"feed_2026-10-07_1_1.json":    w.write(alice, Document{Kind: "feed/1", Title: "Blog", Set: store.EntrySet{FeedID: blog.ID}}),
			"feed_2026-10-07_1_2.json":    w.write(alice, Document{Kind: "feed/2", Title: "News", Set: store.EntrySet{FeedID: news.ID}}),
			"notes.md":                    []byte("not looked at"),
			"nested/starred_copy.json.gz": []byte("not looked at either"),
		} {
			member, err := z.Create(name)
			w.must(err)
			_, err = member.Write(content)
			w.must(err)
		}
		w.must(z.Close())

		report, err := Import(ctx, w.db, &hooks.Registry{}, bob, "freshgo_alice_2026-10-07_export.zip", archive.Bytes(), options)
		// Two entries are in the list of starred ones and in that of their feed.
		if err != nil || report.Feeds != 2 || report.Entries != 4 || report.Updated != 2 || report.Incomplete {
			t.Fatalf("Import = %+v, %v; want two feeds and four entries", report, err)
		}
		want := w.entries(alice)
		if got := w.entries(bob); !reflect.DeepEqual(got, want) {
			t.Errorf("entries after the round trip:\n got %+v\nwant %+v", got, want)
		}
		// The subscriptions came with the OPML: names and priorities are those of alice.
		feeds := w.feeds(bob)
		if f := feeds["https://reader:secret@blog.example/feed.xml"]; f == nil || f.Name != "Blog & more" || f.Priority != 20 || f.Website != "https://blog.example/" {
			t.Errorf("feed of the round trip = %+v", f)
		}

		// A second import adds nothing and keeps what the reader did since.
		entries, _, err := w.db.ListPage(ctx, bob.ID, store.Listing{})
		w.must(err)
		for _, e := range entries {
			if e.GUID == "n1" {
				_, err := w.db.SetEntriesRead(ctx, bob.ID, []int64{e.ID}, true, now.Unix())
				w.must(err)
				w.must(w.db.SetEntriesFavorite(ctx, bob.ID, []int64{e.ID}, true, now.Unix()))
			}
		}
		report, err = Import(ctx, w.db, &hooks.Registry{}, bob, "starred.json", w.write(alice, Document{Kind: "starred", Set: store.EntrySet{OnlyFavorite: true}}), options)
		if err != nil || report.Feeds != 0 || report.Entries != 0 || report.Updated != 1 {
			t.Errorf("second import = %+v, %v; want one entry rewritten", report, err)
		}
		// The document of the feed says the entry is unread and says nothing
		// of a star: the entry becomes unread and keeps its star.
		if _, err := Import(ctx, w.db, &hooks.Registry{}, bob, "feed.json", w.write(alice, Document{Kind: "feed/2", Set: store.EntrySet{FeedID: news.ID}}), options); err != nil {
			t.Fatal(err)
		}
		for _, e := range w.entries(bob) {
			if e.GUID == "n1" && (e.Read || !e.Starred) {
				t.Errorf("entry after the document of its feed came again: %+v", e)
			}
		}
	})
}

// The document is what FreshRSS writes for its own export.
func TestDocumentFormat(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		alice := w.user("alice", "")
		blog, _ := w.library(alice)
		var document struct {
			ID, Title, Author string
			Items             []map[string]any
		}
		raw := w.write(alice, Document{Kind: "feed/1", Title: "List", Set: store.EntrySet{FeedID: blog.ID}})
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("the document is not JSON: %v\n%s", err, raw)
		}
		if document.ID != "user/alice/state/org.freshrss/feed/1" || document.Title != "List" || document.Author != "alice" || len(document.Items) != 2 {
			t.Fatalf("document = %+v", document)
		}
		first := document.Items[0]
		id := int64(first["frss:id"].(float64))
		want := map[string]any{
			"frss:id": float64(id), "id": "tag:google.com,2005:reader/item/" + strings.Repeat("0", 16-len(hex(id))) + hex(id),
			"crawlTimeMsec": itoa(id / 1000), "timestampUsec": itoa(id), "published": 1791000000.0,
			"title":     "Q&amp;A: &quot;1 &lt; 2&quot;",
			"canonical": []any{map[string]any{"href": "https://blog.example/a?x=1&y=2"}},
			"alternate": []any{map[string]any{"href": "https://blog.example/a?x=1&y=2", "type": "text/html"}},
			"categories": []any{
				"user/-/state/com.google/reading-list", "user/-/state/org.freshrss/main", "user/-/state/org.freshrss/important",
				"user/-/state/com.google/unread", "user/-/state/com.google/starred", "user/-/label/later", "go", "c & d",
			},
			"origin": map[string]any{
				"streamId": "feed/1", "htmlUrl": "https://blog.example/", "title": "Blog & more", "feedUrl": "https://blog.example/feed.xml",
			},
			"content": map[string]any{"content": `<p>Text &amp; <b>more</b></p>`}, "guid": "a", "author": "Ann; Bob &amp; Co",
			"enclosure": []any{
				map[string]any{"href": "https://blog.example/a.mp3", "type": "audio/mpeg", "length": 123.0},
				map[string]any{"href": "https://blog.example/a.png", "type": "image"},
			},
		}
		if !reflect.DeepEqual(first, want) {
			t.Errorf("first item:\n got %v\nwant %v", first, want)
		}
		// Characters of markup stand as they are, not as escapes of JSON.
		if !bytes.Contains(raw, []byte(`<p>Text &amp; <b>more</b></p>`)) {
			t.Errorf("the text of an entry is escaped in the document:\n%s", raw)
		}
		// A handler may keep an entry out of a document.
		registry := &hooks.Registry{}
		registry.EntryBeforeDisplay.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) { return e, e.GUID != "a" })
		var b bytes.Buffer
		w.must(Write(context.Background(), &b, w.db, registry, alice, Document{Kind: "feed/1", Set: store.EntrySet{FeedID: blog.ID}}))
		if err := json.Unmarshal(b.Bytes(), &document); err != nil || len(document.Items) != 1 || document.Items[0]["guid"] != "b" {
			t.Errorf("document with an entry kept out: %v\n%s", err, b.Bytes())
		}
		// An empty document is a document.
		if err := json.Unmarshal(w.write(alice, Document{Kind: "feed/9", Set: store.EntrySet{FeedID: 9}}), &document); err != nil || len(document.Items) != 0 {
			t.Errorf("empty document: %v, %d items", err, len(document.Items))
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func hex(n int64) string {
	const digits = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var out []byte
	for ; n > 0; n /= 16 {
		out = append([]byte{digits[n%16]}, out...)
	}
	return string(out)
}

// Documents of other readers pass for the same format.
func TestForeignDocuments(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{"mark_when":{"reception":true},"timezone":"UTC"}`)
		has := &store.Feed{UserID: u.ID, URL: "https://known.example/feed", Name: "Known"}
		w.must(w.db.CreateFeed(ctx, has))
		w.must(w.db.CreateTag(ctx, &store.Tag{UserID: u.ID, Name: "Taken"}))
		document := `{"items":[
			{"id":"g1","title":"Reader","origin":{"streamId":"feed/http://old.example/rss","title":" Old ","htmlUrl":"http://old.example/","category":"Imported"},
			 "alternate":[{"href":"http://old.example/1"}],"summary":{"content":"<p onclick=\"x()\">Sum</p><script>x</script>"},
			 "published":1700000000,"categories":["user/123/state/com.google/read","user/123/label/ Keep ","user/123/label/Imported","topic",7],"author":"A; B"},
			{"id":"g2","origin":{"htmlUrl":"http://site.example/","category":"Taken"},"url":"http://site.example/2","content":"plain <i>text</i>",
			 "updated":"2026-01-02T03:04:05Z","categories":["user/-/state/com.google/unread"]},
			{"guid":"g3","title":"Milliseconds","origin":{"feedUrl":"https://known.example/feed"},"timestampUsec":"1700000000123456","categories":"none"},
			{"guid":"g4","title":"Nowhere","published":"1700000000999"},
			{"guid":"g3","title":"Again","origin":{"feedUrl":"https://known.example/feed"}},
			{"guid":"g5","origin":{"feedUrl":"ftp://x"},"title":"Bad feed"},
			{"title":"No identifier"}, "not an item", {"guid":7}
		]}`
		report, err := Import(ctx, w.db, &hooks.Registry{}, u, "export.json", []byte(document), options)
		if err != nil || report.Feeds != 4 || report.Entries != 5 {
			t.Fatalf("Import = %+v, %v; want four feeds and five entries", report, err)
		}
		feeds := w.feeds(u)
		old, placeholder := feeds["http://old.example/rss"], feeds[placeholderFeed]
		if old == nil || old.Name != "Old" || old.Website != "http://old.example/" || old.CategoryID == store.DefaultCategoryID ||
			placeholder == nil || placeholder.TTL >= 0 || placeholder.Name != "Import" || feeds["http://site.example/"].CategoryID != store.DefaultCategoryID {
			t.Errorf("feeds of the import: old %+v, placeholder %+v, site %+v", old, placeholder, feeds["http://site.example/"])
		}
		got := map[string]shown{}
		for _, e := range w.entries(u) {
			got[e.GUID] = e
		}
		want := map[string]shown{
			"g1": {"http://old.example/rss", "g1", "Reader", "<p>Sum</p>", "http://old.example/1", []string{"A", "B"}, []string{"topic"}, []string{"Keep"}, 1700000000, true, false},
			"g2": {"http://site.example/", "g2", "http://site.example/2", "plain <i>text</i>", "http://site.example/2", nil, nil, nil, 1767323045, false, false},
			"g3": {"https://known.example/feed", "g3", "Milliseconds", "", "", nil, nil, nil, 1700000000, true, false},
			"g4": {placeholderFeed, "g4", "Nowhere", "", "", nil, nil, nil, 1700000000, true, false},
			"g5": {"https://ftp://x", "g5", "Bad feed", "", "", nil, nil, nil, 0, true, false},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entries of the import:\n got %+v\nwant %+v", got, want)
		}

		// A list of starred entries in the old form stars what has no label.
		starred := `[{"id":"s1","origin":{"feedUrl":"https://known.example/feed"},"categories":[]},
			{"id":"s2","origin":{"feedUrl":"https://known.example/feed"},"categories":["user/-/label/Keep"]}]`
		if _, err := Import(ctx, w.db, &hooks.Registry{}, u, "starred.json", []byte(starred), options); err != nil {
			t.Fatal(err)
		}
		for _, e := range w.entries(u) {
			if e.GUID == "s1" && !e.Starred || e.GUID == "s2" && (e.Starred || !reflect.DeepEqual(e.Labels, []string{"Keep"})) {
				t.Errorf("entry of a list of starred ones = %+v", e)
			}
		}
	})
}

// What cannot be taken in is said, and the rest still comes.
func TestImportRefusals(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		registry := &hooks.Registry{}
		for name, want := range map[string]error{"notes.md": ErrUnknownFile, "entries.json": ErrDocument, "feeds.opml": opml.ErrDocument, "all.zip": ErrDocument} {
			if _, err := Import(ctx, w.db, registry, u, name, []byte("<html>"), options); !errors.Is(err, want) {
				t.Errorf("Import of %s that holds something else: error = %v, want %v", name, err, want)
			}
		}
		// A list of addresses is a list of feeds.
		report, err := Import(ctx, w.db, registry, u, "feeds.txt", []byte("https://a.example/feed\r\n\n b.example/rss \n"), options)
		if feeds := w.feeds(u); err != nil || report.Feeds != 2 || feeds["https://a.example/feed"] == nil || feeds["https://b.example/rss"] == nil {
			t.Errorf("Import of a list of addresses = %+v, %v; feeds %v", report, err, feeds)
		}
		// The bounds of the installation hold for feeds and categories.
		limited := Options{Now: now, MaxFeeds: 3, MaxCategories: 2}
		document := `[{"id":"1","origin":{"feedUrl":"https://c.example/","category":"One"}},{"id":"2","origin":{"feedUrl":"https://d.example/","category":"Two"}}]`
		report, err = Import(ctx, w.db, registry, u, "x.json", []byte(document), limited)
		if err != nil || report.Feeds != 1 || report.Entries != 1 || !report.Incomplete {
			t.Errorf("Import past the limit of feeds = %+v, %v", report, err)
		}
		document = `<opml version="2.0"><body><outline text="Three"><outline type="rss" xmlUrl="https://e.example/"/><outline type="rss" xmlUrl="https://f.example/"/></outline></body></opml>`
		limited.MaxFeeds = 4
		report, err = Import(ctx, w.db, registry, u, "more.opml", []byte(document), limited)
		categories, _ := w.db.Categories(ctx, u.ID)
		if feeds := w.feeds(u); err != nil || report.Feeds != 1 || !report.Incomplete || len(categories) != 2 || feeds["https://e.example/"].CategoryID != store.DefaultCategoryID {
			t.Errorf("OPML past the limits = %+v, %v; %d categories", report, err, len(categories))
		}
		// A NUL, which JSON can spell and a database cannot keep, is left
		// out, and an identifier is no longer than a refresh would store.
		long := strings.Repeat("я", 500)
		document = `[{"id":"nul\u0000l","title":"a\u0000b \\u0000","origin":{"feedUrl":"https://a.example/feed","title":"x\u0000"},"categories":["user/-/label/l\u0000x","t\u0000ag"]},
			{"id":"` + long + `","origin":{"feedUrl":"https://a.example/feed"}}]`
		if report, err = Import(ctx, w.db, registry, u, "nul.json", []byte(document), options); err != nil || report.Entries != 2 {
			t.Errorf("Import of a document with NUL characters = %+v, %v", report, err)
		}
		for _, e := range w.entries(u) {
			switch {
			case e.GUID == "null":
				if e.Title != `ab \u0000` || !reflect.DeepEqual(e.Labels, []string{"lx"}) || !reflect.DeepEqual(e.Tags, []string{"tag"}) {
					t.Errorf("entry of a document with NUL characters = %+v", e)
				}
			case strings.HasPrefix(e.GUID, "я"):
				if len(e.GUID) > 767 || len(e.GUID) < 766 {
					t.Errorf("a long identifier is stored with %d bytes", len(e.GUID))
				}
			}
		}
		// One unreadable file of an archive does not keep the others out.
		var archive bytes.Buffer
		z := zip.NewWriter(&archive)
		for name, content := range map[string]string{"broken.json": "{", "good.json": `[{"id":"z","origin":{"feedUrl":"https://a.example/feed"}}]`} {
			member, _ := z.Create(name)
			_, _ = member.Write([]byte(content))
		}
		w.must(z.Close())
		report, err = Import(ctx, w.db, registry, u, "two.zip", archive.Bytes(), options)
		if err != nil || report.Entries != 1 || !report.Incomplete {
			t.Errorf("Import of an archive with a broken file = %+v, %v", report, err)
		}
		// An archive is not unpacked past what an import may hold, however
		// often it lists a file.
		archive.Reset()
		z = zip.NewWriter(&archive)
		for i := range maxMembers + 5 {
			member, _ := z.Create("f" + strconv.Itoa(i) + ".json")
			_, _ = member.Write([]byte(`[{"id":"m` + strconv.Itoa(i) + `","origin":{"feedUrl":"https://a.example/feed"}}]`))
		}
		w.must(z.Close())
		report, err = Import(ctx, w.db, registry, u, "many.zip", archive.Bytes(), options)
		if err != nil || report.Entries != maxMembers || !report.Incomplete {
			t.Errorf("Import of an archive of %d files = %+v, %v; want %d entries", maxMembers+5, report, err, maxMembers)
		}
		// A handler may refuse an entry or a feed.
		registry.EntryBeforeAdd.Add(0, func(_ context.Context, e *store.Entry) (*store.Entry, bool) { return e, e.GUID != "no" })
		registry.FeedBeforeInsert.Add(0, func(_ context.Context, f *store.Feed) (*store.Feed, bool) { return f, false })
		document = `[{"id":"no","origin":{"feedUrl":"https://a.example/feed"}},{"id":"yes","origin":{"feedUrl":"https://a.example/feed"}},{"id":"lost","origin":{"feedUrl":"https://g.example/"}}]`
		report, err = Import(ctx, w.db, registry, u, "y.json", []byte(document), options)
		if err != nil || report.Entries != 1 || report.Feeds != 0 || !report.Incomplete {
			t.Errorf("Import with handlers that refuse = %+v, %v", report, err)
		}
	})
}
