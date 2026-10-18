package translate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"golang.org/x/text/language"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// longArticle has more than LongText characters of text.
var longArticle = strings.Repeat("<p>Hello "+strings.Repeat("x", 400)+"</p>", 8)

// S1–S6: a summary is made on request, stored with the entry beside its
// text, and gone when the text changes; a model that fails or is cut off
// leaves the entry as it was.
func TestSummarise(t *testing.T) {
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
			entry := &store.Entry{FeedID: f.ID, GUID: "article", Title: "Hello, reader", Content: longArticle, Attributes: json.RawMessage(`{"original_content":"kept"}`)}
			if err := db.InsertEntries(ctx, u.ID, []*store.Entry{entry}); err != nil {
				t.Fatal(err)
			}
			stored := func() *store.Entry {
				e, err := db.EntryByID(ctx, u.ID, entry.ID)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			var meanwhile func()
			s := newService(t, func(system, user string) string {
				if meanwhile != nil {
					meanwhile()
				}
				return "  A short summary.\nSecond line. "
			})
			tr := New(db, s.Client)

			// S1: the text is asked for in the language, without markup.
			got, err := tr.Summarise(ctx, u.ID, entry.ID, language.Russian)
			if err != nil || got != "A short summary.\nSecond line." {
				t.Fatalf("Summarise = %q, %v", got, err)
			}
			if asked := s.asked[0]; strings.Contains(asked, "<p>") || !strings.Contains(asked, "Title: Hello, reader") || !strings.Contains(asked, "Hello xxxx") {
				t.Errorf("the model was sent markup or too little: %.200q", asked)
			}
			// S2: the summary is stored; the title, the text and the other
			// attributes are not touched.
			e := stored()
			if text, ok := SummaryOf(e); !ok || text != got {
				t.Errorf("SummaryOf = %q, %v", text, ok)
			}
			if e.Title != "Hello, reader" || e.Content != longArticle || !strings.Contains(string(e.Attributes), `"original_content":"kept"`) {
				t.Errorf("the entry itself changed: %q, attributes %s", e.Title, e.Attributes)
			}

			// S4: a model that fails leaves the stored summary and the entry
			// as they were.
			good := stored().Attributes
			key := s.Key
			s.Key = "refused"
			if _, err := tr.Summarise(ctx, u.ID, entry.ID, language.Russian); err == nil {
				t.Error("a service that refuses: no error")
			}
			// S5: an answer cut off is a failure, not a summary.
			s.Key, s.finish = key, "length"
			if _, err := tr.Summarise(ctx, u.ID, entry.ID, language.German); err == nil {
				t.Error("an answer that ended by length: no error")
			}
			if after := stored(); string(after.Attributes) != string(good) || after.Content != longArticle {
				t.Errorf("after the failures the entry is %s", after.Attributes)
			}
			s.finish = ""

			// S6: another text has no summary.
			e = stored()
			e.Content = "<p>Bye, reader.</p>"
			if err := db.UpdateEntry(ctx, e); err != nil {
				t.Fatal(err)
			}
			if _, ok := SummaryOf(stored()); ok {
				t.Error("the summary of the old text is offered for the new one")
			}

			// S6a: a text that changes while the model answers gets no summary
			// of the old one.
			e = stored()
			e.Content = "<p>Another text, again.</p>"
			if err := db.UpdateEntry(ctx, e); err != nil {
				t.Fatal(err)
			}
			meanwhile = func() {
				e := stored()
				e.Content = "<p>Changed while the model answered.</p>"
				if err := db.UpdateEntry(ctx, e); err != nil {
					t.Error(err)
				}
				meanwhile = nil
			}
			if got, err := tr.Summarise(ctx, u.ID, entry.ID, language.Russian); err != nil || got != "" {
				t.Errorf("Summarise of a text that changed meanwhile = %q, %v", got, err)
			}
			if _, ok := SummaryOf(stored()); ok {
				t.Error("the summary of the text before the change is stored")
			}

			// An entry without text has nothing to summarise, and no request is made.
			blank := &store.Entry{FeedID: f.ID, GUID: "blank", Title: "Nothing", Content: "<img src=\"a.png\">"}
			if err := db.InsertEntries(ctx, u.ID, []*store.Entry{blank}); err != nil {
				t.Fatal(err)
			}
			before := s.requests()
			if _, err := tr.Summarise(ctx, u.ID, blank.ID, language.Russian); !errors.Is(err, ErrNoText) || s.requests() != before {
				t.Errorf("an entry without text: %v, %d requests", err, s.requests()-before)
			}
			if _, err := tr.Summarise(ctx, u.ID, 12345, language.Russian); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("an entry there is not: %v", err)
			}
			if _, err := tr.Summarise(ctx, u.ID+1, entry.ID, language.Russian); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("the entry of another user: %v", err)
			}
		})
	}
}

// S7–S9: only long entries are summarised for a feed that asks, in the
// language of the user, and one that fails does not stop the others.
func TestSummariseLong(t *testing.T) {
	ctx := context.Background()
	driver, dsn := storetest.Engines()[0].New(t)
	db, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	u := &store.User{Name: "alice", Settings: json.RawMessage(`{"language":"fr","translate_to":"ru"}`)}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	f := &store.Feed{UserID: u.ID, URL: "https://example.org/feed", Name: "Feed"}
	if err := db.CreateFeed(ctx, f); err != nil {
		t.Fatal(err)
	}
	// Characters of text count, not those of markup: the third is short
	// text in much markup.
	markup := strings.Repeat(`<p class="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">Hi</p>`, 100)
	entries := []*store.Entry{
		{FeedID: f.ID, GUID: "long", Title: "Long", Content: longArticle},
		{FeedID: f.ID, GUID: "short", Title: "Short", Content: "<p>Hello</p>"},
		{FeedID: f.ID, GUID: "markup", Title: "Markup", Content: markup},
		{FeedID: f.ID, GUID: "second", Title: "Second", Content: longArticle + "<p>more</p>"},
	}
	if err := db.InsertEntries(ctx, u.ID, entries); err != nil {
		t.Fatal(err)
	}
	s := newService(t, func(system, user string) string {
		if strings.Contains(user, "Title: Long") {
			return ""
		}
		return "Résumé."
	})
	err = New(db, s.Client).SummariseLong(ctx, u, entries)
	if err == nil || !strings.Contains(err.Error(), "entry") {
		t.Errorf("the failed entry is not reported: %v", err)
	}
	if s.requests() != 2 {
		t.Errorf("%d requests, want 2: the long entries only", s.requests())
	}
	if !strings.Contains(s.systems()[0], "Russian") {
		t.Errorf("the summary is not asked for in the language of the user: %q", s.systems()[0])
	}
	check := func(id int64, want bool) {
		t.Helper()
		e, err := db.EntryByID(ctx, u.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := SummaryOf(e); ok != want {
			t.Errorf("entry %q has a summary: %v, want %v", e.Title, ok, want)
		}
	}
	check(entries[0].ID, false)
	check(entries[1].ID, false)
	check(entries[2].ID, false)
	check(entries[3].ID, true)
}

func TestUserLanguage(t *testing.T) {
	for settings, want := range map[string]language.Tag{
		`{}`:                                    language.English,
		`{"language":"ru"}`:                     language.Russian,
		`{"language":"ru","translate_to":"de"}`: language.German,
		`{"translate_to":"nonsense!"}`:          language.English,
		`not json`:                              language.English,
	} {
		if got := UserLanguage(json.RawMessage(settings)); got != want {
			t.Errorf("UserLanguage(%s) = %v, want %v", settings, got, want)
		}
	}
}

// S10: an entry is long above LongText characters of text, whatever the markup.
func TestLong(t *testing.T) {
	for _, c := range []struct {
		content string
		want    bool
	}{
		{"<p>" + strings.Repeat("a", LongText) + "</p>", false},
		{"<p>" + strings.Repeat("a", LongText+1) + "</p>", true},
		{"<p>" + strings.Repeat("a", 2*LongText+5) + "</p>", true},
		{strings.Repeat(`<p class="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">Hi</p>`, 300), false},
		{"", false},
	} {
		if got := Long(&store.Entry{Content: c.content}); got != c.want {
			t.Errorf("Long(%.40q… of %d bytes) = %v, want %v", c.content, len(c.content), got, c.want)
		}
	}
}
