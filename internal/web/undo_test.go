package web

import (
	"context"
	"errors"
	"html"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

var (
	undoForm     = regexp.MustCompile(`<form method="post" action="/undo" class="undo"><p>[^<]* <input type="hidden" name="next" value="([^"]*)"><button type="submit">Undo</button></p></form>`)
	markAllField = regexp.MustCompile(`<input type="hidden" name="(stream|state|q|before|next)" value="([^"]*)">`)
)

// offered is the form of the button that takes back the action of the
// notice of a page, nil when the page offers none.
func offered(body string) url.Values {
	m := undoForm.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	return url.Values{"next": {html.UnescapeString(m[1])}}
}

// U108: the notice of marking a page read, of deleting a feed, a category or
// a label and of unsubscribing from a category offers to undo it, and Undo
// puts back what the action changed, once, for ten minutes.
func TestUndo(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		clock := s.clock()
		s.asAlice()
		alice := s.user("alice")
		undo := func(what string, form url.Values) string {
			t.Helper()
			if form == nil {
				t.Fatalf("%s: the notice offers no undo", what)
			}
			_, body := s.follow("/undo", form)
			return notice(body)
		}

		// Marking a page read: what was read before stays read.
		unread := func() []int64 { return s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 1}, Read: ptr(false)}) }
		before := unread()
		if len(before) < 2 {
			t.Fatalf("feed 1 has %d unread entries: the case proves nothing", len(before))
		}
		if _, err := s.db.SetEntriesRead(ctx, alice.ID, before[:1], true, clock.Unix()); err != nil {
			t.Fatal(err)
		}
		before = before[1:]
		form := url.Values{}
		for _, m := range markAllField.FindAllStringSubmatch(s.page("/feeds/1"), -1) {
			form.Set(m[1], html.UnescapeString(m[2]))
		}
		_, body := s.follow("/read-all", form)
		again := offered(body)
		if len(unread()) != 0 || again.Get("next") != "/feeds/1" {
			t.Fatalf("after marking the page read: %d unread, undo %v", len(unread()), again)
		}
		location, body := s.follow("/undo", again)
		if location != "/feeds/1" || notice(body) != "Undone." || !reflect.DeepEqual(unread(), before) {
			t.Errorf("after undoing: at %s, notice %q, unread %v, want %v", location, notice(body), unread(), before)
		}
		if got := undo("undoing twice", again); got != "There is nothing to undo: an action can be undone for 10 minutes, until the next one, and not after a restart of the server." {
			t.Errorf("undoing twice: notice %q", got)
		}

		// Deleting a feed: it comes back with its settings, entries and labels.
		feed, err := s.db.FeedByID(ctx, alice.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := s.db.EntriesByFeed(ctx, alice.ID, 1)
		if err != nil || len(entries) == 0 {
			t.Fatalf("entries of feed 1: %d, %v", len(entries), err)
		}
		if err := s.db.TagEntries(ctx, alice.ID, 2, []int64{entries[0].ID}); err != nil {
			t.Fatal(err)
		}
		_, body = s.follow("/subscriptions/feeds/1/delete", nil)
		if _, err := s.db.FeedByID(ctx, alice.ID, 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the feed after deleting it: %v", err)
		}
		if got := undo("deleting a feed", offered(body)); got != "Undone." {
			t.Errorf("undoing the deletion of a feed: notice %q", got)
		}
		restored, err := s.db.FeedByID(ctx, alice.ID, 1)
		if err != nil || !reflect.DeepEqual(restored, feed) {
			t.Errorf("the feed after undoing = %+v, %v\nwant %+v", restored, err, feed)
		}
		if got, err := s.db.EntriesByFeed(ctx, alice.ID, 1); err != nil || !reflect.DeepEqual(got, entries) {
			t.Errorf("the entries after undoing: %d of %d, %v", len(got), len(entries), err)
		}
		if labels, err := s.db.EntryTagIDs(ctx, alice.ID, entries[0].ID); err != nil || !slices.Contains(labels, 2) {
			t.Errorf("labels of an entry after undoing: %v, %v", labels, err)
		}

		// Deleting a category: it comes back with its feeds.
		inCategory := func(id int64) []int64 {
			var ids []int64
			for _, f := range s.feeds("alice") {
				if f.CategoryID == id {
					ids = append(ids, f.ID)
				}
			}
			return ids
		}
		category, feeds := s.category("alice", 2), inCategory(2)
		if len(feeds) == 0 {
			t.Fatal("category 2 has no feeds: the case proves nothing")
		}
		_, body = s.follow("/subscriptions/categories/2/delete", nil)
		if len(inCategory(2)) != 0 {
			t.Fatal("the feeds of a deleted category are still in it")
		}
		if got := undo("deleting a category", offered(body)); got != "Undone." {
			t.Errorf("undoing the deletion of a category: notice %q", got)
		}
		if got := s.category("alice", 2); !reflect.DeepEqual(got, category) || !reflect.DeepEqual(inCategory(2), feeds) {
			t.Errorf("after undoing: category %+v with feeds %v, want %+v with %v", got, inCategory(2), category, feeds)
		}

		// Unsubscribing from a category: every feed of it comes back.
		_, body = s.follow("/subscriptions/categories/2/empty", nil)
		if len(inCategory(2)) != 0 {
			t.Fatal("feeds left after unsubscribing from the category")
		}
		if got := undo("unsubscribing from a category", offered(body)); got != "Undone." || !reflect.DeepEqual(inCategory(2), feeds) {
			t.Errorf("undoing the unsubscribing: notice %q, feeds %v, want %v", got, inCategory(2), feeds)
		}

		// Deleting a label: it comes back on its entries. A page with another
		// notice offers nothing, and the action can still be undone.
		label := s.label("alice", 2)
		_, body = s.follow("/subscriptions/labels/2/delete", nil)
		labelUndo := offered(body)
		_, body = s.follow("/settings/profile", url.Values{"email": {""}, "token": {"t"}})
		if offered(body) != nil || !strings.HasPrefix(notice(body), "Saved.") {
			t.Errorf("the notice of saving the profile: %q, undo %v", notice(body), offered(body))
		}
		if got := undo("deleting a label", labelUndo); got != "Undone." {
			t.Errorf("undoing the deletion of a label: notice %q", got)
		}
		if got := s.label("alice", 2); !reflect.DeepEqual(got, label) {
			t.Errorf("the label after undoing = %+v, want %+v", got, label)
		}
		if labels, err := s.db.EntryTagIDs(ctx, alice.ID, entries[0].ID); err != nil || !slices.Contains(labels, 2) {
			t.Errorf("labels of an entry after undoing: %v, %v", labels, err)
		}

		// Ten minutes later nothing is undone.
		_, body = s.follow("/subscriptions/labels/2/delete", nil)
		late := offered(body)
		*clock = clock.Add(undoLife)
		if got := undo("undoing late", late); !strings.HasPrefix(got, "There is nothing to undo") {
			t.Errorf("undoing after ten minutes: notice %q", got)
		}
		if labels, err := s.db.Tags(ctx, alice.ID); err != nil || slices.ContainsFunc(labels, func(t *store.Tag) bool { return t.ID == 2 }) {
			t.Errorf("the label came back after ten minutes: %v", err)
		}
	})
}

// A label made meanwhile under the name of the deleted one keeps it from
// coming back, and the reader is told so.
func TestUndoConflict(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.clock()
		s.asAlice()
		label := s.label("alice", 2)
		_, body := s.follow("/subscriptions/labels/2/delete", nil)
		form := offered(body)
		if err := s.db.CreateTag(context.Background(), &store.Tag{UserID: s.user("alice").ID, Name: label.Name}); err != nil {
			t.Fatal(err)
		}
		if _, body := s.follow("/undo", form); notice(body) != "This cannot be undone: something of the same name or number has been made meanwhile." {
			t.Errorf("undoing under a taken name: notice %q", notice(body))
		}
	})
}

