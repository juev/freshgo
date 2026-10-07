package hooks

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

func TestChainPassesResultOnInPriorityOrder(t *testing.T) {
	ctx := context.Background()
	var r Registry
	var order []string
	// Added out of order: priority decides, then the order of adding.
	r.EntryBeforeInsert.Add(10, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
		order = append(order, "late:"+e.Title)
		e.Title += "+late"
		return e, true
	})
	r.EntryBeforeInsert.Add(-5, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
		order = append(order, "early:"+e.Title)
		return &store.Entry{Title: e.Title + "+early"}, true
	})
	r.EntryBeforeInsert.Add(10, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
		order = append(order, "last:"+e.Title)
		e.Title += "+last"
		return e, true
	})

	got, ok := r.EntryBeforeInsert.Call(ctx, &store.Entry{Title: "t"})
	if !ok || got.Title != "t+early+late+last" {
		t.Errorf("Call = %+v, %v", got, ok)
	}
	want := []string{"early:t", "late:t+early", "last:t+early+late"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("handlers ran as %v, want %v", order, want)
	}
}

func TestChainDropStopsTheRest(t *testing.T) {
	ctx := context.Background()
	var r Registry
	called := false
	r.EntryBeforeInsert.Add(0, func(context.Context, *store.Entry) (*store.Entry, bool) { return nil, false })
	r.EntryBeforeInsert.Add(1, func(_ context.Context, e *store.Entry) (*store.Entry, bool) {
		called = true
		return e, true
	})
	if got, ok := r.EntryBeforeInsert.Call(ctx, &store.Entry{Title: "t"}); ok || got != nil {
		t.Errorf("Call = %+v, %v; want the entry dropped", got, ok)
	}
	if called {
		t.Error("a handler ran after the entry was dropped")
	}

	r.CheckURLBeforeAdd.Add(0, func(_ context.Context, u string) (string, bool) { return u, u != "http://blocked.example/" })
	if _, ok := r.CheckURLBeforeAdd.Call(ctx, "http://blocked.example/"); ok {
		t.Error("a refused URL was kept")
	}
	if u, ok := r.CheckURLBeforeAdd.Call(ctx, "http://fine.example/"); !ok || u != "http://fine.example/" {
		t.Errorf("an accepted URL came back as %q, %v", u, ok)
	}
}

func TestEmptyRegistryChangesNothing(t *testing.T) {
	ctx := context.Background()
	var r Registry
	e := &store.Entry{Title: "t"}
	if got, ok := r.EntryBeforeAdd.Call(ctx, e); !ok || got != e {
		t.Errorf("empty chain returned %+v, %v", got, ok)
	}
	feeds := []*store.Feed{{ID: 2}, {ID: 1}}
	if got, ok := r.FeedsListBeforeActualize.Call(ctx, feeds); !ok || !reflect.DeepEqual(got, feeds) {
		t.Errorf("empty chain returned %+v, %v", got, ok)
	}
	if r.EntriesRead.Call(ctx, EntriesRead{IDs: []int64{1}}) {
		t.Error("an event without handlers was reported as dealt with")
	}
	r.Init.Call(ctx, struct{}{})
}

func TestEventStopsAtTheHandlerThatDealtWithIt(t *testing.T) {
	ctx := context.Background()
	var r Registry
	var got []EntriesRead
	var order []int
	r.EntriesRead.Add(2, func(_ context.Context, a EntriesRead) bool {
		order = append(order, 2)
		return true
	})
	r.EntriesRead.Add(1, func(_ context.Context, a EntriesRead) bool {
		order = append(order, 1)
		got = append(got, a)
		return false
	})
	r.EntriesRead.Add(3, func(context.Context, EntriesRead) bool {
		order = append(order, 3)
		return false
	})

	arg := EntriesRead{UserID: 7, IDs: []int64{10, 11}, IsRead: true}
	if !r.EntriesRead.Call(ctx, arg) {
		t.Error("Call = false although a handler dealt with the event")
	}
	if !reflect.DeepEqual(got, []EntriesRead{arg}) {
		t.Errorf("handler got %+v, want %+v", got, arg)
	}
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Errorf("handlers ran as %v, want [1 2]", order)
	}
}

func TestEventHandlerMayChangeTheEntry(t *testing.T) {
	var r Registry
	r.EntryAutoUnread.Add(0, func(_ context.Context, a EntryAuto) bool {
		a.Entry.Title = a.Why
		return false
	})
	e := &store.Entry{}
	r.EntryAutoUnread.Call(context.Background(), EntryAuto{Entry: e, Why: WhyUpdatedArticle})
	if e.Title != "updated_article" {
		t.Errorf("the handler's change was lost: %+v", e)
	}
}

func TestSignalCallsEveryHandler(t *testing.T) {
	var r Registry
	var order []string
	r.UserMaintenance.Add(1, func(_ context.Context, u *store.User) { order = append(order, "b:"+u.Name) })
	r.UserMaintenance.Add(0, func(_ context.Context, u *store.User) { order = append(order, "a:"+u.Name) })
	r.UserMaintenance.Call(context.Background(), &store.User{Name: "alice"})
	if !reflect.DeepEqual(order, []string{"a:alice", "b:alice"}) {
		t.Errorf("handlers ran as %v", order)
	}
}

// What goes through the hooks survives JSON, which is how an out-of-process
// runtime would get it.
func TestEntitiesSurviveJSON(t *testing.T) {
	entry := &store.Entry{
		UserID: 1, ID: 1700000000000001, FeedID: 2, GUID: "a&amp;b", Title: "Привет <мир>", Authors: []string{"A", "B; C"},
		Content: "<p>текст</p>", Link: "https://example.org/?a=1&b=2", Published: 1, LastSeen: 2, LastModified: 3,
		LastUserModified: 4, Hash: []byte{0, 255, 16}, IsRead: true, IsFavorite: true, Tags: []string{"x"},
		Attributes: json.RawMessage(`{"enclosures":[{"url":"https://example.org/a.mp3"}]}`),
	}
	feed := &store.Feed{
		UserID: 1, ID: 2, URL: "https://example.org/feed", Kind: 10, CategoryID: 3, Name: "n", Website: "w",
		Description: "d", LastUpdate: 5, Priority: -5, PathEntries: "article", HTTPAuth: "u:p", Error: 6, TTL: -900,
		Attributes: json.RawMessage(`{"xpath":{"item":"//a"}}`), HTTPETag: `"e"`, HTTPLastModified: "lm",
	}
	for _, v := range []any{entry, feed} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		back := reflect.New(reflect.TypeOf(v).Elem()).Interface()
		if err := json.Unmarshal(data, back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, v) {
			t.Errorf("after JSON:\n got %+v\nwant %+v", back, v)
		}
	}
}

func TestGatherPutsTogetherWhatHandlersGive(t *testing.T) {
	ctx := context.Background()
	var r Registry
	if got := r.NavMenu.Call(ctx, Page{}); got != nil {
		t.Errorf("a hook without handlers gave %+v", got)
	}
	r.NavMenu.Add(5, func(_ context.Context, p Page) []Link {
		return []Link{{Name: "late for " + p.Language, URL: "/late"}}
	})
	r.NavMenu.Add(0, func(context.Context, Page) []Link { return nil })
	r.NavMenu.Add(-1, func(context.Context, Page) []Link {
		return []Link{{Name: "first", URL: "/1"}, {Name: "second", URL: "/2"}}
	})
	want := []Link{{Name: "first", URL: "/1"}, {Name: "second", URL: "/2"}, {Name: "late for ru", URL: "/late"}}
	if got := r.NavMenu.Call(ctx, Page{Language: "ru"}); !reflect.DeepEqual(got, want) {
		t.Errorf("Call = %+v, want %+v", got, want)
	}
}
