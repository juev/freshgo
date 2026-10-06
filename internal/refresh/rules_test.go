package refresh

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

// labelled returns the guids of the entries of a feed that carry the label.
func (w *world) labelled(f *store.Feed, tag *store.Tag) []string {
	w.t.Helper()
	out := []string{}
	for _, e := range w.entries(f) {
		ids, err := w.db.EntryTagIDs(context.Background(), f.UserID, e.ID)
		if err != nil {
			w.t.Fatal(err)
		}
		for _, id := range ids {
			if id == tag.ID {
				out = append(out, e.GUID)
			}
		}
	}
	return out
}

func (w *world) starred(f *store.Feed) []string {
	w.t.Helper()
	out := []string{}
	for _, e := range w.entries(f) {
		if e.IsFavorite {
			out = append(out, e.GUID)
		}
	}
	return out
}

// R11: the filter actions of the user, of the category, of the feed and of
// the labels act on arriving entries.
func TestFilterActions(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		calls := w.watchAutoRead()
		// The feed's rule goes through a saved search of the user.
		u := w.user("alice", `{
			"queries": [{"name": "Ads", "search": "intitle:реклама"}],
			"filters": [{"search": "inurl:/sponsored", "actions": ["star"]}]}`)
		cat := w.category(u, "News", `{"filters": [{"search": "intext:купить", "actions": ["read"]}]}`)
		promo := &store.Tag{UserID: u.ID, Name: "promo",
			Attributes: json.RawMessage(`{"filters": [{"search": "search:Ads OR inurl:/sponsored", "actions": ["label"]}]}`)}
		if err := w.db.CreateTag(ctx, promo); err != nil {
			t.Fatal(err)
		}
		// The second rule has a backreference, which Go's regular expressions lack.
		feedRules := `{"filters": [
			{"search": "search:Ads", "actions": ["read"]},
			{"search": "intitle:/(a)\\1/", "actions": ["read", "star"]}]}`
		ad := item{guid: "ad", title: "Реклама недели", link: "https://example.org/ad", body: "text"}
		buy := item{guid: "buy", title: "Обзор", link: "https://example.org/buy", body: "Можно купить здесь"}
		sponsored := item{guid: "sponsored", title: "Partner post", link: "https://example.org/sponsored/1", body: "text"}
		usual := item{guid: "usual", title: "Новости", link: "https://example.org/usual", body: "text"}
		w.serveBody("/feed", rssType, rss("Blog", usual, sponsored, buy, ad))
		f := w.feed(u, "/feed", func(f *store.Feed) { f.CategoryID, f.Attributes = cat.ID, json.RawMessage(feedRules) })
		if st := w.runOne(); st.NewEntries != 4 || st.Failed != 0 {
			t.Fatalf("stats %+v", st)
		}

		if got, want := w.unread(f), []string{"sponsored", "usual"}; !reflect.DeepEqual(got, want) {
			t.Errorf("unread: %v, want %v", got, want)
		}
		if got, want := w.starred(f), []string{"sponsored"}; !reflect.DeepEqual(got, want) {
			t.Errorf("starred: %v, want %v", got, want)
		}
		if got, want := w.labelled(f, promo), []string{"ad", "sponsored"}; !reflect.DeepEqual(got, want) {
			t.Errorf("labelled: %v, want %v", got, want)
		}
		if want := []string{"ad:filter", "buy:filter"}; !reflect.DeepEqual(*calls, want) {
			t.Errorf("EntryAutoRead calls: %v, want %v", *calls, want)
		}
		if logs := w.logs.String(); !strings.Contains(logs, "filter is not usable") || !strings.Contains(logs, "regular expression is not supported") {
			t.Errorf("the unusable rule was not reported: %s", logs)
		}

		// The user takes the star off; then two entries change in the feed,
		// one of them now matching the rule of the feed and of the label.
		e := w.entry(f, "sponsored")
		e.IsFavorite = false
		if err := w.db.UpdateEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
		sponsored.body = "text, corrected"
		usual.title = "Новости и реклама"
		w.serveBody("/feed", rssType, rss("Blog", usual, sponsored, buy, ad))
		w.later()
		if st := w.runOne(); st.UpdatedEntries != 2 || st.NewEntries != 0 {
			t.Fatalf("stats %+v", st)
		}
		if got, want := w.unread(f), []string{"sponsored"}; !reflect.DeepEqual(got, want) {
			t.Errorf("unread after the entries changed: %v, want %v", got, want)
		}
		if got := w.starred(f); len(got) != 0 {
			t.Errorf("a changed entry was starred again: %v", got)
		}
		if got, want := w.labelled(f, promo), []string{"ad", "sponsored"}; !reflect.DeepEqual(got, want) {
			t.Errorf("labelled after the entries changed: %v, want %v; a changed entry gets no label", got, want)
		}
	})
}

// Rules are read against the user's time zone and clock, and a new entry
// stands under the identifier it gets now.
func TestFilterByDate(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{"timezone": "UTC", "filters": [{"search": "date:2026-10-06", "actions": ["read"]}, {"search": "pubdate:/2020", "actions": ["star"]}]}`)
		w.serveBody("/feed", rssType, rss("Blog",
			item{guid: "old", title: "Old", date: "Tue, 31 Dec 2019 23:00:00 GMT", body: "text"},
			item{guid: "new", title: "New", date: "Mon, 05 Oct 2026 10:00:00 GMT", body: "text"}))
		f := w.feed(u, "/feed", nil)
		w.runOne()
		if got := w.unread(f); len(got) != 0 {
			t.Errorf("unread: %v; both entries arrive on 2026-10-06", got)
		}
		if got, want := w.starred(f), []string{"old"}; !reflect.DeepEqual(got, want) {
			t.Errorf("starred: %v, want %v", got, want)
		}
	})
}
