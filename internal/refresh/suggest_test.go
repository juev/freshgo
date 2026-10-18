package refresh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// suggestWorld gives the Refresher of a world a language model that
// answers two label names to every question, or fails when broken is set,
// and counts the questions.
func suggestWorld(t *testing.T, w *world, broken *bool) func() int {
	t.Helper()
	var (
		mu    sync.Mutex
		asked int
	)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		asked++
		fail := *broken
		mu.Unlock()
		if fail {
			http.Error(rw, "overloaded", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": "Blogs\nWords"}}},
		})
	}))
	t.Cleanup(server.Close)
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	w.r.Suggest = translate.New(w.db, &translate.Client{URL: server.URL, Key: "k", Model: "m", HTTP: client}).SuggestLabelsFor
	t.Cleanup(func() { w.r.Wait() })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return asked
	}
}

// suggestions are the titles of the entries of a feed that have label
// suggestions, with the names.
func (w *world) suggestions(f *store.Feed) map[string][]string {
	w.t.Helper()
	out := map[string][]string{}
	for _, e := range w.entries(f) {
		if names := translate.LabelSuggestions(e); names != nil {
			out[e.Title] = names
		}
	}
	return out
}

// A rule with the action "suggest", of the user or of the feed, has the
// model suggest labels for the new entries it matches and for no others;
// no entry is labelled, nothing is asked again for entries already stored,
// and a model that fails leaves the entries as they are.
func TestRefreshSuggestsLabels(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		broken := false
		asked := suggestWorld(t, w, &broken)
		u := w.user("alice", `{"filters": [{"search": "intitle:Long", "actions": ["suggest"]}]}`)
		byUser := w.feed(u, "/user.xml", nil)
		byFeed := w.feed(u, "/feed.xml", func(f *store.Feed) {
			f.Attributes = json.RawMessage(`{"filters": [{"search": "intitle:Short", "actions": ["suggest", "star"]}]}`)
		})
		silent := w.feed(u, "/silent.xml", func(f *store.Feed) {
			f.Attributes = json.RawMessage(`{"filters": [{"search": "intitle:Nothing", "actions": ["suggest"]}]}`)
		})
		for _, path := range []string{"/user.xml", "/feed.xml", "/silent.xml"} {
			articles(w, path)
		}
		taken := &store.Tag{UserID: u.ID, Name: "Blogs"}
		if err := w.db.CreateTag(ctx, taken); err != nil {
			t.Fatal(err)
		}

		w.run(Options{})
		w.r.Wait()
		// The user's rule matches the title Long in every feed; the rule of the feed its own.
		want := map[*store.Feed]map[string][]string{
			byUser: {"Long": {"Blogs", "Words"}},
			byFeed: {"Long": {"Blogs", "Words"}, "Short": {"Blogs", "Words"}},
			silent: {"Long": {"Blogs", "Words"}},
		}
		for f, names := range want {
			if got := w.suggestions(f); !reflect.DeepEqual(got, names) {
				t.Errorf("suggestions of %s: %v, want %v", f.URL, got, names)
			}
		}
		if n := asked(); n != 4 {
			t.Errorf("the model was asked %d times, want 4", n)
		}
		// Nothing was labelled and no label was made.
		if tags, err := w.db.Tags(ctx, u.ID); err != nil || len(tags) != 1 {
			t.Errorf("labels: %v, %v", tags, err)
		}
		for _, f := range []*store.Feed{byUser, byFeed, silent} {
			for _, e := range w.entries(f) {
				if ids, err := w.db.EntryTagIDs(ctx, u.ID, e.ID); err != nil || len(ids) != 0 {
					t.Errorf("entry %q of %s is labelled: %v, %v", e.Title, f.URL, ids, err)
				}
			}
		}
		if e := w.entry(byFeed, "urn:short"); !e.IsFavorite {
			t.Error("the other action of the rule was not done")
		}

		// Entries that are stored already are not asked about again.
		w.later()
		w.run(Options{Force: true})
		w.r.Wait()
		if n := asked(); n != 4 {
			t.Errorf("the model was asked %d times after the second run, want 4", n)
		}
	})
}

// A model that fails leaves the new entries as they arrived, and the
// refresh does not fail with it.
func TestRefreshSuggestionFailureStoresNothing(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		broken := true
		asked := suggestWorld(t, w, &broken)
		u := w.user("alice", `{"filters": [{"search": "intitle:Long", "actions": ["suggest"]}]}`)
		f := w.feed(u, "/feed.xml", nil)
		articles(w, "/feed.xml")

		w.run(Options{})
		w.r.Wait()
		if asked() == 0 {
			t.Fatal("the model was not asked")
		}
		if got := w.suggestions(f); len(got) != 0 {
			t.Errorf("suggestions after a failure: %v", got)
		}
		if got := len(w.entries(f)); got != 2 {
			t.Errorf("%d entries stored, want 2", got)
		}
	})
}
