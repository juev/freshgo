package translate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// L1–L2: the names are read from the answer one a line or as a JSON array;
// at most three are kept, none empty, long, twice whatever the case, a
// name of a category or one the entry has; a name that differs from a
// label of the user in case alone is that label.
func TestPickLabels(t *testing.T) {
	long := strings.Repeat("x", MaxLabelName+1)
	for name, c := range map[string]struct {
		answer                        string
		existing, categories, applied []string
		want                          []string
	}{
		"lines":        {answer: "Go\nNews", want: []string{"Go", "News"}},
		"list marks":   {answer: "- Go\n* News\n1. Web\n2) Extra", want: []string{"Go", "News", "Web"}},
		"quotes":       {answer: "\"Go\"\n`News`", want: []string{"Go", "News"}},
		"json":         {answer: `["Go", " News ", ""]`, want: []string{"Go", "News"}},
		"too many":     {answer: "a\nb\nc\nd\ne", want: []string{"a", "b", "c"}},
		"duplicates":   {answer: "Go\ngo\nGO\nNews", want: []string{"Go", "News"}},
		"empty":        {answer: "\n \n-\n", want: nil},
		"too long":     {answer: long + "\nGo", want: []string{"Go"}},
		"category":     {answer: "News\nGo", categories: []string{"News"}, want: []string{"Go"}},
		"other case":   {answer: "news", categories: []string{"News"}, want: []string{"news"}},
		"existing":     {answer: "golang\nnews", existing: []string{"Golang", "News"}, want: []string{"Golang", "News"}},
		"on the entry": {answer: "Go\nNews", applied: []string{"go"}, want: []string{"News"}},
		"spaces":       {answer: "Open   source", want: []string{"Open source"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := pickLabels(c.answer, c.existing, c.categories, c.applied); !reflect.DeepEqual(got, c.want) {
				t.Errorf("pickLabels(%q) = %q, want %q", c.answer, got, c.want)
			}
		})
	}
}

// L3–L8: labels are suggested on request and kept with the entry, which is
// not labelled, nor its text touched; the model is told the labels of the
// user; suggestions go when the text changes, one by one or all; a model
// that fails, or answers nothing usable, stores nothing.
func TestSuggestLabels(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(engine.Name, func(t *testing.T) {
			ctx := context.Background()
			driver, dsn := engine.New(t)
			db, err := store.Open(ctx, driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			u := &store.User{Name: "alice", Settings: json.RawMessage(`{}`)}
			if err := db.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			f := &store.Feed{UserID: u.ID, URL: "https://example.org/feed", Name: "Feed"}
			if err := db.CreateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateTag(ctx, &store.Tag{UserID: u.ID, Name: "Golang"}); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateTag(ctx, &store.Tag{UserID: u.ID, Name: "Applied"}); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateCategory(ctx, &store.Category{UserID: u.ID, Name: "News"}); err != nil {
				t.Fatal(err)
			}
			entry := &store.Entry{FeedID: f.ID, GUID: "article", Title: "Go 1.30 is out", Content: "<p>The release of <b>Go</b>.</p>", Attributes: json.RawMessage(`{"original_content":"kept"}`)}
			if err := db.InsertEntries(ctx, u.ID, []*store.Entry{entry}); err != nil {
				t.Fatal(err)
			}
			applied, err := db.Tags(ctx, u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.TagEntries(ctx, u.ID, applied[1].ID, []int64{entry.ID}); err != nil {
				t.Fatal(err)
			}
			stored := func() *store.Entry {
				e, err := db.EntryByID(ctx, u.ID, entry.ID)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			answer := "```\n- golang\n- News\n- Applied\n- Releases\n- Extra\n```"
			s := newService(t, func(system, user string) string { return answer })
			tr := New(db, s.Client)

			// L3: the names are chosen by the rules above, and the model is
			// told the labels of the user and the text without markup.
			got, err := tr.SuggestLabels(ctx, u.ID, entry.ID)
			if want := []string{"Golang", "Releases", "Extra"}; err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("SuggestLabels = %q, %v; want %q", got, err, want)
			}
			if asked := s.asked[0]; !strings.Contains(asked, "Labels:\nGolang\nApplied") || !strings.Contains(asked, "Title: Go 1.30 is out") ||
				!strings.Contains(asked, "The release of Go") || strings.Contains(asked, "<b>") {
				t.Errorf("the model was told: %q", asked)
			}
			// L4: they are stored; the entry is not labelled and keeps its text and attributes.
			e := stored()
			if names := LabelSuggestions(e); !reflect.DeepEqual(names, got) {
				t.Errorf("LabelSuggestions = %q", names)
			}
			if e.Title != entry.Title || e.Content != entry.Content || !strings.Contains(string(e.Attributes), `"original_content":"kept"`) {
				t.Errorf("the entry itself changed: %s", e.Attributes)
			}
			if labels, err := db.EntryLabels(ctx, u.ID, []int64{entry.ID}); err != nil || !reflect.DeepEqual(labels[entry.ID], []string{"Applied"}) {
				t.Errorf("labels of the entry: %v, %v", labels, err)
			}
			if tags, err := db.Tags(ctx, u.ID); err != nil || len(tags) != 2 {
				t.Errorf("a label was made: %v, %v", tags, err)
			}

			// L5: a name goes alone, then the last one takes the record with it.
			if err := DropSuggestion(ctx, db, u.ID, entry.ID, "Releases"); err != nil {
				t.Fatal(err)
			}
			if names := LabelSuggestions(stored()); !reflect.DeepEqual(names, []string{"Golang", "Extra"}) {
				t.Errorf("after dropping one: %q", names)
			}
			if err := DropSuggestion(ctx, db, u.ID, entry.ID, ""); err != nil {
				t.Fatal(err)
			}
			if e := stored(); LabelSuggestions(e) != nil || strings.Contains(string(e.Attributes), labelsAttribute) || !strings.Contains(string(e.Attributes), "original_content") {
				t.Errorf("after dropping all: %s", e.Attributes)
			}

			// L6: a model that fails or is cut off stores nothing.
			s.mu.Lock()
			s.finish = "length"
			s.mu.Unlock()
			if _, err := tr.SuggestLabels(ctx, u.ID, entry.ID); err == nil || LabelSuggestions(stored()) != nil {
				t.Errorf("an answer cut off: %v, %q", err, LabelSuggestions(stored()))
			}
			s.mu.Lock()
			s.finish = ""
			answer = "News\n\n"
			s.mu.Unlock()
			// L7: an answer without a usable name is an error and stores nothing.
			if _, err := tr.SuggestLabels(ctx, u.ID, entry.ID); !errors.Is(err, ErrNoLabels) || LabelSuggestions(stored()) != nil {
				t.Errorf("a category as the only name: %v, %q", err, LabelSuggestions(stored()))
			}

			// L8: suggestions are for the text they were made for.
			s.mu.Lock()
			answer = "Releases"
			s.mu.Unlock()
			if _, err := tr.SuggestLabels(ctx, u.ID, entry.ID); err != nil {
				t.Fatal(err)
			}
			changed := stored()
			changed.Content = "<p>Another text.</p>"
			if err := db.UpdateEntry(ctx, changed); err != nil {
				t.Fatal(err)
			}
			if names := LabelSuggestions(stored()); names != nil {
				t.Errorf("suggestions for the old text are offered: %q", names)
			}

			// The batch for the entries of a refresh: one that fails does not keep the others.
			other := &store.Entry{FeedID: f.ID, GUID: "other", Title: "Other", Content: "<p>Text.</p>"}
			if err := db.InsertEntries(ctx, u.ID, []*store.Entry{other}); err != nil {
				t.Fatal(err)
			}
			err = tr.SuggestLabelsFor(ctx, u, []*store.Entry{{ID: 1}, other})
			if err == nil || !errors.Is(err, store.ErrNotFound) {
				t.Errorf("SuggestLabelsFor with a missing entry: %v", err)
			}
			if e, err := db.EntryByID(ctx, u.ID, other.ID); err != nil || !reflect.DeepEqual(LabelSuggestions(e), []string{"Releases"}) {
				t.Errorf("the entry after the missing one: %v, %v", LabelSuggestions(e), err)
			}
		})
	}
}
