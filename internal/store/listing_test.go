package store

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/storetest"
)

// Entry identifiers of the shelf: seconds 2001 to 2006 in microseconds.
const (
	s1 int64 = 2001_000_000 + iota*1_000_000
	s2
	s3
	s4
	s5
	s6
)

// newShelf gives a user entries that every order sorts differently, and
// another user an entry under an identifier of the first.
func newShelf(t *testing.T, s *Store) *User {
	t.Helper()
	ctx := context.Background()
	alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
	for _, u := range []*User{alice, bob} {
		if err := s.CreateCategory(ctx, &Category{UserID: u.ID, ID: 2, Name: "News"}); err != nil {
			t.Fatal(err)
		}
		for _, f := range []*Feed{{Name: "beta"}, {Name: "Alpha", CategoryID: 2}} {
			f.UserID, f.URL = u.ID, "https://example.org/"+f.Name
			if err := s.CreateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
	}
	err := s.InsertEntries(ctx, alice.ID, []*Entry{
		{ID: s1, FeedID: 1, GUID: "1", Title: "apple", Published: 500},
		{ID: s2, FeedID: 2, GUID: "2", Title: "Zebra", Published: 300},
		{ID: s3, FeedID: 1, GUID: "3", Title: "Éclair", Published: 300},
		{ID: s4, FeedID: 2, GUID: "4", Title: "apple", Published: 100, IsRead: true},
		{ID: s5, FeedID: 1, GUID: "5", Published: 400, IsFavorite: true},
		{ID: s6, FeedID: 2, GUID: "6", Title: "banana", Published: 600, IsRead: true, IsFavorite: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEntries(ctx, bob.ID, []*Entry{{ID: s3, FeedID: 1, GUID: "3", Title: "apple"}}); err != nil {
		t.Fatal(err)
	}
	return alice
}

// pages reads a listing page by page and returns the identifiers and the
// number of pages.
func pages(t *testing.T, s *Store, userID int64, l Listing) (ids []int64, count int) {
	t.Helper()
	for {
		entries, next, err := s.ListPage(context.Background(), userID, l)
		if err != nil {
			t.Fatalf("ListPage(%+v): %v", l, err)
		}
		if l.Limit > 0 && len(entries) > l.Limit {
			t.Fatalf("ListPage(%+v) returned %d entries", l, len(entries))
		}
		for _, e := range entries {
			if e.UserID != userID || e.GUID == "" {
				t.Fatalf("ListPage(%+v): %+v is not a whole entry of the user", l, e)
			}
			ids = append(ids, e.ID)
		}
		count++
		if next == "" {
			return ids, count
		}
		if len(entries) == 0 {
			t.Fatalf("ListPage(%+v): an empty page that has a next one", l)
		}
		l.After = next
	}
}

// R4: a listing is sorted five ways in both directions, narrowed by set and
// state, and read page by page without gaps or repeats.
func TestListPage(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		alice := newShelf(t, s)
		for name, tc := range map[string]struct {
			l    Listing
			want []int64
		}{
			"added, newest first":     {Listing{}, []int64{s6, s5, s4, s3, s2, s1}},
			"added, oldest first":     {Listing{Ascending: true}, []int64{s1, s2, s3, s4, s5, s6}},
			"published, newest first": {Listing{Order: OrderPublished}, []int64{s6, s1, s5, s3, s2, s4}},
			"published, oldest first": {Listing{Order: OrderPublished, Ascending: true}, []int64{s4, s2, s3, s5, s1, s6}},
			// Bytes decide: no title, capitals, small letters, letters beyond ASCII.
			"title":                {Listing{Order: OrderTitle, Ascending: true}, []int64{s5, s2, s1, s4, s6, s3}},
			"title, backwards":     {Listing{Order: OrderTitle}, []int64{s3, s6, s4, s1, s2, s5}},
			"feed":                 {Listing{Order: OrderFeed, Ascending: true}, []int64{s2, s4, s6, s1, s3, s5}},
			"feed, backwards":      {Listing{Order: OrderFeed}, []int64{s5, s3, s1, s6, s4, s2}},
			"unread by title":      {Listing{Order: OrderTitle, Ascending: true, Read: boolPtr(false)}, []int64{s5, s2, s1, s3}},
			"starred by date":      {Listing{Order: OrderPublished, Favorite: boolPtr(true)}, []int64{s6, s5}},
			"unread or starred":    {Listing{UnreadOrFavorite: true}, []int64{s6, s5, s3, s2, s1}},
			"read and not starred": {Listing{Read: boolPtr(true), Favorite: boolPtr(false)}, []int64{s4}},
			"a category by title":  {Listing{Set: EntrySet{CategoryID: 2}, Order: OrderTitle, Ascending: true}, []int64{s2, s4, s6}},
			"a feed by date":       {Listing{Set: EntrySet{FeedID: 1}, Order: OrderPublished, Ascending: true}, []int64{s3, s5, s1}},
			"an unknown feed":      {Listing{Set: EntrySet{FeedID: 99}, Order: OrderFeed}, nil},
		} {
			for _, limit := range []int{0, 1, 2, 3, 6, 7} {
				tc.l.Limit = limit
				got, count := pages(t, s, alice.ID, tc.l)
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s, %d a page: %v, want %v", name, limit, got, tc.want)
				}
				wantCount := 1
				if limit > 0 && len(tc.want) > 0 {
					wantCount = (len(tc.want) + limit - 1) / limit
				}
				if count != wantCount {
					t.Errorf("%s, %d a page: %d pages, want %d", name, limit, count, wantCount)
				}
			}
		}
	})
}

// R4: a shuffle shows every entry once whatever the size of the page, and a
// seed always shuffles the same way.
func TestListPageShuffled(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		alice := newShelf(t, s)
		all := []int64{s1, s2, s3, s4, s5, s6}
		orders := map[string]bool{}
		for _, seed := range []int64{0, 1, 7, -5, 1 << 40, 123456789} {
			whole, _ := pages(t, s, alice.ID, Listing{Order: OrderRandom, Seed: seed})
			orders[fmt.Sprint(whole)] = true
			for _, limit := range []int{1, 2, 4} {
				// Pages of one shuffle are in the order of the whole shuffle.
				got, _ := pages(t, s, alice.ID, Listing{Order: OrderRandom, Seed: seed, Limit: limit})
				if !reflect.DeepEqual(got, whole) {
					t.Errorf("seed %d, %d a page: %v, want %v as without pages", seed, limit, got, whole)
				}
			}
			sorted := slices.Clone(whole)
			slices.Sort(sorted)
			if !reflect.DeepEqual(sorted, all) {
				t.Errorf("seed %d: entries %v, want each of %v once", seed, whole, all)
			}
		}
		if len(orders) < 2 {
			t.Errorf("six seeds shuffle the same way: %v", orders)
		}
	})
}

// R4: entries that arrive between two pages neither repeat nor hide others.
func TestListPageWhileEntriesArrive(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice := newShelf(t, s)
		arrive := func(guid string) {
			t.Helper()
			e := &Entry{FeedID: 1, GUID: guid, Title: "apple", Published: 450}
			if err := s.InsertEntries(ctx, alice.ID, []*Entry{e}); err != nil {
				t.Fatal(err)
			}
		}
		for i, tc := range []struct {
			l    Listing
			want []int64
		}{
			{Listing{}, []int64{s6, s5, s4, s3, s2, s1}},
			{Listing{Order: OrderPublished}, []int64{s6, s1, s5, s3, s2, s4}},
			{Listing{Order: OrderTitle, Ascending: true}, []int64{s5, s2, s1, s4, s6, s3}},
		} {
			tc.l.Limit = 3
			first, next, err := s.ListPage(ctx, alice.ID, tc.l)
			if err != nil {
				t.Fatal(err)
			}
			arrive(fmt.Sprintf("new-%d", i))
			tc.l.After = next
			rest, _ := pages(t, s, alice.ID, tc.l)
			all := rest
			for i, e := range first {
				all = slices.Insert(all, i, e.ID)
			}
			// What arrived, now or for a case before, stands somewhere among
			// the old entries; these follow each other as they did.
			var got []int64
			for _, id := range all {
				if id <= s6 {
					got = append(got, id)
				}
			}
			if seen := slices.Clone(all); len(slices.Compact(sorted(seen))) != len(all) {
				t.Errorf("case %d: an entry is listed twice in %v", i, all)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("case %d: old entries over two readings %v, want %v", i, got, tc.want)
			}
		}
	})
}

func TestListPageRefusesForeignPlace(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		alice := newShelf(t, s)
		// The last is a title with a NUL, which no entry has.
		nul := base64.RawURLEncoding.EncodeToString([]byte(`{"i":1,"t":"a\u0000b"}`))
		for _, after := range []string{"!", "bm90IGpzb24", nul} {
			_, _, err := s.ListPage(context.Background(), alice.ID, Listing{Order: OrderTitle, After: after})
			if !errors.Is(err, ErrCursor) {
				t.Errorf("After %q: error %v, want ErrCursor", after, err)
			}
		}
	})
}

// Searches by label and by what else the database tells apart, alone and
// mixed with texts, negations and alternatives; the reference cases have few.
var structuralSearches = []string{
	"L:1", "-L:1", "L:*", "-L:*", "!L:*", "L:1 L:12", "L:1,2", "L:2,12 hello", "L:99", "-L:99",
	"labels:work", `label:"my label"`, "labels:work,home", "-labels:work,home", "label:Bleu Alice", "labels:nothing",
	"labels:work OR intitle:test", "!(L:1 hello)", "-(L:1)", "(L:1) OR !(labels:home)", "(alpha) OR !(L:12 Hello)",
	"f:1 -L:* word1", "c:2 OR L:12", "!(c:2 beta) alpha", "!(c:2)", "(f:2) OR !(f:1 hello)", "(f:1) !(e:1180656000000001)",
	"e:1180656000000001,1704067200000000", "-e:1180656000000001", "e:01180656000000001", "e:", "f:", "-f:", "c:",
	"date:2014-03/ -f:2", "-date:2014-03/", "-date:/2014-03", "date:/2008 OR pubdate:2020/", "mdate:2014 -userdate:/2014-03-19",
	"-pubdate:2014-03 OR L:2", "!(date:2014-03-15) word1", "S:0 L:1", "search:Second OR labels:home",
}

// R6: a search returns the entries search.Query.Match picks among all the
// entries of the user, whatever the order and the size of the page.
func TestSearchListsWhatMatches(t *testing.T) {
	var input struct {
		Queries []search.SavedQuery `json:"queries"`
		Feeds   []struct {
			ID       int64 `json:"id"`
			Category int64 `json:"category"`
		} `json:"feeds"`
		Entries []struct {
			ID           string   `json:"id"`
			Feed         int64    `json:"feed"`
			Title        string   `json:"title"`
			Authors      []string `json:"authors"`
			Content      string   `json:"content"`
			Link         string   `json:"link"`
			Published    int64    `json:"published"`
			Tags         []string `json:"tags"`
			Modified     int64    `json:"modified"`
			UserModified int64    `json:"userModified"`
		} `json:"entries"`
		Cases []struct {
			Q string `json:"q"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("../../testdata/reference/oracle/search-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Cases) < 400 || len(input.Entries) != 7 {
		t.Fatalf("%d cases over %d entries", len(input.Cases), len(input.Entries))
	}
	queries := slices.Clone(structuralSearches)
	for _, c := range input.Cases {
		queries = append(queries, c.Q)
	}
	// Labels by the index of the entries that carry them.
	labels := []struct {
		label   search.Label
		entries []int
	}{
		{search.Label{ID: 1, Name: "work"}, []int{0, 2}},
		{search.Label{ID: 2, Name: "home"}, []int{1}},
		{search.Label{ID: 3, Name: "my label"}, []int{5}},
		{search.Label{ID: 12, Name: "Bleu"}, []int{0, 4}},
	}

	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		categories := map[int64]int64{}
		var subjects []*search.Entry
		for _, u := range []*User{alice, bob} {
			if err := s.CreateCategory(ctx, &Category{UserID: u.ID, ID: 2, Name: "News"}); err != nil {
				t.Fatal(err)
			}
			for _, f := range input.Feeds {
				categories[f.ID] = f.Category
				feed := &Feed{UserID: u.ID, ID: f.ID, CategoryID: f.Category, URL: "https://example.org/" + strconv.FormatInt(f.ID, 10)}
				if err := s.CreateFeed(ctx, feed); err != nil {
					t.Fatal(err)
				}
			}
			var entries []*Entry
			for _, e := range input.Entries {
				id, err := strconv.ParseInt(e.ID, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				entries = append(entries, &Entry{
					ID: id, FeedID: e.Feed, GUID: e.ID, Title: e.Title, Authors: e.Authors, Content: e.Content, Link: e.Link,
					Tags: e.Tags, Published: e.Published, LastModified: e.Modified, LastUserModified: e.UserModified,
				})
				if u == alice {
					subjects = append(subjects, &search.Entry{
						ID: id, FeedID: e.Feed, CategoryID: categories[e.Feed], Title: e.Title, Authors: e.Authors,
						Content: e.Content, Link: e.Link, Tags: e.Tags, Published: e.Published, LastModified: e.Modified,
						LastUserModified: e.UserModified,
					})
				}
			}
			// The other user has the same entries, and every label on each.
			if err := s.InsertEntries(ctx, u.ID, entries); err != nil {
				t.Fatal(err)
			}
			for _, l := range labels {
				if err := s.CreateTag(ctx, &Tag{UserID: u.ID, ID: l.label.ID, Name: l.label.Name}); err != nil {
					t.Fatal(err)
				}
				for i, e := range entries {
					if u == bob || slices.Contains(l.entries, i) {
						if err := s.TagEntry(ctx, u.ID, l.label.ID, e.ID); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
		for _, l := range labels {
			for _, i := range l.entries {
				subjects[i].Labels = append(subjects[i].Labels, l.label)
			}
		}
		titles := map[int64]string{}
		for _, e := range subjects {
			titles[e.ID] = e.Title
		}

		opts := search.Options{Queries: input.Queries, Now: time.Now().UTC(), Labels: true}
		narrowed := 0
		for _, text := range queries {
			q, err := search.Parse(text, opts)
			if err != nil {
				continue
			}
			var want []int64
			for _, e := range subjects {
				if q.Match(e) {
					want = append(want, e.ID)
				}
			}
			if len(want) > 0 && len(want) < len(subjects) {
				narrowed++
			}
			slices.SortFunc(want, func(a, b int64) int { return cmp.Compare(b, a) })
			for _, limit := range []int{0, 2} {
				got, _ := pages(t, s, alice.ID, Listing{Search: q, Limit: limit})
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%q, %d a page: entries %v, Match picks %v", text, limit, got, want)
				}
			}
			slices.SortFunc(want, func(a, b int64) int {
				if c := strings.Compare(titles[a], titles[b]); c != 0 {
					return c
				}
				return cmp.Compare(a, b)
			})
			got, _ := pages(t, s, alice.ID, Listing{Search: q, Order: OrderTitle, Ascending: true, Limit: 3})
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q by title: entries %v, Match picks %v", text, got, want)
			}
		}
		if narrowed < 200 {
			t.Errorf("only %d searches pick some entries and not all: the cases prove little", narrowed)
		}

		// More alternatives than SQLite takes in one expression.
		var feeds []string
		for i := range 1100 {
			feeds = append(feeds, "f:"+strconv.Itoa(i+1))
		}
		many, err := search.Parse(strings.Join(feeds, " OR "), opts)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := pages(t, s, alice.ID, Listing{Search: many, Limit: 4}); len(got) != len(subjects) {
			t.Errorf("a search of 1100 alternatives: %d entries, want all %d", len(got), len(subjects))
		}

		// A search inside a set and a state.
		q, err := search.Parse("word1 OR alpha", opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetEntriesRead(ctx, alice.ID, []int64{subjects[0].ID}, true, 1); err != nil {
			t.Fatal(err)
		}
		got, _ := pages(t, s, alice.ID, Listing{Search: q, Set: EntrySet{FeedID: 1}, Read: boolPtr(false), Limit: 1})
		if want := []int64{subjects[6].ID, subjects[2].ID}; !reflect.DeepEqual(got, want) {
			t.Errorf("a search among the unread of a feed: %v, want %v", got, want)
		}
	})
}

// R6: on 100 000 entries the first page of a search that one entry matches,
// the oldest, comes within two seconds. Run by `make bench`; with
// FRESHGO_TEST_POSTGRES_URL set, on PostgreSQL too.
func BenchmarkSearchRareMatch(b *testing.B) {
	const (
		entries = 100_000
		limit   = 2 * time.Second
	)
	words := strings.Fields(`time year people way day man thing woman life child world school state family
		student group country problem hand part place case week company system program question work government
		number night point home water room mother area money story fact month lot right study book eye job word
		business issue side kind head house service friend father power hour game line end member law car city`)
	rnd := rand.New(rand.NewPCG(1, 2))
	text := func(n int) string {
		var sb strings.Builder
		for range n {
			sb.WriteString(words[rnd.IntN(len(words))])
			sb.WriteByte(' ')
		}
		return sb.String()
	}
	for _, e := range storetest.Engines() {
		b.Run(e.Name, func(b *testing.B) {
			ctx := context.Background()
			driver, dsn := e.New(b)
			s, err := Open(ctx, driver, dsn)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close() })
			u := &User{Name: "alice"}
			if err := s.CreateUser(ctx, u); err != nil {
				b.Fatal(err)
			}
			const feeds = 20
			for i := range feeds {
				f := &Feed{UserID: u.ID, URL: fmt.Sprintf("https://example.org/%d", i), Name: text(2)}
				if err := s.CreateFeed(ctx, f); err != nil {
					b.Fatal(err)
				}
			}
			// About 2 KB of content an entry, 200 MB in all.
			for first := 0; first < entries; first += 1000 {
				batch := make([]*Entry, 1000)
				for i := range batch {
					n := first + i
					batch[i] = &Entry{
						FeedID: int64(1 + n%feeds), GUID: strconv.Itoa(n), Title: text(8), Authors: []string{text(2)},
						Content: "<p>" + text(400) + "</p>", Link: "https://example.org/" + strconv.Itoa(n),
						Published: int64(1_600_000_000 + n), Tags: []string{words[n%len(words)]}, IsRead: n%3 != 0,
					}
				}
				if first == 0 {
					batch[0].Content += "<p>a xylophone at last</p>"
				}
				if err := s.InsertEntries(ctx, u.ID, batch); err != nil {
					b.Fatal(err)
				}
			}
			for _, tc := range []struct {
				name string
				l    Listing
				want int
			}{
				{"word", Listing{}, 1},
				{"word by title", Listing{Order: OrderTitle, Ascending: true}, 1},
				{"word among the unread", Listing{Read: boolPtr(false)}, 1},
				{"no search, by date", Listing{Order: OrderPublished}, 50},
			} {
				b.Run(tc.name, func(b *testing.B) {
					if tc.want == 1 {
						q, err := search.Parse("xylophone", search.Options{Labels: true})
						if err != nil {
							b.Fatal(err)
						}
						tc.l.Search = q
					}
					tc.l.Limit = 50
					for b.Loop() {
						got, _, err := s.ListPage(ctx, u.ID, tc.l)
						if err != nil || len(got) != tc.want {
							b.Fatalf("ListPage: %d entries, %v; want %d", len(got), err, tc.want)
						}
					}
					if per := b.Elapsed() / time.Duration(b.N); per > limit {
						b.Fatalf("a page takes %v, the limit is %v", per, limit)
					}
				})
			}
			b.Run("counts", func(b *testing.B) {
				for b.Loop() {
					if _, err := s.FeedCounts(ctx, u.ID); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func sorted(ids []int64) []int64 {
	slices.Sort(ids)
	return ids
}
