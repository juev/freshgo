package greader

import (
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
	"golang.org/x/text/language"
)

type clientTranslation struct {
	ID           string `json:"id"`
	Language     string `json:"language"`
	SourceHash   string `json:"sourceHash"`
	Revision     string `json:"revision"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	Exists       bool   `json:"exists"`
	Complete     bool   `json:"complete"`
	SameLanguage bool   `json:"sameLanguage"`
	Done         int    `json:"done"`
	Total        int    `json:"total"`
	Failed       int    `json:"failed"`
}

func TestTranslationRoundTrip(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		var calls atomic.Int64
		model := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var q struct{ Messages []struct{ Content string } }
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Error(err)
				out.WriteHeader(400)
				return
			}
			answer := "No"
			if !strings.HasPrefix(q.Messages[0].Content, "Answer with one word") {
				answer = strings.ReplaceAll(q.Messages[1].Content, "Hello", "Привет")
			}
			if err := json.NewEncoder(out).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": answer}}}}); err != nil {
				t.Error(err)
			}
		}))
		t.Cleanup(model.Close)
		httpClient, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
		if err != nil {
			t.Fatal(err)
		}
		w.handler.translator = translate.New(w.db, &translate.Client{URL: model.URL, Key: "fixture", Model: "fixture", HTTP: httpClient})
		f := w.feed(w.alice, &store.Feed{Name: "Feed"})
		e := &store.Entry{FeedID: f.ID, Title: "Hello reader", Content: strings.Repeat("<p>Hello "+strings.Repeat("x", 500)+"</p>", 5)}
		w.entries(w.alice, e)
		path := Alias + "/api/freshgo/translation/entry?id=" + strconv.FormatInt(e.ID, 10) + "&language=ru"
		var current clientTranslation
		w.json(path, &current)
		if current.Exists || current.Revision == "" || current.SourceHash == "" || current.ID != strconv.FormatInt(e.ID, 10) {
			t.Fatalf("initial representation = %+v", current)
		}
		initial := current
		for i := 0; i < 20 && !current.Complete; i++ {
			form := url.Values{"id": {current.ID}, "language": {"ru"}, "revision": {current.Revision}, "done": {strconv.Itoa(current.Done)}}.Encode()
			code, body := w.post(w.auth, Alias+"/api/freshgo/translation/entry", form)
			if code != 200 {
				t.Fatalf("step = %d %s", code, body)
			}
			if err := json.Unmarshal([]byte(body), &current); err != nil {
				t.Fatal(err)
			}
			count := calls.Load()
			code, body = w.post(w.auth, Alias+"/api/freshgo/translation/entry", form)
			if code != 200 || calls.Load() != count {
				t.Fatalf("replayed committed step = %d %s, calls %d -> %d", code, body, count, calls.Load())
			}
		}
		if !current.Complete || current.Failed != 0 || strings.Count(current.Content, "Привет") != 5 || current.Title != "Привет reader" {
			t.Fatalf("final = %+v", current)
		}
		before := calls.Load()
		if _, err := w.handler.translator.Show(context.Background(), w.alice.ID, e.ID, language.Russian, false); err != nil {
			t.Fatal(err)
		}
		var cached clientTranslation
		w.json(path, &cached)
		if cached.Content != current.Content || !cached.Complete || calls.Load() != before {
			t.Fatalf("cached hidden translation = %+v", cached)
		}
		form := url.Values{"id": {current.ID}, "language": {"ru"}, "revision": {initial.Revision}, "done": {"0"}}.Encode()
		if code, body := w.post(w.auth, "/api/freshgo/translation/entry", form); code != 200 {
			t.Fatalf("cached POST %d %s", code, body)
		}
		stored, err := w.db.EntryByID(context.Background(), w.alice.ID, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if translate.Of(stored, "ru").Shown {
			t.Fatal("API request changed web Show original preference")
		}
		item := w.items(e.ID)[0]
		if item.Title != e.Title || item.Summary.Content != e.Content {
			t.Fatal("Google Reader source was replaced")
		}
		w.user("bob", "")
		if code, _ := w.get(w.login("bob"), path); code != 404 {
			t.Fatalf("foreign entry = %d, want 404", code)
		}
		stored.Content += "<p>changed</p>"
		if err := w.db.UpdateEntry(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
		if code, body := w.post(w.auth, "/api/freshgo/translation/entry", form); code != 409 {
			t.Fatalf("stale source = %d %s, want 409", code, body)
		}
		if calls.Load() != before {
			t.Fatalf("cached/foreign/stale requests reached model: %d -> %d", before, calls.Load())
		}

	})
}
