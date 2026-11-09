package greader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

func translationFixture(t *testing.T, w *world, model http.HandlerFunc) (*store.Entry, string, clientTranslation) {
	t.Helper()
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	w.handler.translator = translate.New(w.db, &translate.Client{URL: server.URL, Key: "fixture", Model: "fixture", HTTP: client})
	f := w.feed(w.alice, &store.Feed{Name: "Feed"})
	e := &store.Entry{FeedID: f.ID, Title: "Hello reader", Content: "<p>Hello reader</p>"}
	w.entries(w.alice, e)
	path := "/api/freshgo/translation/entry?id=" + strconv.FormatInt(e.ID, 10) + "&language=ru"
	var initial clientTranslation
	w.json(path, &initial)
	return e, path, initial
}

func translationForm(initial clientTranslation) string {
	return url.Values{"id": {initial.ID}, "language": {"ru"}, "revision": {initial.Revision}, "done": {"0"}}.Encode()
}

func TestTranslationServiceFailureAndInputRefusal(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		var calls atomic.Int64
		e, path, initial := translationFixture(t, w, func(out http.ResponseWriter, _ *http.Request) { calls.Add(1); http.Error(out, "fixture refusal", http.StatusServiceUnavailable) })
		for _, query := range []string{"id=0&language=ru", "id=no&language=ru", "id=" + initial.ID + "&language=und", "id=" + initial.ID + "&language=not_a_tag!"} {
			if code, _ := w.get(w.auth, "/api/freshgo/translation/entry?"+query); code != 400 {
				t.Errorf("invalid query %q = %d", query, code)
			}
		}
		for _, body := range []string{"id=" + initial.ID + "&language=ru&done=-1&revision=" + initial.Revision, "id=" + initial.ID + "&language=ru&done=0"} {
			if code, _ := w.post(w.auth, "/api/freshgo/translation/entry", body); code != 400 {
				t.Errorf("invalid POST = %d", code)
			}
		}
		_ = w.user("bob", "")
		bob := w.login("bob")
		if code, _ := w.post(bob, "/api/freshgo/translation/entry", translationForm(initial)); code != 404 {
			t.Fatalf("foreign POST = %d", code)
		}
		if calls.Load() != 0 {
			t.Fatal("invalid/foreign request reached model")
		}
		if code, _ := w.post(w.auth, "/api/freshgo/translation/entry", translationForm(initial)); code != 502 {
			t.Fatalf("model failure = %d", code)
		}
		var after clientTranslation
		w.json(path, &after)
		if after.Exists || after.Complete {
			t.Fatalf("failed translation became complete: %+v", after)
		}
		stored, err := w.db.EntryByID(context.Background(), w.alice.ID, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Content != e.Content || stored.Title != e.Title {
			t.Fatal("model failure changed original")
		}
	})
}

func TestTranslationConcurrentReplayAndChangedSourceInFlight(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				var calls atomic.Int64
				var enteredOnce sync.Once
				entered, release := make(chan struct{}), make(chan struct{})
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				e, _, initial := translationFixture(t, w, func(out http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var request struct{ Messages []struct{ Content string } }
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					answer := "No"
					if !strings.HasPrefix(request.Messages[0].Content, "Answer with one word") {
						answer = strings.ReplaceAll(request.Messages[1].Content, "Hello", "Привет")
						if strings.Contains(answer, "[[") {
							enteredOnce.Do(func() { close(entered) })
							<-release
						}
					}
					if err := json.NewEncoder(out).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": answer}}}}); err != nil {
						t.Error(err)
					}
				})
				result := make(chan int, 2)
				request := func() {
					r := httptest.NewRequest(http.MethodPost, "http://example.test/api/freshgo/translation/entry", strings.NewReader(translationForm(initial)))
					r.Header.Set("Authorization", w.auth)
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					out := httptest.NewRecorder()
					w.handler.ServeHTTP(out, r)
					result <- out.Code
				}
				go request()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("model not reached")
				}
				if changed {
					e.Content = "<p>New source</p>"
					if err := w.db.UpdateEntry(context.Background(), e); err != nil {
						t.Fatal(err)
					}
				} else {
					go request()
				}
				close(release)
				n, want := 2, 200
				if changed {
					n, want = 1, 409
				}
				for range n {
					select {
					case got := <-result:
						if got != want {
							t.Fatalf("concurrent/source-change status %d, want %d", got, want)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("request did not finish")
					}
				}
				if calls.Load() != 3 {
					t.Fatalf("model calls = %d, want one language/title/body cycle (3)", calls.Load())
				}
			})
		})
	}
}
