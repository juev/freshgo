package greader

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

func TestTranslationSameLanguageIsReusable(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		var calls atomic.Int64
		model := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if err := json.NewEncoder(out).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": "Yes"}}}}); err != nil {
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
		e := &store.Entry{FeedID: f.ID, Title: "Привет", Content: "<p>Уже по-русски.</p>"}
		w.entries(w.alice, e)
		path := "/api/freshgo/translation/entry?id=" + strconv.FormatInt(e.ID, 10) + "&language=ru"
		var current clientTranslation
		w.json(path, &current)
		form := url.Values{"id": {current.ID}, "language": {"ru"}, "revision": {current.Revision}, "done": {"0"}}.Encode()
		code, body := w.post(w.auth, "/api/freshgo/translation/entry", form)
		if code != 200 {
			t.Fatalf("same language status = %d %s", code, body)
		}
		if err := json.Unmarshal([]byte(body), &current); err != nil {
			t.Fatal(err)
		}
		if !current.SameLanguage {
			t.Fatalf("same-language result lost = %+v", current)
		}
		before := calls.Load()
		w.json(path, &current)
		if !current.SameLanguage {
			t.Fatal("same-language result was not stored")
		}
		if code, _ := w.post(w.auth, "/api/freshgo/translation/entry", form); code != 200 {
			t.Fatalf("replay = %d", code)
		}
		if calls.Load() != before {
			t.Fatal("same-language replay repeated paid work")
		}
	})
}
