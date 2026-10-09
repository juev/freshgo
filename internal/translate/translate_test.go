package translate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/language"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// service is a chat endpoint of the test: it answers with what reply makes
// of the message of the user, and keeps what it was asked.
type service struct {
	*Client
	mu    sync.Mutex
	asked []string
	reply func(system, user string) string
}

func newService(t *testing.T, reply func(system, user string) string) *service {
	t.Helper()
	s := &service{reply: reply}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		var messages []struct{ Role, Content string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || json.Unmarshal(request["messages"], &messages) != nil || len(messages) != 2 ||
			r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" ||
			string(request["model"]) != `"m"` || len(request) != 2 || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.asked = append(s.asked, messages[1].Content)
		answer := s.reply(messages[0].Content, messages[1].Content)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": answer}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "cost": 0.5},
		})
	}))
	t.Cleanup(server.Close)
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	s.Client = &Client{URL: server.URL + "/v1/", Key: "secret", Model: "m", HTTP: client}
	return s
}

func (s *service) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

var (
	ru         = strings.NewReplacer("Hello", "Привет", "See the", "Смотрите", "catalogue of", "каталог", "feeds", "лент", "dear", "дорогой", "reader", "читатель", "Bye", "Пока", " at ", " в ", "First line", "Первая строка", "Second line", "Вторая строка", "This sentence is long enough to count as a text.", "Это предложение достаточно длинное.")
	paragraphs = regexp.MustCompile(`(?m)^\[\[(\d+)\]\] (.*)$`)
)

// translator answers as a model that does what it is asked.
func translator(system, user string) string {
	if strings.HasPrefix(system, "Answer with one word") {
		return "No."
	}
	return ru.Replace(user)
}

// T2–T5: an answer is taken paragraph by paragraph, and only the
// paragraphs whose answer cannot be used are asked for again, alone.
func TestBlocks(t *testing.T) {
	ctx := context.Background()
	root, err := parse(article)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(ru.Replace(render(root)), "<pre>Привет читатель</pre>", "<pre>Hello reader</pre>", 1)

	s := newService(t, translator)
	got, stats, err := s.Blocks(ctx, article, "Russian")
	if err != nil || got != want || stats.Paragraphs != 6 || stats.Requests != 1 || stats.Again != 0 || stats.Failed != 0 || stats.In != 10 || stats.Out != 20 || stats.Cost != 0.5 {
		t.Errorf("Blocks = %q, %+v, %v\nwant     %q", got, stats, err, want)
	}
	if asked := s.asked[0]; !strings.Contains(asked, "[[2]] See the <1>catalogue of <2>feeds</2></1> at <3/>, <4>dear</4> reader<5/>.") ||
		strings.Contains(asked, "href") || strings.Contains(asked, "curl example.org") || strings.Contains(asked, "Hello reader") {
		t.Errorf("the model was sent markup, code or addresses:\n%s", asked)
	}

	// The model leaves out one paragraph and drops a mark of another
	// until it gets them alone.
	s = newService(t, func(system, user string) string {
		if !strings.Contains(system, "paragraphs of one article") {
			return ru.Replace(user)
		}
		return paragraphs.ReplaceAllStringFunc(ru.Replace(user), func(line string) string {
			switch paragraphs.FindStringSubmatch(line)[1] {
			case "1":
				return ""
			case "2":
				return strings.Replace(line, "<3/>", "", 1)
			}
			return line
		})
	})
	got, stats, err = s.Blocks(ctx, article, "Russian")
	if err != nil || got != want || s.requests() != 3 || stats.Again != 2 || stats.Failed != 0 || stats.Reasons["no answer"] != 1 || stats.Reasons["marks changed"] != 1 {
		t.Errorf("Blocks with two bad paragraphs = %q, %d requests, %+v, %v", got, s.requests(), stats, err)
	}

	// A paragraph whose answers stay unusable is left as it was; the others are translated.
	s = newService(t, func(_, user string) string { return strings.ReplaceAll(ru.Replace(user), "<5/>", "") })
	got, stats, err = s.Blocks(ctx, article, "Russian")
	if err != nil || stats.Failed != 1 || !strings.Contains(got, "<h2>Привет</h2>") || !strings.Contains(got, "See the <a") || !strings.Contains(got, "<li>Пока<ul><li>дорогой читатель</li></ul></li>") {
		t.Errorf("Blocks with one paragraph given up = %q, %+v, %v", got, stats, err)
	}

	// A long paragraph given back as it is, is asked for again alone; left
	// as it is then too, it needs no translating.
	sentence := "<p>This sentence is long enough to count as a text.</p>"
	s = newService(t, func(system, user string) string {
		if strings.Contains(system, "paragraphs of one article") {
			return user
		}
		return ru.Replace(user)
	})
	if got, stats, err = s.Blocks(ctx, sentence, "Russian"); err != nil || got != ru.Replace(sentence) || stats.Again != 1 || stats.Reasons["left untranslated"] != 1 || stats.Failed != 0 {
		t.Errorf("Blocks with a model that echoes = %q, %+v, %v", got, stats, err)
	}
	s = newService(t, func(_, user string) string { return user })
	if got, stats, err = s.Blocks(ctx, sentence, "Russian"); err != nil || got != sentence || stats.Again != 1 || stats.Failed != 0 {
		t.Errorf("Blocks of a text that stays = %q, %+v, %v", got, stats, err)
	}

	// A service that does not answer leaves the text and fails nothing else.
	s = newService(t, translator)
	s.Client.Key = "wrong"
	if got, stats, err = s.Blocks(ctx, sentence, "Russian"); err != nil || got != sentence || stats.Failed != 1 || stats.Reasons["request failed"] != 1 {
		t.Errorf("Blocks with a service that refuses = %q, %+v, %v", got, stats, err)
	}

	// A long text goes in several requests.
	long := strings.Repeat("<p>Hello "+strings.Repeat("x", 1400)+"</p>", 5)
	s = newService(t, translator)
	if got, stats, err = s.Blocks(ctx, long, "Russian"); err != nil || s.requests() != 3 || strings.Count(got, "Привет") != 5 {
		t.Errorf("Blocks of a long text: %d requests, %d translated, %+v, %v", s.requests(), strings.Count(got, "Привет"), stats, err)
	}
}

// T6–T10: an entry is translated step by step, each step stored; the
// translation is shown, hidden, and gone when the text of the entry changes.
func TestSteps(t *testing.T) {
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
			long := strings.Repeat("<p>Hello "+strings.Repeat("x", 1400)+"</p>", 5)
			entries := []*store.Entry{
				{FeedID: f.ID, GUID: "long", Title: "Hello, reader", Content: long, Attributes: json.RawMessage(`{"original_content":"kept"}`)},
				{FeedID: f.ID, GUID: "own", Title: "Привет", Content: "<p>Уже по-русски.</p>"},
			}
			if err := db.InsertEntries(ctx, u.ID, entries); err != nil {
				t.Fatal(err)
			}
			id, own := entries[0].ID, entries[1].ID
			stored := func() *store.Entry {
				e, err := db.EntryByID(ctx, u.ID, id)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			s := newService(t, func(system, user string) string {
				if strings.HasPrefix(system, "Answer with one word") {
					if strings.Contains(user, "Уже") {
						return "Yes"
					}
					return "no"
				}
				return ru.Replace(user)
			})
			tr, russian := New(db, s.Client), language.Russian

			if state := Of(stored(), "ru"); state.Exists || state.Content != long || state.Title != "Hello, reader" {
				t.Errorf("before anything: %+v", state)
			}
			// The first step asks for the language, the title and two paragraphs.
			state, err := tr.Step(ctx, u.ID, id, russian)
			if err != nil || !state.Exists || !state.Shown || state.Done != 2 || state.Total != 5 || state.Complete() || state.Title != "Привет, читатель" ||
				strings.Count(state.Content, "Привет") != 2 || strings.Count(state.Content, "Hello") != 3 || s.requests() != 3 {
				t.Errorf("after one step: %+v, %v, %d requests", state, err, s.requests())
			}
			if !strings.Contains(s.asked[0], "written in Russian") {
				t.Errorf("the language was asked for as %q", s.asked[0])
			}
			e := stored()
			if e.Content != long || e.Title != "Hello, reader" || !strings.Contains(string(e.Attributes), `"original_content":"kept"`) {
				t.Errorf("the entry itself changed: title %q, attributes %.200s", e.Title, e.Attributes)
			}
			if got := Of(e, "ru"); got != state {
				t.Errorf("the stored state is %+v, the step returned %+v", got, state)
			}
			if other := Of(e, "de"); other.Exists {
				t.Errorf("a translation into another language: %+v", other)
			}

			// The original and the translation, without a request.
			before := s.requests()
			if state, err = tr.Show(ctx, u.ID, id, russian, false); err != nil || !state.Exists || state.Shown || state.Content != long || state.Title != "Hello, reader" || state.Done != 2 {
				t.Errorf("the original shown: %+v, %v", state, err)
			}
			if state, err = tr.Show(ctx, u.ID, id, russian, true); err != nil || !state.Shown || strings.Count(state.Content, "Привет") != 2 || s.requests() != before {
				t.Errorf("the translation shown again: %+v, %v, %d requests more", state, err, s.requests()-before)
			}

			// The rest, without asking for the language and the title again.
			if state, err = tr.All(ctx, u.ID, id, russian); err != nil || !state.Complete() || state.Done != 5 || strings.Count(state.Content, "Привет") != 5 || s.requests() != before+2 {
				t.Errorf("after all steps: %+v, %v, %d requests more", state, err, s.requests()-before)
			}
			// Nothing is left to ask for.
			if state, err = tr.All(ctx, u.ID, id, russian); err != nil || !state.Complete() || s.requests() != before+2 {
				t.Errorf("asked again when complete: %+v, %v, %d requests more", state, err, s.requests()-before)
			}

			// The text of the entry changes: the translation is of another text.
			e = stored()
			e.Content = "<p>Bye, reader.</p>"
			if err := db.UpdateEntry(ctx, e); err != nil {
				t.Fatal(err)
			}
			if state := Of(stored(), "ru"); state.Exists || state.Content != "<p>Bye, reader.</p>" {
				t.Errorf("after the text changed: %+v", state)
			}
			if state, err = tr.Step(ctx, u.ID, id, russian); err != nil || !state.Complete() || state.Content != "<p>Пока, читатель.</p>" {
				t.Errorf("the new text translated: %+v, %v", state, err)
			}
			if strings.Contains(string(stored().Attributes), "xxxx") {
				t.Error("the translation of the old text is still stored")
			}

			// A text in the language already costs one request and stores nothing.
			before = s.requests()
			if state, err = tr.Step(ctx, u.ID, own, russian); !errors.Is(err, ErrSameLanguage) || state.Exists || s.requests() != before+1 {
				t.Errorf("a text in Russian: %+v, %v, %d requests", state, err, s.requests()-before)
			}
			if _, err = tr.Step(ctx, u.ID, 12345, russian); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("an entry there is not: %v", err)
			}
			if _, err = tr.Show(ctx, u.ID+1, id, russian, false); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("the entry of another user: %v", err)
			}
		})
	}
}
