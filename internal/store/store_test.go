package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/storetest"
)

// eachEngine runs the test against every available engine with an empty database.
func eachEngine(t *testing.T, test func(t *testing.T, s *Store)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			s, err := Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			test(t, s)
		})
	}
}

func mustUser(t *testing.T, s *Store, name string) *User {
	t.Helper()
	u := &User{Name: name}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser(%q): %v", name, err)
	}
	return u
}

func mustFeed(t *testing.T, s *Store, userID int64, feedURL string) *Feed {
	t.Helper()
	f := &Feed{UserID: userID, URL: feedURL, Name: feedURL}
	if err := s.CreateFeed(context.Background(), f); err != nil {
		t.Fatalf("CreateFeed(%q): %v", feedURL, err)
	}
	return f
}

func TestOpenTwiceKeepsData(t *testing.T) {
	ctx := context.Background()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			first, err := Open(ctx, driver, dsn)
			if err != nil {
				t.Fatalf("first Open: %v", err)
			}
			mustUser(t, first, "alice")
			if err := first.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			second, err := Open(ctx, driver, dsn)
			if err != nil {
				t.Fatalf("second Open: %v", err)
			}
			t.Cleanup(func() { _ = second.Close() })
			if _, err := second.UserByName(ctx, "alice"); err != nil {
				t.Errorf("user created before reopening: %v", err)
			}
			migrations, err := loadMigrations(driver)
			if err != nil {
				t.Fatal(err)
			}
			var version int
			if err := second.queryRow(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != len(migrations) {
				t.Errorf("schema version = %d, want %d", version, len(migrations))
			}
		})
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			s, err := Open(ctx, driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := s.exec(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", 9999); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if again, err := Open(ctx, driver, dsn); err == nil {
				_ = again.Close()
				t.Error("Open of a database with a newer schema: no error")
			}
		})
	}
}

func TestUsers(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		bob := &User{Name: "bob", APIPasswordHash: "$2y$09$hash", Settings: json.RawMessage(`{"language":"ru"}`)}
		if err := s.CreateUser(ctx, bob); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		alice := mustUser(t, s, "alice")
		if bob.ID == 0 || alice.ID == 0 || bob.ID == alice.ID {
			t.Fatalf("user ids: bob %d, alice %d; want distinct non-zero", bob.ID, alice.ID)
		}

		got, err := s.UserByName(ctx, "bob")
		if err != nil {
			t.Fatalf("UserByName: %v", err)
		}
		if !reflect.DeepEqual(got, bob) {
			t.Errorf("UserByName = %+v, want %+v", got, bob)
		}
		if string(alice.Settings) != "" {
			t.Fatalf("test setup: alice.Settings = %q", alice.Settings)
		}
		gotAlice, err := s.UserByName(ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if string(gotAlice.Settings) != "{}" {
			t.Errorf("empty settings stored as %q, want {}", gotAlice.Settings)
		}

		if _, err := s.UserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
			t.Errorf("UserByName(unknown) error = %v, want ErrNotFound", err)
		}
		if err := s.CreateUser(ctx, &User{Name: "bob"}); !errors.Is(err, ErrConflict) {
			t.Errorf("CreateUser(duplicate) error = %v, want ErrConflict", err)
		}

		users, err := s.Users(ctx)
		if err != nil {
			t.Fatalf("Users: %v", err)
		}
		var names []string
		for _, u := range users {
			names = append(names, u.Name)
		}
		if want := []string{"alice", "bob"}; !reflect.DeepEqual(names, want) {
			t.Errorf("Users = %v, want %v", names, want)
		}

		categories, err := s.Categories(ctx, bob.ID)
		if err != nil {
			t.Fatalf("Categories: %v", err)
		}
		if len(categories) != 1 || categories[0].ID != DefaultCategoryID || categories[0].Name != DefaultCategoryName {
			t.Errorf("categories of a new user = %+v, want only the default one", categories)
		}
	})
}

func TestIdentifiersArePerUser(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")

		for _, u := range []*User{alice, bob} {
			c := &Category{UserID: u.ID, Name: "News"}
			if err := s.CreateCategory(ctx, c); err != nil {
				t.Fatalf("CreateCategory for %s: %v", u.Name, err)
			}
			if c.ID != 2 {
				t.Errorf("%s: first own category id = %d, want 2", u.Name, c.ID)
			}
			if f := mustFeed(t, s, u.ID, "https://example.org/feed"); f.ID != 1 {
				t.Errorf("%s: first feed id = %d, want 1", u.Name, f.ID)
			}
			tag := &Tag{UserID: u.ID, Name: "later"}
			if err := s.CreateTag(ctx, tag); err != nil {
				t.Fatalf("CreateTag for %s: %v", u.Name, err)
			}
			if tag.ID != 1 {
				t.Errorf("%s: first tag id = %d, want 1", u.Name, tag.ID)
			}
		}

		// An identifier given by the caller is kept and moves the counter past it.
		imported := &Feed{UserID: alice.ID, ID: 40, URL: "https://example.org/imported", Name: "imported"}
		if err := s.CreateFeed(ctx, imported); err != nil {
			t.Fatalf("CreateFeed with id: %v", err)
		}
		if imported.ID != 40 {
			t.Errorf("explicit feed id became %d, want 40", imported.ID)
		}
		if next := mustFeed(t, s, alice.ID, "https://example.org/next"); next.ID != 41 {
			t.Errorf("feed id after explicit 40 = %d, want 41", next.ID)
		}
		older := &Feed{UserID: alice.ID, ID: 7, URL: "https://example.org/older", Name: "older"}
		if err := s.CreateFeed(ctx, older); err != nil {
			t.Fatalf("CreateFeed with a smaller id: %v", err)
		}
		if next := mustFeed(t, s, alice.ID, "https://example.org/after-older"); next.ID != 42 {
			t.Errorf("feed id after explicit 7 = %d, want 42", next.ID)
		}
		if err := s.CreateFeed(ctx, &Feed{UserID: alice.ID, ID: 40, URL: "https://example.org/dup", Name: "dup"}); !errors.Is(err, ErrConflict) {
			t.Errorf("CreateFeed with a taken id: error = %v, want ErrConflict", err)
		}

		if err := s.CreateCategory(ctx, &Category{UserID: alice.ID, Name: "News"}); !errors.Is(err, ErrConflict) {
			t.Errorf("CreateCategory with a taken name: error = %v, want ErrConflict", err)
		}
	})
}

func TestFeedRoundTrip(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		category := &Category{UserID: u.ID, Name: "Блоги", Kind: 2, LastUpdate: 1700000000, Error: 1700000001,
			Attributes: json.RawMessage(`{"opml_url":"https://example.org/list.opml"}`)}
		if err := s.CreateCategory(ctx, category); err != nil {
			t.Fatalf("CreateCategory: %v", err)
		}
		want := &Feed{
			UserID: u.ID, URL: "https://example.org/feed?a=1&b=2", Kind: 10, CategoryID: category.ID,
			Name: "Tom & Jerry <news>", Website: "https://example.org/", Description: "Новости — каждый день",
			LastUpdate: 1700000100, Priority: -5, PathEntries: "article .body", HTTPAuth: "user:p@ss",
			Error: 1700000200, TTL: -3600,
			Attributes: json.RawMessage(`{"xpath":{"item":"//article"},"curl_params":{"10004":"proxy:8080"}}`),
			HTTPETag:   `W/"abc"`, HTTPLastModified: "Tue, 06 Oct 2026 10:00:00 GMT",
		}
		if err := s.CreateFeed(ctx, want); err != nil {
			t.Fatalf("CreateFeed: %v", err)
		}
		got, err := s.FeedByID(ctx, u.ID, want.ID)
		if err != nil {
			t.Fatalf("FeedByID: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FeedByID:\n got %+v\nwant %+v", got, want)
		}

		plain := mustFeed(t, s, u.ID, "https://example.org/plain")
		if plain.CategoryID != DefaultCategoryID {
			t.Errorf("feed without a category went to %d, want the default category", plain.CategoryID)
		}
		feeds, err := s.Feeds(ctx, u.ID)
		if err != nil {
			t.Fatalf("Feeds: %v", err)
		}
		if len(feeds) != 2 || !reflect.DeepEqual(feeds[0], want) || feeds[1].ID != plain.ID {
			t.Errorf("Feeds = %d items, want the two created feeds in id order", len(feeds))
		}

		categories, err := s.Categories(ctx, u.ID)
		if err != nil {
			t.Fatalf("Categories: %v", err)
		}
		if len(categories) != 2 || !reflect.DeepEqual(categories[1], category) {
			t.Errorf("Categories[1] = %+v, want %+v", categories[len(categories)-1], category)
		}

		if _, err := s.FeedByID(ctx, u.ID, 999); !errors.Is(err, ErrNotFound) {
			t.Errorf("FeedByID(unknown) error = %v, want ErrNotFound", err)
		}
		other := mustUser(t, s, "bob")
		if _, err := s.FeedByID(ctx, other.ID, want.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("FeedByID of another user's feed: error = %v, want ErrNotFound", err)
		}
		orphan := &Feed{UserID: u.ID, URL: "https://example.org/orphan", Name: "orphan", CategoryID: 777}
		if err := s.CreateFeed(ctx, orphan); err == nil {
			t.Error("CreateFeed in a missing category: no error, foreign keys are not enforced")
		}
	})
}

func TestEntryRoundTrip(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")

		full := &Entry{
			FeedID: feed.ID, GUID: "tag:example.org,2026:1?a=1&amp;b=2", Title: "Привет, <мир> & co",
			Authors: []string{"Иван Петров", "O'Neil; Jr."}, Content: "<p>Текст 🚀</p>",
			Link: "https://example.org/1?a=1&b=2", Published: 1700000000, LastSeen: 1700000500,
			LastModified: 1700000300, LastUserModified: 1700000400, Hash: []byte{0x00, 0xff, 0x10, 0x80},
			IsRead: true, IsFavorite: true, Tags: []string{"php", "two words"},
			Attributes: json.RawMessage(`{"enclosures":[{"url":"https://example.org/a.mp3"}]}`),
		}
		bare := &Entry{FeedID: feed.ID, GUID: "bare"}
		if err := s.InsertEntries(ctx, u.ID, []*Entry{full, bare}); err != nil {
			t.Fatalf("InsertEntries: %v", err)
		}

		got, err := s.EntryByID(ctx, u.ID, full.ID)
		if err != nil {
			t.Fatalf("EntryByID: %v", err)
		}
		if !reflect.DeepEqual(got, full) {
			t.Errorf("EntryByID:\n got %+v\nwant %+v", got, full)
		}

		gotBare, err := s.EntryByID(ctx, u.ID, bare.ID)
		if err != nil {
			t.Fatalf("EntryByID(bare): %v", err)
		}
		if gotBare.Hash != nil {
			t.Errorf("entry inserted without a hash has hash %x, want nil", gotBare.Hash)
		}
		if gotBare.IsRead || gotBare.IsFavorite || gotBare.Authors != nil || gotBare.Tags != nil {
			t.Errorf("bare entry = %+v, want zero read/favorite/authors/tags", gotBare)
		}

		if _, err := s.EntryByID(ctx, u.ID, 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("EntryByID(unknown) error = %v, want ErrNotFound", err)
		}

		other := mustFeed(t, s, u.ID, "https://example.org/other")
		elsewhere := &Entry{FeedID: other.ID, GUID: "elsewhere"}
		if err := s.InsertEntries(ctx, u.ID, []*Entry{elsewhere}); err != nil {
			t.Fatal(err)
		}
		byFeed, err := s.EntriesByFeed(ctx, u.ID, feed.ID)
		if err != nil {
			t.Fatalf("EntriesByFeed: %v", err)
		}
		if len(byFeed) != 2 || !reflect.DeepEqual(byFeed[0], full) || byFeed[1].ID != bare.ID {
			t.Errorf("EntriesByFeed = %d entries, want the two of the feed in id order", len(byFeed))
		}
	})
}

func TestDuplicateGUID(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		first := mustFeed(t, s, u.ID, "https://example.org/first")
		second := mustFeed(t, s, u.ID, "https://example.org/second")
		if err := s.InsertEntries(ctx, u.ID, []*Entry{{FeedID: first.ID, GUID: "a"}}); err != nil {
			t.Fatalf("InsertEntries: %v", err)
		}

		// The batch fails as a whole: its first entry must not stay.
		err := s.InsertEntries(ctx, u.ID, []*Entry{{FeedID: first.ID, GUID: "b"}, {FeedID: first.ID, GUID: "a"}})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("InsertEntries with a guid already in the feed: error = %v, want ErrConflict", err)
		}
		if n, err := s.CountEntries(ctx, u.ID); err != nil || n != 1 {
			t.Errorf("entries after a failed batch = %d (err %v), want 1", n, err)
		}

		if err := s.InsertEntries(ctx, u.ID, []*Entry{{FeedID: second.ID, GUID: "a"}}); err != nil {
			t.Errorf("same guid in another feed: %v", err)
		}
		bob := mustUser(t, s, "bob")
		bobFeed := mustFeed(t, s, bob.ID, "https://example.org/first")
		if err := s.InsertEntries(ctx, bob.ID, []*Entry{{FeedID: bobFeed.ID, GUID: "a"}}); err != nil {
			t.Errorf("same guid and feed id for another user: %v", err)
		}
	})
}

func TestEntryIDs(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")
		clock := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return clock }

		insert := func(n int) []int64 {
			t.Helper()
			entries := make([]*Entry, n)
			for i := range entries {
				entries[i] = &Entry{FeedID: feed.ID, GUID: fmt.Sprintf("%d-%d", clock.UnixNano(), time.Now().UnixNano()+int64(i))}
			}
			if err := s.InsertEntries(ctx, u.ID, entries); err != nil {
				t.Fatalf("InsertEntries: %v", err)
			}
			ids := make([]int64, n)
			for i, e := range entries {
				ids[i] = e.ID
			}
			return ids
		}

		now := clock.UnixMicro()
		if got, want := insert(3), []int64{now, now + 1, now + 2}; !reflect.DeepEqual(got, want) {
			t.Errorf("first batch ids = %v, want the current microsecond onwards %v", got, want)
		}
		// The clock did not move: the next batch continues after the previous one.
		if got, want := insert(2), []int64{now + 3, now + 4}; !reflect.DeepEqual(got, want) {
			t.Errorf("ids with a stopped clock = %v, want %v", got, want)
		}
		// The clock went back: ids still grow.
		clock = clock.Add(-time.Hour)
		if got, want := insert(1), []int64{now + 5}; !reflect.DeepEqual(got, want) {
			t.Errorf("ids after the clock went back = %v, want %v", got, want)
		}
		// The clock moved forward: ids follow the time again.
		clock = clock.Add(2 * time.Hour)
		if got, want := insert(1), []int64{clock.UnixMicro()}; !reflect.DeepEqual(got, want) {
			t.Errorf("ids after the clock moved on = %v, want %v", got, want)
		}

		// An imported entry keeps its id, even one from the future, and new
		// entries come after it.
		future := clock.Add(24 * time.Hour).UnixMicro()
		imported := &Entry{ID: future, FeedID: feed.ID, GUID: "imported"}
		mixed := []*Entry{imported, {FeedID: feed.ID, GUID: "fresh"}}
		if err := s.InsertEntries(ctx, u.ID, mixed); err != nil {
			t.Fatalf("InsertEntries with an explicit id: %v", err)
		}
		if imported.ID != future {
			t.Errorf("explicit entry id became %d, want %d", imported.ID, future)
		}
		if mixed[1].ID != future+1 {
			t.Errorf("id after an explicit one = %d, want %d", mixed[1].ID, future+1)
		}

		// Another user has a counter of their own.
		bob := mustUser(t, s, "bob")
		bobFeed := mustFeed(t, s, bob.ID, "https://example.org/feed")
		bobEntry := &Entry{FeedID: bobFeed.ID, GUID: "x"}
		if err := s.InsertEntries(ctx, bob.ID, []*Entry{bobEntry}); err != nil {
			t.Fatal(err)
		}
		if bobEntry.ID != clock.UnixMicro() {
			t.Errorf("first id of another user = %d, want the current time %d", bobEntry.ID, clock.UnixMicro())
		}
	})
}

func TestEntryIDsConcurrent(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")
		started := time.Now().UnixMicro()

		const writers, batches, batchSize = 8, 15, 4
		var (
			mu  sync.Mutex
			all []int64
			wg  sync.WaitGroup
		)
		errs := make(chan error, writers)
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var previous int64
				for b := range batches {
					entries := make([]*Entry, batchSize)
					for i := range entries {
						entries[i] = &Entry{FeedID: feed.ID, GUID: fmt.Sprintf("w%d-b%d-%d", w, b, i)}
					}
					if err := s.InsertEntries(ctx, u.ID, entries); err != nil {
						errs <- fmt.Errorf("writer %d: %w", w, err)
						return
					}
					for i, e := range entries {
						if i > 0 && e.ID != entries[i-1].ID+1 {
							errs <- fmt.Errorf("writer %d batch %d: ids %d, %d are not consecutive", w, b, entries[i-1].ID, e.ID)
							return
						}
					}
					// A batch written after another one committed comes after it.
					if entries[0].ID <= previous {
						errs <- fmt.Errorf("writer %d batch %d: id %d is not above the previous batch %d", w, b, entries[0].ID, previous)
						return
					}
					previous = entries[batchSize-1].ID
					mu.Lock()
					for _, e := range entries {
						all = append(all, e.ID)
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if t.Failed() {
			return
		}

		if want := writers * batches * batchSize; len(all) != want {
			t.Fatalf("got %d ids, want %d", len(all), want)
		}
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		for i := 1; i < len(all); i++ {
			if all[i] == all[i-1] {
				t.Fatalf("id %d issued twice", all[i])
			}
		}
		if all[0] < started {
			t.Errorf("smallest id %d is before the test started (%d)", all[0], started)
		}
		if n, err := s.CountEntries(ctx, u.ID); err != nil || n != len(all) {
			t.Errorf("CountEntries = %d (err %v), want %d", n, err, len(all))
		}
	})
}

func TestTags(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")
		entry := &Entry{FeedID: feed.ID, GUID: "a"}
		if err := s.InsertEntries(ctx, u.ID, []*Entry{entry}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCategory(ctx, &Category{UserID: u.ID, Name: "News"}); err != nil {
			t.Fatal(err)
		}

		later := &Tag{UserID: u.ID, Name: "later", Attributes: json.RawMessage(`{"filters":["intitle:go"]}`)}
		work := &Tag{UserID: u.ID, Name: "work"}
		for _, tag := range []*Tag{later, work} {
			if err := s.CreateTag(ctx, tag); err != nil {
				t.Fatalf("CreateTag(%q): %v", tag.Name, err)
			}
		}
		if err := s.CreateTag(ctx, &Tag{UserID: u.ID, Name: "later"}); !errors.Is(err, ErrConflict) {
			t.Errorf("CreateTag with a taken name: error = %v, want ErrConflict", err)
		}
		err := s.CreateTag(ctx, &Tag{UserID: u.ID, Name: "News"})
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "category") {
			t.Errorf("CreateTag with the name of a category: error = %v, want ErrConflict naming the category", err)
		}

		tags, err := s.Tags(ctx, u.ID)
		if err != nil {
			t.Fatalf("Tags: %v", err)
		}
		work.Attributes = json.RawMessage(`{}`)
		if want := []*Tag{later, work}; !reflect.DeepEqual(tags, want) {
			t.Errorf("Tags = %+v, want %+v", tags, want)
		}

		for range 2 { // attaching twice is allowed
			if err := s.TagEntry(ctx, u.ID, work.ID, entry.ID); err != nil {
				t.Fatalf("TagEntry: %v", err)
			}
		}
		if err := s.TagEntry(ctx, u.ID, later.ID, entry.ID); err != nil {
			t.Fatalf("TagEntry: %v", err)
		}
		ids, err := s.EntryTagIDs(ctx, u.ID, entry.ID)
		if err != nil {
			t.Fatalf("EntryTagIDs: %v", err)
		}
		if want := []int64{later.ID, work.ID}; !reflect.DeepEqual(ids, want) {
			t.Errorf("EntryTagIDs = %v, want %v", ids, want)
		}
		if err := s.TagEntry(ctx, u.ID, work.ID, entry.ID+1); err == nil {
			t.Error("TagEntry on a missing entry: no error")
		}
	})
}

func TestInTxRollsBack(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		boom := errors.New("boom")
		err := s.InTx(ctx, func(tx *Store) error {
			if err := tx.CreateUser(ctx, &User{Name: "alice"}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx error = %v, want the error of the callback", err)
		}
		if _, err := s.UserByName(ctx, "alice"); !errors.Is(err, ErrNotFound) {
			t.Errorf("user created in a rolled back transaction: error = %v, want ErrNotFound", err)
		}

		err = s.InTx(ctx, func(tx *Store) error {
			return tx.CreateUser(ctx, &User{Name: "bob"})
		})
		if err != nil {
			t.Fatalf("InTx: %v", err)
		}
		if _, err := s.UserByName(ctx, "bob"); err != nil {
			t.Errorf("user created in a committed transaction: %v", err)
		}
	})
}

func TestSettings(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		if _, err := s.Setting(ctx, SettingSalt); !errors.Is(err, ErrNotFound) {
			t.Errorf("Setting(unset) error = %v, want ErrNotFound", err)
		}
		for _, value := range []string{"first", "second"} {
			if err := s.SetSetting(ctx, SettingSalt, value); err != nil {
				t.Fatalf("SetSetting: %v", err)
			}
			if got, err := s.Setting(ctx, SettingSalt); err != nil || got != value {
				t.Errorf("Setting = %q, %v; want %q", got, err, value)
			}
		}
	})
}

func TestCustomIcon(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")
		if _, err := s.CustomIconByHash(ctx, "0a"); !errors.Is(err, ErrNotFound) {
			t.Errorf("CustomIconByHash(unset) error = %v, want ErrNotFound", err)
		}
		for i, content := range [][]byte{{0x89, 'P', 'N', 'G', 0x00}, {0x00, 0x01}} {
			want := CustomIcon{Hash: "0a", Content: content, Modified: int64(100 + i)}
			if err := s.SetCustomIcon(ctx, u.ID, feed.ID, want); err != nil {
				t.Fatalf("SetCustomIcon: %v", err)
			}
			got, err := s.CustomIconByHash(ctx, "0a")
			if err != nil || !reflect.DeepEqual(got, &want) {
				t.Errorf("CustomIconByHash = %+v, %v; want %+v", got, err, want)
			}
		}
		if err := s.SetCustomIcon(ctx, u.ID, feed.ID+1, CustomIcon{Hash: "0b", Content: []byte{1}}); err == nil {
			t.Error("SetCustomIcon for a missing feed: no error")
		}
		if _, err := s.CustomIconByHash(ctx, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("CustomIconByHash(empty hash) error = %v, want ErrNotFound", err)
		}
		if err := s.DeleteFeed(ctx, u.ID, feed.ID); err != nil {
			t.Fatalf("DeleteFeed: %v", err)
		}
		if _, err := s.CustomIconByHash(ctx, "0a"); !errors.Is(err, ErrNotFound) {
			t.Errorf("CustomIconByHash after the feed is deleted: error = %v, want ErrNotFound", err)
		}
	})
}

func TestIcons(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		if _, err := s.Icon(ctx, "aa"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Icon(unset) error = %v, want ErrNotFound", err)
		}
		for _, want := range []*Icon{
			{Hash: "aa", Source: "https://example.org/", Checked: 5},
			{Hash: "aa", Source: "https://example.org/", Content: []byte{0, 1, 2}, ContentType: "image/png", Modified: 7, Checked: 7},
			{Hash: "aa", Source: "https://example.org/new", Checked: 9},
		} {
			if err := s.PutIcon(ctx, want); err != nil {
				t.Fatalf("PutIcon: %v", err)
			}
			got, err := s.Icon(ctx, "aa")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Icon = %+v, %v; want %+v", got, err, want)
			}
		}
	})
}

func TestUpdateCategory(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		want := &Category{UserID: u.ID, ID: DefaultCategoryID, Name: "Без категории", Kind: 1,
			LastUpdate: 10, Error: 20, Attributes: json.RawMessage(`{"position":3}`)}
		if err := s.UpdateCategory(ctx, want); err != nil {
			t.Fatalf("UpdateCategory: %v", err)
		}
		categories, err := s.Categories(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(categories) != 1 || !reflect.DeepEqual(categories[0], want) {
			t.Errorf("Categories = %+v, want only %+v", categories, want)
		}
		if err := s.UpdateCategory(ctx, &Category{UserID: u.ID, ID: 99, Name: "x"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateCategory(missing) error = %v, want ErrNotFound", err)
		}
	})
}

func TestRaiseCounters(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		if err := s.RaiseCounters(ctx, u.ID, Counters{Category: 5, Feed: 9}); err != nil {
			t.Fatalf("RaiseCounters: %v", err)
		}
		c := &Category{UserID: u.ID, Name: "News"}
		if err := s.CreateCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.ID != 6 {
			t.Errorf("category id after counter 5 = %d, want 6", c.ID)
		}
		if f := mustFeed(t, s, u.ID, "https://example.org/a"); f.ID != 10 {
			t.Errorf("feed id after counter 9 = %d, want 10", f.ID)
		}
		tag := &Tag{UserID: u.ID, Name: "later"}
		if err := s.CreateTag(ctx, tag); err != nil {
			t.Fatal(err)
		}
		if tag.ID != 1 {
			t.Errorf("tag id with an untouched counter = %d, want 1", tag.ID)
		}
		// A smaller value does not move a counter back.
		if err := s.RaiseCounters(ctx, u.ID, Counters{Feed: 2}); err != nil {
			t.Fatal(err)
		}
		if f := mustFeed(t, s, u.ID, "https://example.org/b"); f.ID != 11 {
			t.Errorf("feed id after lowering the counter = %d, want 11", f.ID)
		}
	})
}

func TestUpdateFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		category := &Category{UserID: u.ID, Name: "News"}
		if err := s.CreateCategory(ctx, category); err != nil {
			t.Fatal(err)
		}
		f := mustFeed(t, s, u.ID, "https://example.org/feed")
		untouched := mustFeed(t, s, u.ID, "https://example.org/other")

		want := &Feed{
			UserID: u.ID, ID: f.ID, URL: "https://example.org/moved", Kind: 15, CategoryID: category.ID,
			Name: "Новое имя", Website: "https://example.org/", Description: "d", LastUpdate: 1700000100,
			Priority: 20, PathEntries: "article", HTTPAuth: "u:p", Error: 1700000200, TTL: -900,
			Attributes: json.RawMessage(`{"unicityCriteria":"sha1:link_published"}`),
			HTTPETag:   `"v2"`, HTTPLastModified: "Tue, 06 Oct 2026 11:00:00 GMT",
		}
		if err := s.UpdateFeed(ctx, want); err != nil {
			t.Fatalf("UpdateFeed: %v", err)
		}
		got, err := s.FeedByID(ctx, u.ID, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("after UpdateFeed:\n got %+v\nwant %+v", got, want)
		}
		if other, err := s.FeedByID(ctx, u.ID, untouched.ID); err != nil || other.URL != untouched.URL || other.Name != untouched.Name {
			t.Errorf("another feed changed: %+v, %v", other, err)
		}

		locked, err := s.LockFeed(ctx, u.ID, f.ID)
		if err != nil || !reflect.DeepEqual(locked, want) {
			t.Errorf("LockFeed = %+v, %v; want the stored feed", locked, err)
		}
		if _, err := s.LockFeed(ctx, u.ID, 999); !errors.Is(err, ErrNotFound) {
			t.Errorf("LockFeed(unknown) error = %v, want ErrNotFound", err)
		}
		missing := *want
		missing.ID = 999
		if err := s.UpdateFeed(ctx, &missing); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateFeed(unknown) error = %v, want ErrNotFound", err)
		}
	})
}

// Two transactions that lock the same feed run one after the other: the
// second sees what the first wrote.
func TestLockFeedSerializes(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		f := mustFeed(t, s, u.ID, "https://example.org/feed")

		const writers = 8
		var wg sync.WaitGroup
		errs := make(chan error, writers)
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- s.InTx(ctx, func(tx *Store) error {
					locked, err := tx.LockFeed(ctx, u.ID, f.ID)
					if err != nil {
						return err
					}
					locked.LastUpdate++
					return tx.UpdateFeed(ctx, locked)
				})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.FeedByID(ctx, u.ID, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastUpdate != writers {
			t.Errorf("LastUpdate = %d after %d locked increments", got.LastUpdate, writers)
		}
	})
}

func TestUpdateEntry(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")
		e := &Entry{FeedID: feed.ID, GUID: "one", Title: "old", IsFavorite: true, LastSeen: 100}
		neighbour := &Entry{FeedID: feed.ID, GUID: "two", Title: "neighbour"}
		if err := s.InsertEntries(ctx, u.ID, []*Entry{e, neighbour}); err != nil {
			t.Fatal(err)
		}

		want := &Entry{
			UserID: u.ID, ID: e.ID, FeedID: feed.ID, GUID: "one", Title: "new <title>", Authors: []string{"A", "B"},
			Content: "<p>новый</p>", Link: "https://example.org/1", Published: 1700000000, LastSeen: 200,
			LastModified: 200, LastUserModified: 150, Hash: []byte{1, 2, 3}, IsRead: true, IsFavorite: false,
			Tags: []string{"t"}, Attributes: json.RawMessage(`{"enclosures":[]}`),
		}
		if err := s.UpdateEntry(ctx, want); err != nil {
			t.Fatalf("UpdateEntry: %v", err)
		}
		got, err := s.EntryByID(ctx, u.ID, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("after UpdateEntry:\n got %+v\nwant %+v", got, want)
		}
		if other, err := s.EntryByID(ctx, u.ID, neighbour.ID); err != nil || other.Title != "neighbour" {
			t.Errorf("another entry changed: %+v, %v", other, err)
		}

		missing := *want
		missing.ID = 1
		if err := s.UpdateEntry(ctx, &missing); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateEntry(unknown) error = %v, want ErrNotFound", err)
		}

		if err := s.SetEntryHash(ctx, u.ID, neighbour.ID, []byte{9}); err != nil {
			t.Fatal(err)
		}
		if other, _ := s.EntryByID(ctx, u.ID, neighbour.ID); !reflect.DeepEqual(other.Hash, []byte{9}) || other.Title != "neighbour" {
			t.Errorf("after SetEntryHash: %+v", other)
		}
	})
}

func TestEntryStatesAndLastSeen(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		feed := mustFeed(t, s, u.ID, "https://example.org/feed")
		other := mustFeed(t, s, u.ID, "https://example.org/other")

		// More entries than fit in one query.
		var entries []*Entry
		var guids []string
		for i := range guidChunk + 20 {
			guid := fmt.Sprintf("guid-%d", i)
			guids = append(guids, guid)
			entries = append(entries, &Entry{FeedID: feed.ID, GUID: guid, LastSeen: 100})
		}
		entries[0].Hash, entries[0].IsRead, entries[0].LastUserModified = []byte{7}, true, 50
		entries[1].IsFavorite = true
		foreign := &Entry{FeedID: other.ID, GUID: "guid-0", LastSeen: 100}
		if err := s.InsertEntries(ctx, u.ID, append(entries, foreign)); err != nil {
			t.Fatal(err)
		}

		states, err := s.EntryStates(ctx, u.ID, feed.ID, append(guids, "absent"))
		if err != nil {
			t.Fatalf("EntryStates: %v", err)
		}
		if len(states) != len(guids) {
			t.Fatalf("EntryStates returned %d entries, want %d", len(states), len(guids))
		}
		if want := (EntryState{ID: entries[0].ID, Hash: []byte{7}, IsRead: true, LastUserModified: 50}); !reflect.DeepEqual(states["guid-0"], want) {
			t.Errorf("state of guid-0 = %+v, want %+v", states["guid-0"], want)
		}
		if st := states["guid-1"]; st.ID != entries[1].ID || st.Hash != nil || !st.IsFavorite || st.IsRead {
			t.Errorf("state of guid-1 = %+v", st)
		}

		seen := guids[10:]
		if err := s.MarkEntriesSeen(ctx, u.ID, feed.ID, seen, 200); err != nil {
			t.Fatalf("MarkEntriesSeen: %v", err)
		}
		lastSeen := func(id int64) int64 {
			t.Helper()
			e, err := s.EntryByID(ctx, u.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			return e.LastSeen
		}
		if lastSeen(entries[9].ID) != 100 || lastSeen(entries[10].ID) != 200 || lastSeen(entries[len(entries)-1].ID) != 200 {
			t.Error("MarkEntriesSeen did not touch exactly the listed entries")
		}
		if lastSeen(foreign.ID) != 100 {
			t.Error("MarkEntriesSeen touched an entry of another feed")
		}

		if err := s.MarkEntriesSeenSince(ctx, u.ID, feed.ID, 200, 300); err != nil {
			t.Fatalf("MarkEntriesSeenSince: %v", err)
		}
		if lastSeen(entries[9].ID) != 100 || lastSeen(entries[10].ID) != 300 || lastSeen(foreign.ID) != 100 {
			t.Error("MarkEntriesSeenSince did not touch exactly the entries seen since the given time")
		}
	})
}

// The rules themselves are compared with FreshRSS in internal/refresh; here
// it is the reach of the statements: one feed of one user.
func TestCleanupTouchesOneFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		feed := mustFeed(t, s, alice.ID, "https://example.org/feed")
		other := mustFeed(t, s, alice.ID, "https://example.org/other")
		foreign := mustFeed(t, s, bob.ID, "https://example.org/feed")
		tag := &Tag{UserID: alice.ID, Name: "later"}
		if err := s.CreateTag(ctx, tag); err != nil {
			t.Fatal(err)
		}

		// In every feed: one entry listed now, the rest listed long ago.
		fill := func(f *Feed) []*Entry {
			t.Helper()
			entries := []*Entry{
				{FeedID: f.ID, GUID: "old-unread", LastSeen: 100},
				{FeedID: f.ID, GUID: "old-read", LastSeen: 100, IsRead: true},
				{FeedID: f.ID, GUID: "old-starred", LastSeen: 100, IsRead: true, IsFavorite: true},
				{FeedID: f.ID, GUID: "old-labelled", LastSeen: 100, IsRead: true},
				{FeedID: f.ID, GUID: "older", LastSeen: 50, IsRead: true},
				{FeedID: f.ID, GUID: "listed", LastSeen: 1000},
			}
			if err := s.InsertEntries(ctx, f.UserID, entries); err != nil {
				t.Fatal(err)
			}
			return entries
		}
		mine := fill(feed)
		fill(other)
		fill(foreign)
		if err := s.TagEntry(ctx, alice.ID, tag.ID, mine[3].ID); err != nil {
			t.Fatal(err)
		}
		guids := func(f *Feed) []string {
			t.Helper()
			entries, err := s.EntriesByFeed(ctx, f.UserID, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, e := range entries {
				out = append(out, e.GUID)
			}
			return out
		}
		all := guids(feed)

		if n, err := s.DeleteOldEntries(ctx, alice.ID, feed.ID, Retention{KeepMin: 3}); err != nil || n != 0 {
			t.Errorf("DeleteOldEntries without a rule = %d, %v; want nothing deleted", n, err)
		}
		n, err := s.DeleteOldEntries(ctx, alice.ID, feed.ID, Retention{
			SeenBefore: 500, KeepFavorites: true, KeepLabeled: true, KeepUnread: true,
		})
		if err != nil || n != 2 {
			t.Fatalf("DeleteOldEntries = %d, %v; want 2", n, err)
		}
		if got, want := guids(feed), []string{"old-unread", "old-starred", "old-labelled", "listed"}; !reflect.DeepEqual(got, want) {
			t.Errorf("entries left: %v, want %v", got, want)
		}
		// The entry at the offset shares its time with others: all of them go,
		// except what was listed at the last refresh.
		if n, err := s.DeleteOldEntries(ctx, alice.ID, feed.ID, Retention{KeepMax: 2}); err != nil || n != 3 {
			t.Errorf("DeleteOldEntries by number = %d, %v; want 3", n, err)
		}
		if got, want := guids(feed), []string{"listed"}; !reflect.DeepEqual(got, want) {
			t.Errorf("entries left: %v, want %v", got, want)
		}
		if got := guids(other); !reflect.DeepEqual(got, all) {
			t.Errorf("another feed of the user lost entries: %v", got)
		}
		if got := guids(foreign); !reflect.DeepEqual(got, all) {
			t.Errorf("a feed of another user lost entries: %v", got)
		}
	})
}

func TestAutoReadStatements(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		news := &Category{UserID: alice.ID, Name: "News"}
		if err := s.CreateCategory(ctx, news); err != nil {
			t.Fatal(err)
		}
		feed := mustFeed(t, s, alice.ID, "https://example.org/feed")
		other := &Feed{UserID: alice.ID, URL: "https://example.org/other", Name: "other", CategoryID: news.ID}
		if err := s.CreateFeed(ctx, other); err != nil {
			t.Fatal(err)
		}
		foreign := mustFeed(t, s, bob.ID, "https://example.org/feed")
		var mine []*Entry
		for i := range 6 {
			mine = append(mine, &Entry{FeedID: feed.ID, GUID: fmt.Sprintf("g%d", i), Title: fmt.Sprintf("Title %d", i), LastSeen: int64(100 * (i + 1))})
		}
		mine[4].IsRead = true
		rest := []*Entry{
			{FeedID: other.ID, GUID: "o0", Title: "Other 0", LastSeen: 100},
			{FeedID: other.ID, GUID: "o1", Title: "Other 1", LastSeen: 100},
		}
		theirs := []*Entry{{FeedID: foreign.ID, GUID: "b0", Title: "Bob 0", LastSeen: 100}}
		if err := s.InsertEntries(ctx, alice.ID, append(mine, rest...)); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertEntries(ctx, bob.ID, theirs); err != nil {
			t.Fatal(err)
		}
		unread := func(userID int64, f *Feed) []string {
			t.Helper()
			entries, err := s.EntriesByFeed(ctx, userID, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, e := range entries {
				if !e.IsRead {
					out = append(out, e.GUID)
				}
			}
			return out
		}

		keys, err := s.LatestFeedEntries(ctx, alice.ID, feed.ID, 2)
		if want := []EntryKey{{"g5", "Title 5"}, {"g4", "Title 4"}}; err != nil || !reflect.DeepEqual(keys, want) {
			t.Errorf("LatestFeedEntries(2) = %v, %v; want %v", keys, err, want)
		}
		if keys, err := s.LatestFeedEntries(ctx, alice.ID, feed.ID, 0); err != nil || len(keys) != 6 {
			t.Errorf("LatestFeedEntries(0) = %d entries, %v; want all 6", len(keys), err)
		}
		keys, err = s.LatestCategoryEntries(ctx, alice.ID, news.ID, 0)
		if want := []EntryKey{{"o1", "Other 1"}, {"o0", "Other 0"}}; err != nil || !reflect.DeepEqual(keys, want) {
			t.Errorf("LatestCategoryEntries = %v, %v; want %v", keys, err, want)
		}
		if keys, err := s.LatestCategoryEntries(ctx, alice.ID, DefaultCategoryID, 3); err != nil || len(keys) != 3 || keys[0].GUID != "g5" {
			t.Errorf("LatestCategoryEntries of the default category = %v, %v", keys, err)
		}

		// g0 and g1 were last listed before 250.
		if n, err := s.MarkUnseenEntriesRead(ctx, alice.ID, feed.ID, 250); err != nil || n != 2 {
			t.Errorf("MarkUnseenEntriesRead = %d, %v; want 2", n, err)
		}
		if got, want := unread(alice.ID, feed), []string{"g2", "g3", "g5"}; !reflect.DeepEqual(got, want) {
			t.Errorf("unread: %v, want %v", got, want)
		}
		if n, err := s.KeepNewestUnread(ctx, alice.ID, feed.ID, 3); err != nil || n != 0 {
			t.Errorf("KeepNewestUnread within the limit = %d, %v; want 0", n, err)
		}
		if n, err := s.KeepNewestUnread(ctx, alice.ID, feed.ID, 1); err != nil || n != 2 {
			t.Errorf("KeepNewestUnread = %d, %v; want 2", n, err)
		}
		if got, want := unread(alice.ID, feed), []string{"g5"}; !reflect.DeepEqual(got, want) {
			t.Errorf("unread: %v, want %v", got, want)
		}
		if n, err := s.KeepNewestUnread(ctx, alice.ID, feed.ID, 0); err != nil || n != 1 {
			t.Errorf("KeepNewestUnread(0) = %d, %v; want 1", n, err)
		}
		if got := unread(alice.ID, other); len(got) != 2 {
			t.Errorf("another feed of the user: unread %v", got)
		}
		if got := unread(bob.ID, foreign); len(got) != 1 {
			t.Errorf("a feed of another user: unread %v", got)
		}
	})
}

func TestSystem(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		got, err := s.System(ctx)
		if err != nil || !reflect.DeepEqual(got, DefaultSystem()) {
			t.Fatalf("System of a new database = %+v, %v; want the defaults", got, err)
		}
		changed := DefaultSystem()
		changed.Title, changed.DefaultUser, changed.AuthType = "Новости", "alice", AuthNone
		changed.AllowAnonymous, changed.APIEnabled = true, false
		changed.Limits.MaxRegistrations = 0
		if err := s.SetSystem(ctx, changed); err != nil {
			t.Fatalf("SetSystem: %v", err)
		}
		if got, err := s.System(ctx); err != nil || !reflect.DeepEqual(got, changed) {
			t.Errorf("System = %+v, %v; want %+v", got, err, changed)
		}
		// A setting added by a later version is not in what an earlier one
		// stored: it has its default.
		if err := s.SetSetting(ctx, SettingSystem, `{"title":"Old"}`); err != nil {
			t.Fatal(err)
		}
		want := DefaultSystem()
		want.Title = "Old"
		if got, err := s.System(ctx); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("System over a partial value = %+v, %v; want %+v", got, err, want)
		}
	})
}

func TestSessions(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		for _, session := range []*Session{
			{TokenHash: "a1", UserID: alice.ID, Created: 100, Used: 100, Expires: 200, Persistent: true, Authenticated: 100},
			{TokenHash: "a2", UserID: alice.ID, Created: 110, Used: 110, Expires: 300},
			{TokenHash: "b1", UserID: bob.ID, Created: 120, Used: 120, Expires: 150},
		} {
			if err := s.CreateSession(ctx, session); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
		}
		want := &Session{TokenHash: "a1", UserID: alice.ID, Created: 100, Used: 100, Expires: 200, Persistent: true, Authenticated: 100}
		if got, err := s.Session(ctx, "a1", 199); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Session = %+v, %v; want %+v", got, err, want)
		}
		// A session ends at the second it expires, and an unknown one never began.
		if _, err := s.Session(ctx, "a1", 200); !errors.Is(err, ErrNotFound) {
			t.Errorf("Session at its expiry: error = %v, want ErrNotFound", err)
		}
		if _, err := s.Session(ctx, "zz", 100); !errors.Is(err, ErrNotFound) {
			t.Errorf("Session(unknown): error = %v, want ErrNotFound", err)
		}
		if err := s.CreateSession(ctx, &Session{TokenHash: "a1", UserID: bob.ID}); err == nil {
			t.Error("CreateSession with a token hash in use: no error")
		}

		if err := s.TouchSession(ctx, "a1", 190, 500); err != nil {
			t.Fatalf("TouchSession: %v", err)
		}
		if got, err := s.Session(ctx, "a1", 400); err != nil || got.Used != 190 || got.Expires != 500 {
			t.Errorf("Session after TouchSession = %+v, %v", got, err)
		}

		if err := s.DeleteExpiredSessions(ctx, 150); err != nil {
			t.Fatalf("DeleteExpiredSessions: %v", err)
		}
		if _, err := s.Session(ctx, "b1", 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("an expired session survived DeleteExpiredSessions: %v", err)
		}
		if _, err := s.Session(ctx, "a2", 0); err != nil {
			t.Errorf("a live session did not survive DeleteExpiredSessions: %v", err)
		}

		if err := s.CreateSession(ctx, &Session{TokenHash: "b2", UserID: bob.ID, Expires: 900}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUserSessions(ctx, alice.ID, "a2"); err != nil {
			t.Fatalf("DeleteUserSessions: %v", err)
		}
		for hash, alive := range map[string]bool{"a1": false, "a2": true, "b2": true} {
			if _, err := s.Session(ctx, hash, 0); (err == nil) != alive {
				t.Errorf("after DeleteUserSessions session %s: error %v, want alive = %v", hash, err, alive)
			}
		}
		if err := s.DeleteSession(ctx, "a2"); err != nil {
			t.Fatalf("DeleteSession: %v", err)
		}
		if err := s.DeleteSession(ctx, "a2"); err != nil {
			t.Errorf("DeleteSession twice: %v", err)
		}
		// The sessions of a user go with the user.
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Session(ctx, "b2", 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("a session outlived its user: %v", err)
		}
	})
}

func TestUpdateUserSettings(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice := &User{Name: "alice", Settings: json.RawMessage(`{"language":"ru","queries":[{"name":"a"}]}`)}
		if err := s.CreateUser(ctx, alice); err != nil {
			t.Fatal(err)
		}
		bob := mustUser(t, s, "bob")
		err := s.UpdateUserSettings(ctx, alice.ID, func(settings map[string]json.RawMessage) error {
			if string(settings["language"]) != `"ru"` {
				t.Errorf("settings handed to the change = %s", settings)
			}
			settings["language"] = json.RawMessage(`"en"`)
			settings["darkMode"] = json.RawMessage(`"dark"`)
			return nil
		})
		if err != nil {
			t.Fatalf("UpdateUserSettings: %v", err)
		}
		got, err := s.UserByID(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		var settings map[string]any
		if err := json.Unmarshal(got.Settings, &settings); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"language": "en", "darkMode": "dark", "queries": []any{map[string]any{"name": "a"}}}
		if !reflect.DeepEqual(settings, want) {
			t.Errorf("settings = %v, want %v", settings, want)
		}
		if other, err := s.UserByID(ctx, bob.ID); err != nil || string(other.Settings) != "{}" {
			t.Errorf("settings of the other user = %s, %v", other.Settings, err)
		}

		// A change that fails changes nothing.
		failure := errors.New("no")
		err = s.UpdateUserSettings(ctx, alice.ID, func(settings map[string]json.RawMessage) error {
			settings["language"] = json.RawMessage(`"de"`)
			return failure
		})
		if !errors.Is(err, failure) {
			t.Errorf("UpdateUserSettings with a failing change: error = %v", err)
		}
		if again, _ := s.UserByID(ctx, alice.ID); !reflect.DeepEqual(again.Settings, got.Settings) {
			t.Errorf("settings after a failed change = %s", again.Settings)
		}
		if err := s.UpdateUserSettings(ctx, bob.ID+100, func(map[string]json.RawMessage) error { return nil }); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateUserSettings(unknown user) error = %v, want ErrNotFound", err)
		}
		if _, err := s.UserByID(ctx, bob.ID+100); !errors.Is(err, ErrNotFound) {
			t.Errorf("UserByID(unknown) error = %v, want ErrNotFound", err)
		}

		// Changes made at once are all kept.
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				err := s.UpdateUserSettings(ctx, bob.ID, func(settings map[string]json.RawMessage) error {
					settings[fmt.Sprintf("key%d", i)] = json.RawMessage("true")
					return nil
				})
				if err != nil {
					t.Errorf("concurrent UpdateUserSettings: %v", err)
				}
			})
		}
		wg.Wait()
		final, err := s.UserByID(ctx, bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]bool
		if err := json.Unmarshal(final.Settings, &keys); err != nil || len(keys) != 8 {
			t.Errorf("settings after 8 concurrent changes = %s, %v; want 8 keys", final.Settings, err)
		}
	})
}

// The first user of an installation is its default user; later ones change
// nothing about that.
func TestFirstUserIsTheDefault(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		mustUser(t, s, "alice")
		mustUser(t, s, "bob")
		if system, err := s.System(ctx); err != nil || system.DefaultUser != "alice" {
			t.Errorf("System = %+v, %v; want alice as the default user", system, err)
		}
		// The rest of the settings is what it was.
		want := DefaultSystem()
		want.DefaultUser = "alice"
		if system, _ := s.System(ctx); !reflect.DeepEqual(system, want) {
			t.Errorf("System = %+v, want %+v", system, want)
		}
	})
}
