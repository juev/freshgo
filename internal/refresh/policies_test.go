package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
)

const day = 24 * time.Hour

func (w *world) category(u *store.User, name, attributes string) *store.Category {
	w.t.Helper()
	c := &store.Category{UserID: u.ID, Name: name, Attributes: json.RawMessage(attributes)}
	if err := w.db.CreateCategory(context.Background(), c); err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *world) insert(u *store.User, entries ...*store.Entry) {
	w.t.Helper()
	if err := w.db.InsertEntries(context.Background(), u.ID, entries); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) label(u *store.User, name string) *store.Tag {
	w.t.Helper()
	tag := &store.Tag{UserID: u.ID, Name: name}
	if err := w.db.CreateTag(context.Background(), tag); err != nil {
		w.t.Fatal(err)
	}
	return tag
}

// unread returns the guids of the unread entries of a feed, sorted.
func (w *world) unread(f *store.Feed) []string {
	w.t.Helper()
	out := []string{}
	for _, e := range w.entries(f) {
		if !e.IsRead {
			out = append(out, e.GUID)
		}
	}
	sort.Strings(out)
	return out
}

// watchAutoRead collects the EntryAutoRead calls as "guid:why".
func (w *world) watchAutoRead() *[]string {
	var calls []string
	w.registry.EntryAutoRead.Add(0, func(_ context.Context, a hooks.EntryAuto) bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		calls = append(calls, a.Entry.GUID+":"+a.Why)
		return false
	})
	return &calls
}

// R10: the entries left by freshgo are the ones cli/purge.php of FreshRSS
// leaves, for the cases of testdata/reference/oracle/purge-cases.json.
func TestPurgeMatchesFreshRSS(t *testing.T) {
	type group struct {
		Count    int   `json:"count"`
		Age      int64 `json:"age"`
		Read     bool  `json:"read"`
		Favorite bool  `json:"favorite"`
		Labelled bool  `json:"labelled"`
	}
	type holder struct {
		Name      string          `json:"name"`
		Category  string          `json:"category"`
		Archiving json.RawMessage `json:"archiving"`
		Groups    []group         `json:"groups"`
	}
	var cases struct {
		Categories []holder `json:"categories"`
		Feeds      []holder `json:"feeds"`
	}
	readJSON(t, filepath.Join(referenceDir, "oracle", "purge-cases.json"), &cases)
	var want map[string][]int
	readJSON(t, filepath.Join(referenceDir, "oracle", "purge.json"), &want)
	attributes := func(h holder) string {
		if h.Archiving == nil {
			return `{}`
		}
		return `{"archiving":` + string(h.Archiving) + `}`
	}

	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		// A new user of freshgo has the retention of a new user of FreshRSS.
		u := w.user("purge", `{}`)
		tag := w.label(u, "kept")
		categories := map[string]int64{}
		for _, c := range cases.Categories {
			categories[c.Name] = w.category(u, c.Name, attributes(c)).ID
		}
		feeds := map[string]*store.Feed{}
		wantDeleted := 0
		for _, c := range cases.Feeds {
			f := w.feed(u, "/"+c.Name, func(f *store.Feed) {
				f.CategoryID = categories[c.Category]
				f.Attributes = json.RawMessage(attributes(c))
			})
			feeds[c.Name] = f
			var entries, labelled []*store.Entry
			for g, group := range c.Groups {
				for i := range group.Count {
					e := &store.Entry{
						FeedID: f.ID, GUID: fmt.Sprintf("%d-%d", g, i), Title: "Entry",
						LastSeen: w.clock.Unix() - group.Age, IsRead: group.Read, IsFavorite: group.Favorite,
					}
					entries = append(entries, e)
					if group.Labelled {
						labelled = append(labelled, e)
					}
				}
				wantDeleted += group.Count - want[c.Name][g]
			}
			w.insert(u, entries...)
			for _, e := range labelled {
				if err := w.db.TagEntry(ctx, u.ID, tag.ID, e.ID); err != nil {
					t.Fatal(err)
				}
			}
		}

		stats, err := w.r.Purge(ctx)
		if err != nil {
			t.Fatalf("Purge: %v", err)
		}
		if wantStats := []PurgeStats{{User: "purge", Deleted: wantDeleted}}; !reflect.DeepEqual(stats, wantStats) {
			t.Errorf("Purge = %+v, want %+v", stats, wantStats)
		}
		for _, c := range cases.Feeds {
			got := make([]int, len(c.Groups))
			for _, e := range w.entries(feeds[c.Name]) {
				var g, i int
				if _, err := fmt.Sscanf(e.GUID, "%d-%d", &g, &i); err != nil {
					t.Fatal(err)
				}
				got[g]++
			}
			if !reflect.DeepEqual(got, want[c.Name]) {
				t.Errorf("%s: entries left per group %v, FreshRSS leaves %v", c.Name, got, want[c.Name])
			}
		}
	})
}

func readJSON(t *testing.T, file string, v any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

// R10: a refresh cleans the feed up with the default settings: what is past
// the 200 entries listed most recently goes, starred and labelled entries
// and everything the feed still lists stay.
func TestRefreshDeletesOldEntries(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		tag := w.label(u, "later")
		f := w.feed(u, "/feed", nil)
		now := w.clock.Unix()
		var (
			entries []*store.Entry
			items   []item
		)
		add := func(kind string, n int, age time.Duration, change func(*store.Entry)) {
			for i := range n {
				// Every entry was listed at a moment of its own.
				e := &store.Entry{FeedID: f.ID, GUID: fmt.Sprintf("%s-%d", kind, i), Title: kind,
					LastSeen: now - int64(age/time.Second) - int64(i)}
				if change != nil {
					change(e)
				}
				entries = append(entries, e)
			}
		}
		add("listed", 60, 30*day, nil)
		add("recent", 140, 10*day, nil)
		add("beyond", 60, 30*day, nil)
		add("ancient", 30, 100*day, nil)
		add("starred", 5, 100*day, func(e *store.Entry) { e.IsFavorite = true })
		add("labelled", 5, 100*day, nil)
		w.insert(u, entries...)
		for _, e := range entries {
			if e.Title == "labelled" {
				if err := w.db.TagEntry(ctx, u.ID, tag.ID, e.ID); err != nil {
					t.Fatal(err)
				}
			}
			if e.Title == "listed" {
				items = append(items, item{guid: e.GUID, title: "listed", body: "text"})
			}
		}
		w.serveBody("/feed", rssType, rss("Blog", items...))

		if st := w.runOne(); st.Refreshed != 1 || st.NewEntries != 0 {
			t.Fatalf("stats %+v", st)
		}
		got := map[string]int{}
		for _, e := range w.entries(f) {
			got[e.Title]++
		}
		if want := map[string]int{"listed": 60, "recent": 140, "starred": 5, "labelled": 5}; !reflect.DeepEqual(got, want) {
			t.Errorf("entries left: %v, want %v", got, want)
		}
	})
}

// R10: the retention of the feed stands before that of its category, and
// that before the user's.
func TestRetentionIsInherited(t *testing.T) {
	const keepTwo = `{"archiving":{"keep_period":false,"keep_max":2,"keep_min":0,"keep_favourites":false,"keep_labels":false,"keep_unreads":false}}`
	const keepFour = `{"archiving":{"keep_period":false,"keep_max":4,"keep_min":0,"keep_favourites":false,"keep_labels":false,"keep_unreads":false}}`
	const keepSix = `{"archiving":{"keep_period":false,"keep_max":6,"keep_min":0,"keep_favourites":false,"keep_labels":false,"keep_unreads":false}}`
	cases := []struct {
		name                 string
		user, category, feed string
		want                 int
	}{
		{"user", keepSix, `{}`, `{}`, 6},
		{"category over user", keepSix, keepFour, `{}`, 4},
		{"feed over category", keepSix, keepFour, keepTwo, 2},
		{"feed over user", keepSix, `{}`, keepTwo, 2},
		{"a period that cannot be read turns the rule off", `{"archiving":{"keep_period":"3 months","keep_max":6,"keep_min":0}}`, `{}`, `{}`, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				u := w.user("alice", c.user)
				cat := w.category(u, "News", c.category)
				f := w.feed(u, "/feed", func(f *store.Feed) { f.CategoryID, f.Attributes = cat.ID, json.RawMessage(c.feed) })
				var entries []*store.Entry
				for i := range 10 {
					entries = append(entries, &store.Entry{FeedID: f.ID, GUID: fmt.Sprint(i), LastSeen: w.clock.Unix() - int64(i)*86400})
				}
				w.insert(u, entries...)
				stats, err := w.r.Purge(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if left := len(w.entries(f)); left != c.want || stats[0].Deleted != 10-c.want {
					t.Errorf("%d entries left, %d reported deleted; want %d left", left, stats[0].Deleted, c.want)
				}
			})
		})
	}
}

// R10: entries the feed no longer lists become read when asked.
func TestReadUponGone(t *testing.T) {
	cases := []struct {
		name           string
		userSettings   string
		feedAttributes string
		wantUnread     []string
	}{
		{"off by default", `{}`, `{}`, []string{"a", "b", "gone"}},
		{"user setting", `{"mark_when":{"gone":true}}`, `{}`, []string{"a", "b"}},
		{"feed attribute", `{}`, `{"read_upon_gone":true}`, []string{"a", "b"}},
		{"feed attribute wins over the user setting", `{"mark_when":{"gone":true}}`, `{"read_upon_gone":false}`, []string{"a", "b", "gone"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				u := w.user("alice", c.userSettings)
				a := item{guid: "a", title: "First", body: "one"}
				b := item{guid: "b", title: "Second", body: "two"}
				gone := item{guid: "gone", title: "Third", body: "three"}
				body := rss("Blog", gone, b, a)
				// The server answers 304 to a request that repeats the validator.
				w.serve("/feed", func(rw http.ResponseWriter, req *http.Request) {
					w.mu.Lock()
					current := body
					w.mu.Unlock()
					etag := fmt.Sprintf(`"%d"`, len(current))
					rw.Header().Set("ETag", etag)
					if req.Header.Get("If-None-Match") == etag {
						rw.WriteHeader(http.StatusNotModified)
						return
					}
					rw.Header().Set("Content-Type", rssType)
					_, _ = io.WriteString(rw, current)
				})
				f := w.feed(u, "/feed", func(f *store.Feed) { f.Attributes = json.RawMessage(c.feedAttributes) })
				w.runOne()
				if got, want := w.unread(f), []string{"a", "b", "gone"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("unread after the first refresh: %v, want %v", got, want)
				}

				w.mu.Lock()
				body = rss("Blog", b, a)
				w.mu.Unlock()
				w.later()
				w.runOne()
				if got := w.unread(f); !reflect.DeepEqual(got, c.wantUnread) {
					t.Errorf("unread after the entry left the feed: %v, want %v", got, c.wantUnread)
				}

				// The user reads it again as unread; an unchanged feed (304)
				// still does not list it.
				e := w.entry(f, "gone")
				e.IsRead = false
				if err := w.db.UpdateEntry(context.Background(), e); err != nil {
					t.Fatal(err)
				}
				w.later()
				hits := w.hitCount("/feed")
				w.runOne()
				if w.hitCount("/feed") != hits+1 {
					t.Fatal("the feed was not requested")
				}
				if got := w.unread(f); !reflect.DeepEqual(got, c.wantUnread) {
					t.Errorf("unread after a 304: %v, want %v", got, c.wantUnread)
				}
			})
		})
	}
}

// A feed that comes back without items has lost all its entries.
func TestReadUponGoneWhenTheFeedIsEmpty(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{"mark_when":{"gone":true}}`)
		w.serveBody("/feed", rssType, rss("Blog", item{guid: "a", title: "First", body: "one"}))
		f := w.feed(u, "/feed", nil)
		w.runOne()
		w.serveBody("/feed", rssType, rss("Blog"))
		w.later()
		if st := w.runOne(); st.Refreshed != 1 || st.Failed != 0 {
			t.Fatalf("stats %+v", st)
		}
		if got := w.unread(f); len(got) != 0 {
			t.Errorf("unread: %v, want none", got)
		}
	})
}

// R10: a feed keeps at most the given number of unread entries, the newest.
func TestKeepMaxUnread(t *testing.T) {
	var fifteen []item
	for i := range 15 {
		// The last item of the document is stored first.
		fifteen = append(fifteen, item{guid: fmt.Sprintf("%02d", 14-i), title: fmt.Sprintf("Entry %d", 14-i), body: "text"})
	}
	newest := func(n int) []string {
		out := []string{}
		for i := 15 - n; i < 15; i++ {
			out = append(out, fmt.Sprintf("%02d", i))
		}
		return out
	}
	cases := []struct {
		name           string
		userSettings   string
		feedAttributes string
		wantUnread     []string
	}{
		{"no limit by default", `{}`, `{}`, newest(15)},
		{"user setting", `{"mark_when":{"max_n_unread":10}}`, `{}`, newest(10)},
		{"off in the user settings", `{"mark_when":{"max_n_unread":false}}`, `{}`, newest(15)},
		{"feed attribute", `{"mark_when":{"max_n_unread":10}}`, `{"keep_max_n_unread":2}`, newest(2)},
		{"none at all", `{}`, `{"keep_max_n_unread":0}`, newest(0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				u := w.user("alice", c.userSettings)
				w.serveBody("/feed", rssType, rss("Blog", fifteen...))
				f := w.feed(u, "/feed", func(f *store.Feed) { f.Attributes = json.RawMessage(c.feedAttributes) })
				if st := w.runOne(); st.NewEntries != 15 {
					t.Fatalf("stats %+v", st)
				}
				if got := w.unread(f); !reflect.DeepEqual(got, c.wantUnread) {
					t.Errorf("unread: %v, want %v", got, c.wantUnread)
				}
			})
		})
	}

	// As in FreshRSS, a refresh that brings nothing leaves alone what the
	// user marked unread.
	t.Run("only when the feed changed", func(t *testing.T) {
		eachEngine(t, func(t *testing.T, w *world) {
			u := w.user("alice", `{"mark_when":{"max_n_unread":2}}`)
			w.serveBody("/feed", rssType, rss("Blog", fifteen...))
			f := w.feed(u, "/feed", nil)
			w.runOne()
			e := w.entry(f, "00")
			e.IsRead = false
			if err := w.db.UpdateEntry(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			w.later()
			w.runOne()
			if got, want := w.unread(f), []string{"00", "13", "14"}; !reflect.DeepEqual(got, want) {
				t.Errorf("unread after a refresh without news: %v, want %v", got, want)
			}

			w.serveBody("/feed", rssType, rss("Blog", append([]item{{guid: "15", title: "Entry 15", body: "text"}}, fifteen...)...))
			w.later()
			w.runOne()
			if got, want := w.unread(f), []string{"14", "15"}; !reflect.DeepEqual(got, want) {
				t.Errorf("unread after a new entry: %v, want %v", got, want)
			}
		})
	})
}

// R10: entries are read on arrival when asked, changed ones included.
func TestReadUponReception(t *testing.T) {
	cases := []struct {
		name           string
		userSettings   string
		feedAttributes string
		wantRead       bool
	}{
		{"off by default", `{}`, `{}`, false},
		{"user setting", `{"mark_when":{"reception":true}}`, `{}`, true},
		{"feed attribute", `{}`, `{"read_upon_reception":true}`, true},
		{"feed attribute wins over the user setting", `{"mark_when":{"reception":true}}`, `{"read_upon_reception":false}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				calls := w.watchAutoRead()
				u := w.user("alice", c.userSettings)
				a := item{guid: "a", title: "First", body: "one"}
				w.serveBody("/feed", rssType, rss("Blog", a))
				f := w.feed(u, "/feed", func(f *store.Feed) { f.Attributes = json.RawMessage(c.feedAttributes) })
				w.runOne()
				if got := w.entry(f, "a").IsRead; got != c.wantRead {
					t.Errorf("new entry: read %v, want %v", got, c.wantRead)
				}
				want := []string(nil)
				if c.wantRead {
					want = []string{"a:upon_reception"}
				}
				if !reflect.DeepEqual(*calls, want) {
					t.Errorf("EntryAutoRead calls: %v, want %v", *calls, want)
				}

				// Unread again, then changed in the feed.
				e := w.entry(f, "a")
				e.IsRead = false
				if err := w.db.UpdateEntry(context.Background(), e); err != nil {
					t.Fatal(err)
				}
				a.body = "one, corrected"
				w.serveBody("/feed", rssType, rss("Blog", a))
				w.later()
				if st := w.runOne(); st.UpdatedEntries != 1 {
					t.Fatalf("stats %+v", st)
				}
				if got := w.entry(f, "a").IsRead; got != c.wantRead {
					t.Errorf("changed entry: read %v, want %v", got, c.wantRead)
				}
			})
		})
	}
}

// R10: a new entry whose title one of the latest entries of the feed has is
// read on arrival.
func TestReadWhenSameTitleInFeed(t *testing.T) {
	// The second refresh finds "old" changed and brings "again" with the
	// title of "old", "archive-again" with the title of "oldest", "twin-1"
	// and "twin-2" with one title, and two entries without a title.
	all := []string{"again", "archive-again", "bare-1", "bare-2", "newer", "old", "oldest", "twin-1", "twin-2"}
	cases := []struct {
		name           string
		userSettings   string
		feedAttributes string
		wantUnread     []string
	}{
		{"off by default", `{}`, `{}`, all},
		{"user setting", `{"mark_when":{"same_title_in_feed":25}}`, `{}`,
			[]string{"bare-1", "bare-2", "newer", "old", "oldest", "twin-1"}},
		{"feed attribute", `{}`, `{"read_when_same_title_in_feed":25}`,
			[]string{"bare-1", "bare-2", "newer", "old", "oldest", "twin-1"}},
		{"feed attribute turns it off", `{"mark_when":{"same_title_in_feed":25}}`, `{"read_when_same_title_in_feed":false}`, all},
		{"only the latest entries count", `{}`, `{"read_when_same_title_in_feed":2}`,
			[]string{"archive-again", "bare-1", "bare-2", "newer", "old", "oldest", "twin-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				calls := w.watchAutoRead()
				u := w.user("alice", c.userSettings)
				oldest := item{guid: "oldest", title: "Archive", body: "zero"}
				old := item{guid: "old", title: "Weekly digest", body: "one"}
				newer := item{guid: "newer", title: "Something else", body: "two"}
				w.serveBody("/feed", rssType, rss("Blog", newer, old, oldest))
				f := w.feed(u, "/feed", func(f *store.Feed) { f.Attributes = json.RawMessage(c.feedAttributes) })
				w.runOne()
				if got, want := w.unread(f), []string{"newer", "old", "oldest"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("unread after the first refresh: %v, want %v", got, want)
				}

				// A changed entry is not a repeat of itself: "old" stays unread.
				old.body = "one, corrected"
				w.serveBody("/feed", rssType, rss("Blog",
					item{guid: "bare-2", body: "no title"},
					item{guid: "bare-1", body: "no title either"},
					item{guid: "twin-2", title: "Twins", body: "b"},
					item{guid: "twin-1", title: "Twins", body: "a"},
					item{guid: "archive-again", title: "Archive", body: "four"},
					item{guid: "again", title: "Weekly digest", body: "three"},
					newer, old, oldest))
				w.later()
				if st := w.runOne(); st.NewEntries != 6 || st.UpdatedEntries != 1 {
					t.Fatalf("stats %+v", st)
				}
				if got := w.unread(f); !reflect.DeepEqual(got, c.wantUnread) {
					t.Errorf("unread: %v, want %v", got, c.wantUnread)
				}
				for _, call := range *calls {
					switch call {
					case "again:same_title_in_feed", "archive-again:same_title_in_feed", "twin-2:same_title_in_feed":
					default:
						t.Errorf("unexpected EntryAutoRead call %q", call)
					}
				}
				if wantCalls := len(all) - len(c.wantUnread); len(*calls) != wantCalls {
					t.Errorf("EntryAutoRead calls: %v, want %d", *calls, wantCalls)
				}
			})
		})
	}
}

// R10: with the rules of a category, an entry that another feed of the
// category already has, by title or by identifier, is read on arrival. The
// feeds are refreshed side by side and still see each other's entries.
func TestReadWhenSameInCategory(t *testing.T) {
	cases := []struct {
		name               string
		categoryAttributes string
		// How many of the two feeds keep the entry unread: the shared-title
		// one, the shared-identifier one.
		wantTitleUnread, wantGUIDUnread int
		wantWhy                         string
	}{
		{"off by default", `{}`, 2, 2, ""},
		{"same title", `{"read_when_same_title_in_category":25}`, 1, 2, "same_title_in_feed"},
		{"same identifier", `{"read_when_same_guid_in_category":25}`, 2, 1, "same_guid_in_category"},
		{"without a limit", `{"read_when_same_guid_in_category":0}`, 2, 1, "same_guid_in_category"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				calls := w.watchAutoRead()
				u := w.user("alice", `{}`)
				cat := w.category(u, "News", c.categoryAttributes)
				w.serveBody("/one", rssType, rss("One",
					item{guid: "shared-guid", title: "From one", body: "text"},
					item{guid: "one-title", title: "Breaking", body: "text"}))
				w.serveBody("/two", rssType, rss("Two",
					item{guid: "shared-guid", title: "From two", body: "text"},
					item{guid: "two-title", title: "Breaking", body: "text"}))
				w.serveBody("/elsewhere", rssType, rss("Elsewhere",
					item{guid: "shared-guid", title: "Breaking", body: "text"}))
				inCategory := func(f *store.Feed) { f.CategoryID = cat.ID }
				one, two := w.feed(u, "/one", inCategory), w.feed(u, "/two", inCategory)
				elsewhere := w.feed(u, "/elsewhere", nil)
				if st := w.runOne(); st.NewEntries != 5 {
					t.Fatalf("stats %+v", st)
				}

				unread := append(w.unread(one), w.unread(two)...)
				titles, guids := 0, 0
				for _, guid := range unread {
					if guid == "shared-guid" {
						guids++
					} else {
						titles++
					}
				}
				if titles != c.wantTitleUnread || guids != c.wantGUIDUnread {
					t.Errorf("unread: %v; want %d with the shared title and %d with the shared identifier",
						unread, c.wantTitleUnread, c.wantGUIDUnread)
				}
				if got, want := w.unread(elsewhere), []string{"shared-guid"}; !reflect.DeepEqual(got, want) {
					t.Errorf("a feed of another category: unread %v, want %v", got, want)
				}
				if c.wantWhy == "" {
					if len(*calls) != 0 {
						t.Errorf("EntryAutoRead calls: %v", *calls)
					}
					return
				}
				if len(*calls) != 1 || (*calls)[0] != "shared-guid:"+c.wantWhy && (*calls)[0] != "one-title:"+c.wantWhy && (*calls)[0] != "two-title:"+c.wantWhy {
					t.Errorf("EntryAutoRead calls: %v, want one with the reason %s", *calls, c.wantWhy)
				}
			})
		})
	}
}

func TestPolicySettings(t *testing.T) {
	got := readUserSettings(json.RawMessage(`{}`))
	if got.archiving != defaultArchiving || got.readUponGone || got.readUponReception || got.maxUnread != -1 || got.sameTitleInFeed != 0 {
		t.Errorf("settings of a new user: %+v", got)
	}
	got = readUserSettings(json.RawMessage(`{
		"archiving": {"keep_period": false, "keep_max": false, "keep_min": 7, "keep_favourites": false, "keep_labels": true, "keep_unreads": true},
		"mark_when": {"gone": true, "reception": true, "max_n_unread": 0, "same_title_in_feed": true}}`))
	if want := (archiving{keepMin: 7, keepLabels: true, keepUnreads: true}); got.archiving != want {
		t.Errorf("archiving %+v, want %+v", got.archiving, want)
	}
	if !got.readUponGone || !got.readUponReception || got.maxUnread != 0 || got.sameTitleInFeed != 1 {
		t.Errorf("mark_when: %+v", got)
	}
}
