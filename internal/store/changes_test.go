package store

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
)

// change is what EntriesChanged is told.
type change struct {
	user  int64
	added bool
}

// changes records what EntriesChanged is told.
type changes struct {
	mu    sync.Mutex
	users []change
}

func (c *changes) record(userID int64, added bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users = append(c.users, change{userID, added})
}

// take returns what was told since the last call.
func (c *changes) take() []change {
	c.mu.Lock()
	defer c.mu.Unlock()
	users := c.users
	c.users = nil
	return users
}

func TestEntriesChanged(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		var c changes
		s.EntriesChanged = c.record
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		f := mustFeed(t, s, alice.ID, "https://example.org/feed")
		mustFeed(t, s, bob.ID, "https://example.org/feed")
		check := func(what string, want ...change) {
			t.Helper()
			if got := c.take(); !slices.Equal(got, want) {
				t.Errorf("%s: told about %v, want %v", what, got, want)
			}
		}
		check("users and feeds")

		entries := []*Entry{{FeedID: f.ID, GUID: "1"}, {FeedID: f.ID, GUID: "2"}}
		if err := s.InsertEntries(ctx, alice.ID, entries); err != nil {
			t.Fatal(err)
		}
		check("entries added", change{alice.ID, true})
		ids := []int64{entries[0].ID, entries[1].ID}

		if _, err := s.SetEntriesRead(ctx, alice.ID, ids[:1], true, 1); err != nil {
			t.Fatal(err)
		}
		check("an entry read", change{alice.ID, false})
		if _, err := s.SetEntriesRead(ctx, alice.ID, ids[:1], true, 2); err != nil {
			t.Fatal(err)
		}
		check("an entry read again")
		if _, err := s.SetEntriesRead(ctx, bob.ID, ids, true, 2); err != nil {
			t.Fatal(err)
		}
		check("entries of another user")
		if err := s.SetEntriesFavorite(ctx, alice.ID, ids, true, 3); err != nil {
			t.Fatal(err)
		}
		check("entries starred", change{alice.ID, false})
		tag := &Tag{UserID: alice.ID, Name: "later"}
		if err := s.CreateTag(ctx, tag); err != nil {
			t.Fatal(err)
		}
		check("a label")
		if err := s.TagEntries(ctx, alice.ID, tag.ID, ids); err != nil {
			t.Fatal(err)
		}
		check("entries labelled", change{alice.ID, false})
		if _, err := s.MarkSetRead(ctx, alice.ID, EntrySet{}, ids[1], 4); err != nil {
			t.Fatal(err)
		}
		check("the rest read", change{alice.ID, false})
		if _, err := s.MarkSetRead(ctx, alice.ID, EntrySet{}, ids[1], 5); err != nil {
			t.Fatal(err)
		}
		check("nothing left to read")

		// Inside a transaction the change is told once, after the commit,
		// with the entries that were added.
		err := s.InTx(ctx, func(tx *Store) error {
			if _, err := tx.SetEntriesRead(ctx, alice.ID, ids, false, 6); err != nil {
				return err
			}
			if err := tx.InsertEntries(ctx, alice.ID, []*Entry{{FeedID: f.ID, GUID: "3"}}); err != nil {
				return err
			}
			if err := tx.SetEntriesFavorite(ctx, alice.ID, ids, false, 6); err != nil {
				return err
			}
			check("before the commit")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		check("a transaction", change{alice.ID, true})
		boom := errors.New("boom")
		err = s.InTx(ctx, func(tx *Store) error {
			if _, err := tx.SetEntriesRead(ctx, alice.ID, ids, true, 7); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("InTx = %v, want the error of the callback", err)
		}
		check("a transaction rolled back")

		if _, err := s.DeleteFeedEntries(ctx, alice.ID, f.ID); err != nil {
			t.Fatal(err)
		}
		check("entries deleted", change{alice.ID, false})
	})
}
