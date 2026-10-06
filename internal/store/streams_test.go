package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// library is two users with feeds of every priority, entries in every state
// and labels, the same for both users except for the titles.
type library struct {
	alice, bob *User
}

// Entry identifiers of the library: seconds 1000 to 1006 in microseconds.
const (
	e1 int64 = 1000_000_000 + iota*1_000_000
	e2
	e3
	e4
	e5
	e6
	e7
)

func newLibrary(t *testing.T, s *Store) library {
	t.Helper()
	ctx := context.Background()
	lib := library{alice: mustUser(t, s, "alice"), bob: mustUser(t, s, "bob")}
	for _, u := range []*User{lib.alice, lib.bob} {
		news := &Category{UserID: u.ID, ID: 2, Name: "News"}
		if err := s.CreateCategory(ctx, news); err != nil {
			t.Fatal(err)
		}
		// Feeds 1 to 4: important and main in News, shown in its category
		// only and hidden in the default category.
		for i, f := range []*Feed{
			{CategoryID: 2, Priority: 20}, {CategoryID: 2, Priority: 10}, {Priority: 0}, {Priority: -10},
		} {
			f.UserID, f.URL = u.ID, "https://example.org/"+string(rune('a'+i))
			if err := s.CreateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
		entries := []*Entry{
			{ID: e1, FeedID: 1, GUID: "1", IsRead: true},
			{ID: e2, FeedID: 1, GUID: "2", IsFavorite: true},
			{ID: e3, FeedID: 2, GUID: "3", IsRead: true, IsFavorite: true, LastModified: 2000},
			{ID: e4, FeedID: 2, GUID: "4"},
			{ID: e5, FeedID: 3, GUID: "5"},
			{ID: e6, FeedID: 4, GUID: "6", IsFavorite: true},
			{ID: e7, FeedID: 3, GUID: "7", IsRead: true},
		}
		if err := s.InsertEntries(ctx, u.ID, entries); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"later", "work"} {
			if err := s.CreateTag(ctx, &Tag{UserID: u.ID, Name: name}); err != nil {
				t.Fatal(err)
			}
		}
		// Label 1 on entries 2 and 3, label 2 on entry 3; in this order.
		for _, pair := range [][2]int64{{2, e3}, {1, e3}, {1, e2}} {
			if err := s.TagEntry(ctx, u.ID, pair[0], pair[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	return lib
}

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

func TestListEntries(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		for name, tc := range map[string]struct {
			q    EntryQuery
			want []int64
		}{
			"everything, newest first": {EntryQuery{}, []int64{e7, e6, e5, e4, e3, e2, e1}},
			"oldest first":             {EntryQuery{Ascending: true}, []int64{e1, e2, e3, e4, e5, e6, e7}},
			"a feed":                   {EntryQuery{Set: EntrySet{FeedID: 2}}, []int64{e4, e3}},
			"a category":               {EntryQuery{Set: EntrySet{CategoryID: 2}}, []int64{e4, e3, e2, e1}},
			"a category without its important feeds": {
				EntryQuery{Set: EntrySet{CategoryID: 2, BelowPriority: intPtr(20)}}, []int64{e4, e3}},
			"a label":                    {EntryQuery{Set: EntrySet{LabelID: 1}}, []int64{e3, e2}},
			"feeds shown somewhere":      {EntryQuery{Set: EntrySet{MinPriority: intPtr(0)}}, []int64{e7, e5, e4, e3, e2, e1}},
			"the main stream":            {EntryQuery{Set: EntrySet{MinPriority: intPtr(10)}}, []int64{e4, e3, e2, e1}},
			"starred outside the hidden": {EntryQuery{Set: EntrySet{MinPriority: intPtr(-9), OnlyFavorite: true}}, []int64{e3, e2}},
			"starred":                    {EntryQuery{Favorite: boolPtr(true)}, []int64{e6, e3, e2}},
			"not starred":                {EntryQuery{Favorite: boolPtr(false)}, []int64{e7, e5, e4, e1}},
			"read":                       {EntryQuery{Read: boolPtr(true)}, []int64{e7, e3, e1}},
			"unread of a feed":           {EntryQuery{Set: EntrySet{FeedID: 1}, Read: boolPtr(false)}, []int64{e2}},
			"a page":                     {EntryQuery{Limit: 2}, []int64{e7, e6}},
			"the next page":              {EntryQuery{From: e6, Limit: 3}, []int64{e6, e5, e4}},
			"the next page, oldest first": {
				EntryQuery{Ascending: true, From: e6, Limit: 3}, []int64{e6, e7}},
			// Added at second 1004 or later, or changed by the feed since.
			"since":           {EntryQuery{Since: 1004}, []int64{e7, e6, e5, e3}},
			"until":           {EntryQuery{Until: 1002}, []int64{e2, e1}},
			"since and until": {EntryQuery{Since: 1006, Until: 1001}, []int64{e7, e3, e2, e1}},
			"an unknown feed": {EntryQuery{Set: EntrySet{FeedID: 99}}, nil},
		} {
			entries, err := s.ListEntries(ctx, lib.alice.ID, tc.q)
			if err != nil {
				t.Fatalf("%s: ListEntries: %v", name, err)
			}
			var got []int64
			for _, e := range entries {
				got = append(got, e.ID)
				if e.UserID != lib.alice.ID || e.GUID == "" {
					t.Errorf("%s: entry %+v is not a whole entry of the user", name, e)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s: ListEntries = %v, want %v", name, got, tc.want)
			}
			ids, err := s.ListEntryIDs(ctx, lib.alice.ID, tc.q)
			if err != nil || !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("%s: ListEntryIDs = %v, %v; want %v", name, ids, err, tc.want)
			}
		}
	})
}

func TestEntriesByIDs(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		ids := func(entries []*Entry) []int64 {
			var out []int64
			for _, e := range entries {
				out = append(out, e.ID)
			}
			return out
		}
		got, err := s.EntriesByIDs(ctx, lib.alice.ID, []int64{e2, 12345, e5, e2, e1}, false)
		if err != nil || !reflect.DeepEqual(ids(got), []int64{e5, e2, e1}) {
			t.Errorf("EntriesByIDs = %v, %v; want the known ones once, newest first", ids(got), err)
		}
		got, err = s.EntriesByIDs(ctx, lib.alice.ID, []int64{e5, e1}, true)
		if err != nil || !reflect.DeepEqual(ids(got), []int64{e1, e5}) {
			t.Errorf("EntriesByIDs ascending = %v, %v", ids(got), err)
		}
		// More identifiers than one statement takes.
		many := make([]int64, 0, 1200)
		for i := range 1199 {
			many = append(many, int64(i+1))
		}
		many = append(many, e4)
		got, err = s.EntriesByIDs(ctx, lib.alice.ID, many, false)
		if err != nil || !reflect.DeepEqual(ids(got), []int64{e4}) {
			t.Errorf("EntriesByIDs of 1200 identifiers = %v, %v; want the one that exists", ids(got), err)
		}
		if got, err := s.EntriesByIDs(ctx, lib.alice.ID, nil, false); err != nil || got != nil {
			t.Errorf("EntriesByIDs of nothing = %v, %v", got, err)
		}

		labels, err := s.EntryLabels(ctx, lib.alice.ID, []int64{e1, e2, e3, 12345})
		want := map[int64][]string{e2: {"later"}, e3: {"later", "work"}}
		if err != nil || !reflect.DeepEqual(labels, want) {
			t.Errorf("EntryLabels = %v, %v; want %v", labels, err, want)
		}
	})
}

func TestMarkEntries(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		entry := func(u *User, id int64) *Entry {
			t.Helper()
			e, err := s.EntryByID(ctx, u.ID, id)
			if err != nil {
				t.Fatal(err)
			}
			return e
		}

		// Entry 1 is read already: only entry 2 changes and is stamped.
		n, err := s.SetEntriesRead(ctx, lib.alice.ID, []int64{e1, e2, 12345}, true, 77)
		if err != nil || n != 1 {
			t.Errorf("SetEntriesRead = %d, %v; want 1 changed entry", n, err)
		}
		if got := entry(lib.alice, e1); !got.IsRead || got.LastUserModified != 0 {
			t.Errorf("entry 1 = read %v, changed by the user at %d; want read and untouched", got.IsRead, got.LastUserModified)
		}
		if got := entry(lib.alice, e2); !got.IsRead || got.LastUserModified != 77 {
			t.Errorf("entry 2 = read %v, changed by the user at %d; want read at 77", got.IsRead, got.LastUserModified)
		}
		if n, err := s.SetEntriesRead(ctx, lib.alice.ID, []int64{e1, e2}, false, 78); err != nil || n != 2 {
			t.Errorf("SetEntriesRead(unread) = %d, %v; want 2", n, err)
		}

		// Starring stamps every entry, changed or not.
		if err := s.SetEntriesFavorite(ctx, lib.alice.ID, []int64{e1, e2}, true, 80); err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{e1, e2} {
			if got := entry(lib.alice, id); !got.IsFavorite || got.LastUserModified != 80 {
				t.Errorf("entry %d = starred %v at %d; want starred at 80", id, got.IsFavorite, got.LastUserModified)
			}
		}
		if err := s.SetEntriesFavorite(ctx, lib.alice.ID, []int64{e2}, false, 81); err != nil {
			t.Fatal(err)
		}
		if got := entry(lib.alice, e2); got.IsFavorite {
			t.Error("entry 2 is still starred")
		}
		// The other user has entries with the same identifiers.
		if got := entry(lib.bob, e2); got.IsRead || !got.IsFavorite || got.LastUserModified != 0 {
			t.Errorf("entry 2 of the other user changed: %+v", got)
		}
	})
}

func TestMarkSetRead(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		unread := func(u *User) []int64 {
			t.Helper()
			ids, err := s.ListEntryIDs(ctx, u.ID, EntryQuery{Read: boolPtr(false), Ascending: true})
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}
		// Unread at the start: entries 2, 4, 5 and 6.
		for _, step := range []struct {
			name  string
			set   EntrySet
			maxID int64
			count int
			left  []int64
		}{
			{"a feed up to an entry", EntrySet{FeedID: 2}, e3, 0, []int64{e2, e4, e5, e6}},
			{"a category without its important feeds", EntrySet{CategoryID: 2, MinPriority: intPtr(0), BelowPriority: intPtr(20)}, e7, 1, []int64{e2, e5, e6}},
			{"a label", EntrySet{LabelID: 1}, e7, 1, []int64{e5, e6}},
			{"everything shown up to an entry", EntrySet{MinPriority: intPtr(-9)}, e4, 0, []int64{e5, e6}},
			{"everything shown", EntrySet{MinPriority: intPtr(-9)}, e7, 1, []int64{e6}},
			{"everything", EntrySet{}, e7, 1, nil},
		} {
			n, err := s.MarkSetRead(ctx, lib.alice.ID, step.set, step.maxID, 90)
			if err != nil || n != step.count {
				t.Errorf("%s: MarkSetRead = %d, %v; want %d", step.name, n, err, step.count)
			}
			if got := unread(lib.alice); !reflect.DeepEqual(got, step.left) {
				t.Errorf("%s: unread = %v, want %v", step.name, got, step.left)
			}
		}
		if e, err := s.EntryByID(ctx, lib.alice.ID, e4); err != nil || e.LastUserModified != 90 {
			t.Errorf("entry 4: changed by the user at %d, %v; want 90", e.LastUserModified, err)
		}
		if got := unread(lib.bob); !reflect.DeepEqual(got, []int64{e2, e4, e5, e6}) {
			t.Errorf("unread of the other user = %v, want it untouched", got)
		}
	})
}

func TestCounts(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		feeds, err := s.FeedCounts(ctx, lib.alice.ID)
		wantFeeds := map[int64]Counts{1: {1, e2}, 2: {1, e4}, 3: {1, e7}, 4: {1, e6}}
		if err != nil || !reflect.DeepEqual(feeds, wantFeeds) {
			t.Errorf("FeedCounts = %v, %v; want %v", feeds, err, wantFeeds)
		}
		// A label on no entry has no counts.
		if err := s.CreateTag(ctx, &Tag{UserID: lib.alice.ID, Name: "empty"}); err != nil {
			t.Fatal(err)
		}
		labels, err := s.LabelCounts(ctx, lib.alice.ID)
		wantLabels := map[int64]Counts{1: {1, e3}, 2: {0, e3}}
		if err != nil || !reflect.DeepEqual(labels, wantLabels) {
			t.Errorf("LabelCounts = %v, %v; want %v", labels, err, wantLabels)
		}
	})
}

func TestLabels(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		alice := lib.alice.ID
		labelled := func(u *User, tagID int64) []int64 {
			t.Helper()
			ids, err := s.ListEntryIDs(ctx, u.ID, EntryQuery{Set: EntrySet{LabelID: tagID}, Ascending: true})
			if err != nil {
				t.Fatal(err)
			}
			return ids
		}

		// An entry that does not exist gets no label; one that has it keeps it.
		if err := s.TagEntries(ctx, alice, 2, []int64{e1, e3, 12345}); err != nil {
			t.Fatalf("TagEntries: %v", err)
		}
		if got := labelled(lib.alice, 2); !reflect.DeepEqual(got, []int64{e1, e3}) {
			t.Errorf("label 2 after TagEntries = %v, want entries 1 and 3", got)
		}
		if err := s.UntagEntries(ctx, alice, 2, []int64{e3, e5, 12345}); err != nil {
			t.Fatalf("UntagEntries: %v", err)
		}
		if got := labelled(lib.alice, 2); !reflect.DeepEqual(got, []int64{e1}) {
			t.Errorf("label 2 after UntagEntries = %v, want entry 1", got)
		}

		if err := s.RenameTag(ctx, alice, 2, "later"); !errors.Is(err, ErrConflict) {
			t.Errorf("RenameTag to the name of a label: error = %v, want ErrConflict", err)
		}
		if err := s.RenameTag(ctx, alice, 2, "News"); !errors.Is(err, ErrConflict) {
			t.Errorf("RenameTag to the name of a category: error = %v, want ErrConflict", err)
		}
		if err := s.RenameTag(ctx, alice, 99, "other"); !errors.Is(err, ErrNotFound) {
			t.Errorf("RenameTag of a missing label: error = %v, want ErrNotFound", err)
		}
		if err := s.RenameTag(ctx, alice, 2, "play"); err != nil {
			t.Fatalf("RenameTag: %v", err)
		}
		if err := s.DeleteTag(ctx, alice, 1); err != nil {
			t.Fatalf("DeleteTag: %v", err)
		}
		tags, err := s.Tags(ctx, alice)
		if err != nil || len(tags) != 1 || tags[0].ID != 2 || tags[0].Name != "play" {
			t.Errorf("Tags = %+v, %v; want only label 2 called play", tags, err)
		}
		if labels, err := s.EntryLabels(ctx, alice, []int64{e1, e2, e3}); err != nil || !reflect.DeepEqual(labels, map[int64][]string{e1: {"play"}}) {
			t.Errorf("EntryLabels after the deletion = %v, %v", labels, err)
		}
		if got := labelled(lib.bob, 1); !reflect.DeepEqual(got, []int64{e2, e3}) {
			t.Errorf("label 1 of the other user = %v, want it untouched", got)
		}
	})
}

func TestCategoriesAndLabelsShareNames(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		alice := lib.alice.ID
		if err := s.CreateCategory(ctx, &Category{UserID: alice, Name: "later"}); !errors.Is(err, ErrConflict) {
			t.Errorf("CreateCategory with the name of a label: error = %v, want ErrConflict", err)
		}
		if err := s.UpdateCategory(ctx, &Category{UserID: alice, ID: 2, Name: "work"}); !errors.Is(err, ErrConflict) {
			t.Errorf("UpdateCategory to the name of a label: error = %v, want ErrConflict", err)
		}
		if err := s.UpdateCategory(ctx, &Category{UserID: alice, ID: 2, Name: DefaultCategoryName}); !errors.Is(err, ErrConflict) {
			t.Errorf("UpdateCategory to the name of a category: error = %v, want ErrConflict", err)
		}
		if err := s.UpdateCategory(ctx, &Category{UserID: alice, ID: 2, Name: "News"}); err != nil {
			t.Errorf("UpdateCategory keeping the name: %v", err)
		}
	})
}

func TestDeleteCategoryAndFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		lib := newLibrary(t, s)
		alice := lib.alice.ID

		if err := s.DeleteCategory(ctx, alice, 2); err != nil {
			t.Fatalf("DeleteCategory: %v", err)
		}
		if err := s.DeleteCategory(ctx, alice, DefaultCategoryID); err != nil {
			t.Fatalf("DeleteCategory of the default category: %v", err)
		}
		categories, err := s.Categories(ctx, alice)
		if err != nil || len(categories) != 1 || categories[0].ID != DefaultCategoryID {
			t.Errorf("Categories = %+v, %v; want only the default one", categories, err)
		}
		feeds, err := s.Feeds(ctx, alice)
		if err != nil || len(feeds) != 4 {
			t.Fatalf("Feeds = %d, %v; want all 4 kept", len(feeds), err)
		}
		for _, f := range feeds {
			if f.CategoryID != DefaultCategoryID {
				t.Errorf("feed %d is in category %d, want the default one", f.ID, f.CategoryID)
			}
		}

		if err := s.DeleteFeed(ctx, alice, 2); err != nil {
			t.Fatalf("DeleteFeed: %v", err)
		}
		if err := s.DeleteFeed(ctx, alice, 2); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteFeed twice: error = %v, want ErrNotFound", err)
		}
		ids, err := s.ListEntryIDs(ctx, alice, EntryQuery{Ascending: true})
		if err != nil || !reflect.DeepEqual(ids, []int64{e1, e2, e5, e6, e7}) {
			t.Errorf("entries after DeleteFeed = %v, %v; want those of the other feeds", ids, err)
		}
		if labels, err := s.EntryLabels(ctx, alice, []int64{e2, e3}); err != nil || !reflect.DeepEqual(labels, map[int64][]string{e2: {"later"}}) {
			t.Errorf("labels after DeleteFeed = %v, %v; want those of entry 3 gone", labels, err)
		}
		if n, err := s.CountEntries(ctx, lib.bob.ID); err != nil || n != 7 {
			t.Errorf("entries of the other user = %d, %v; want 7", n, err)
		}
	})
}

func TestSalt(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		first, err := s.Salt(ctx)
		if err != nil || len(first) != 64 {
			t.Fatalf("Salt = %q, %v; want 64 hexadecimal digits", first, err)
		}
		if again, err := s.Salt(ctx); err != nil || again != first {
			t.Errorf("Salt again = %q, %v; want the same %q", again, err, first)
		}
		if err := s.SetSetting(ctx, SettingSalt, "imported"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Salt(ctx); err != nil || got != "imported" {
			t.Errorf("Salt after an import = %q, %v; want the imported one", got, err)
		}
	})
}
