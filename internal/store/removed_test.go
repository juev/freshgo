package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// A removed feed comes back as it was: its settings, its entries with their
// state, the labels on them and its icon.
func TestRemoveAndRestoreFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		news := &Category{UserID: u.ID, Name: "News"}
		if err := s.CreateCategory(ctx, news); err != nil {
			t.Fatal(err)
		}
		f := &Feed{UserID: u.ID, URL: "https://example.org/feed", Name: "Blog", CategoryID: news.ID, TTL: 900, Priority: PriorityMain,
			Attributes: json.RawMessage(`{"read_upon_reception":true}`)}
		if err := s.CreateFeed(ctx, f); err != nil {
			t.Fatal(err)
		}
		entries := []*Entry{
			{FeedID: f.ID, GUID: "a", Title: "A", Content: "<p>a</p>", Authors: []string{"Ann"}, IsRead: true, Hash: []byte{1, 2}, LastUserModified: 5},
			{FeedID: f.ID, GUID: "b", Title: "B", IsFavorite: true, Tags: []string{"go"}},
		}
		if err := s.InsertEntries(ctx, u.ID, entries); err != nil {
			t.Fatal(err)
		}
		kept, dropped := &Tag{UserID: u.ID, Name: "kept"}, &Tag{UserID: u.ID, Name: "dropped"}
		for _, tag := range []*Tag{kept, dropped} {
			if err := s.CreateTag(ctx, tag); err != nil {
				t.Fatal(err)
			}
			if err := s.TagEntries(ctx, u.ID, tag.ID, []int64{entries[0].ID}); err != nil {
				t.Fatal(err)
			}
		}
		icon := CustomIcon{Hash: "h", Content: []byte("png"), Modified: 7}
		if err := s.SetCustomIcon(ctx, u.ID, f.ID, icon); err != nil {
			t.Fatal(err)
		}
		want, err := s.FeedByID(ctx, u.ID, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := s.EntriesByFeed(ctx, u.ID, f.ID)
		if err != nil {
			t.Fatal(err)
		}

		removed, err := s.RemoveFeed(ctx, u.ID, f.ID)
		if err != nil {
			t.Fatalf("RemoveFeed: %v", err)
		}
		if _, err := s.FeedByID(ctx, u.ID, f.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the feed after RemoveFeed: %v", err)
		}
		if _, err := s.RemoveFeed(ctx, u.ID, f.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("RemoveFeed of a feed that is gone: %v, want ErrNotFound", err)
		}
		// Meanwhile one of the labels goes.
		if err := s.DeleteTag(ctx, u.ID, dropped.ID); err != nil {
			t.Fatal(err)
		}

		if err := s.RestoreFeed(ctx, removed); err != nil {
			t.Fatalf("RestoreFeed: %v", err)
		}
		if got, err := s.FeedByID(ctx, u.ID, f.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("the restored feed = %+v, %v\nwant %+v", got, err, want)
		}
		if got, err := s.EntriesByFeed(ctx, u.ID, f.ID); err != nil || !reflect.DeepEqual(got, stored) {
			t.Errorf("the restored entries = %+v, %v\nwant %+v", got, err, stored)
		}
		if got, err := s.EntryTagIDs(ctx, u.ID, entries[0].ID); err != nil || !reflect.DeepEqual(got, []int64{kept.ID}) {
			t.Errorf("labels of the restored entry = %v, %v; want only the one left", got, err)
		}
		if got, err := s.CustomIconByHash(ctx, "h"); err != nil || string(got.Content) != "png" || got.Modified != 7 {
			t.Errorf("the restored icon = %+v, %v", got, err)
		}
		if err := s.RestoreFeed(ctx, removed); !errors.Is(err, ErrConflict) {
			t.Errorf("RestoreFeed of a feed that is there: %v, want ErrConflict", err)
		}

		// A feed whose category went meanwhile comes back to the default one.
		removed, err = s.RemoveFeed(ctx, u.ID, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteCategory(ctx, u.ID, news.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RestoreFeed(ctx, removed); err != nil {
			t.Fatalf("RestoreFeed without its category: %v", err)
		}
		if got, err := s.FeedByID(ctx, u.ID, f.ID); err != nil || got.CategoryID != DefaultCategoryID {
			t.Errorf("the feed restored without its category: %+v, %v", got, err)
		}
	})
}

// A removed category comes back with the feeds that went to the default
// category and are still there.
func TestRemoveAndRestoreCategory(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		news, other := &Category{UserID: u.ID, Name: "News", Kind: 2}, &Category{UserID: u.ID, Name: "Other"}
		for _, c := range []*Category{news, other} {
			if err := s.CreateCategory(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		stays, moves, elsewhere := mustFeed(t, s, u.ID, "https://example.org/1"), mustFeed(t, s, u.ID, "https://example.org/2"), mustFeed(t, s, u.ID, "https://example.org/3")
		for _, f := range []*Feed{stays, moves} {
			f.CategoryID = news.ID
			if err := s.UpdateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
		category := func(f *Feed) int64 {
			t.Helper()
			got, err := s.FeedByID(ctx, u.ID, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			return got.CategoryID
		}

		before, err := s.Categories(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RemoveCategory(ctx, u.ID, DefaultCategoryID); !errors.Is(err, ErrConflict) {
			t.Errorf("RemoveCategory of the default category: %v, want ErrConflict", err)
		}
		removed, err := s.RemoveCategory(ctx, u.ID, news.ID)
		if err != nil {
			t.Fatalf("RemoveCategory: %v", err)
		}
		if slices.Sort(removed.Feeds); !reflect.DeepEqual(removed.Feeds, []int64{stays.ID, moves.ID}) || category(stays) != DefaultCategoryID {
			t.Errorf("RemoveCategory: feeds %v, the first now in %d", removed.Feeds, category(stays))
		}
		moves.CategoryID = other.ID
		if err := s.UpdateFeed(ctx, moves); err != nil {
			t.Fatal(err)
		}
		if err := s.RestoreCategory(ctx, removed); err != nil {
			t.Fatalf("RestoreCategory: %v", err)
		}
		categories, err := s.Categories(ctx, u.ID)
		if err != nil || !reflect.DeepEqual(categories, before) {
			t.Errorf("categories after RestoreCategory = %+v, %v; want %+v", categories, err, before)
		}
		if got := [3]int64{category(stays), category(moves), category(elsewhere)}; got != [3]int64{news.ID, other.ID, DefaultCategoryID} {
			t.Errorf("categories of the feeds after RestoreCategory = %v", got)
		}

		// A name taken meanwhile keeps the category away.
		if removed, err = s.RemoveCategory(ctx, u.ID, news.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCategory(ctx, &Category{UserID: u.ID, Name: "News"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RestoreCategory(ctx, removed); !errors.Is(err, ErrConflict) {
			t.Errorf("RestoreCategory under a name taken: %v, want ErrConflict", err)
		}
		if got := category(stays); got != DefaultCategoryID {
			t.Errorf("a failed RestoreCategory moved a feed to %d", got)
		}
	})
}

// A removed label comes back on those of its entries that are still there.
func TestRemoveAndRestoreTag(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := mustUser(t, s, "alice")
		kept, gone := mustFeed(t, s, u.ID, "https://example.org/1"), mustFeed(t, s, u.ID, "https://example.org/2")
		entries := []*Entry{{FeedID: kept.ID, GUID: "a"}, {FeedID: gone.ID, GUID: "b"}}
		if err := s.InsertEntries(ctx, u.ID, entries); err != nil {
			t.Fatal(err)
		}
		tag := &Tag{UserID: u.ID, Name: "later", Attributes: json.RawMessage(`{"color":"red"}`)}
		if err := s.CreateTag(ctx, tag); err != nil {
			t.Fatal(err)
		}
		if err := s.TagEntries(ctx, u.ID, tag.ID, []int64{entries[0].ID, entries[1].ID}); err != nil {
			t.Fatal(err)
		}
		removed, err := s.RemoveTag(ctx, u.ID, tag.ID)
		if err != nil {
			t.Fatalf("RemoveTag: %v", err)
		}
		if _, err := s.RemoveTag(ctx, u.ID, tag.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("RemoveTag of a label that is gone: %v, want ErrNotFound", err)
		}
		if err := s.DeleteFeed(ctx, u.ID, gone.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RestoreTag(ctx, removed); err != nil {
			t.Fatalf("RestoreTag: %v", err)
		}
		if tags, err := s.Tags(ctx, u.ID); err != nil || len(tags) != 1 || !reflect.DeepEqual(tags[0], tag) {
			t.Errorf("labels after RestoreTag = %+v, %v; want %+v", tags, err, tag)
		}
		if got, err := s.EntryTagIDs(ctx, u.ID, entries[0].ID); err != nil || !reflect.DeepEqual(got, []int64{tag.ID}) {
			t.Errorf("labels of the entry left = %v, %v", got, err)
		}
		if err := s.RestoreTag(ctx, removed); !errors.Is(err, ErrConflict) {
			t.Errorf("RestoreTag of a label that is there: %v, want ErrConflict", err)
		}
	})
}
