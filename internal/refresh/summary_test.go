package refresh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/language"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// modelWorld gives the Refresher of a world a language model that answers
// "Summary." to every question, or fails when broken is set, and counts
// the questions.
func modelWorld(t *testing.T, w *world, broken *bool) func() int {
	t.Helper()
	var (
		mu    sync.Mutex
		asked int
	)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return asked
	}
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
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": "Summary."}}},
		})
	}))
	t.Cleanup(server.Close)
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	w.r.Summarise = translate.New(w.db, &translate.Client{URL: server.URL, Key: "k", Model: "m", HTTP: client}).SummariseLong
	t.Cleanup(func() { w.r.Wait() })
	return count
}

// articles serves a feed with a long entry and a short one.
func articles(w *world, path string) {
	long := strings.Repeat("Words of a long article. ", 200)
	w.serveBody(path, "application/atom+xml", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title>`+
		`<entry><id>urn:long</id><title>Long</title><updated>2026-10-06T10:00:00Z</updated><content type="text">`+long+`</content></entry>`+
		`<entry><id>urn:short</id><title>Short</title><updated>2026-10-06T10:00:00Z</updated><content type="text">A few words.</content></entry></feed>`)
}

// A feed that asks for summaries gets one for each new long entry and none
// for a short one; a feed that does not ask gets none.
func TestRefreshSummarisesLongEntries(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		broken := false
		asked := modelWorld(t, w, &broken)
		u := w.user("alice", `{"translate_to":"ru"}`)
		asking := w.feed(u, "/asking.xml", func(f *store.Feed) { f.Attributes = json.RawMessage(`{"summarise_long":true}`) })
		silent := w.feed(u, "/silent.xml", nil)
		articles(w, "/asking.xml")
		articles(w, "/silent.xml")

		w.run(Options{})
		w.r.Wait()
		summary := func(entries []*store.Entry) map[string]bool {
			got := map[string]bool{}
			for _, e := range entries {
				_, ok := translate.SummaryOf(e, language.Russian)
				got[e.Title] = ok
			}
			return got
		}
		if got := summary(w.entries(asking)); !got["Long"] || got["Short"] {
			t.Errorf("summaries of the feed that asks: %v, want the long entry alone", got)
		}
		if got := summary(w.entries(silent)); got["Long"] || got["Short"] {
			t.Errorf("summaries of the feed that does not ask: %v", got)
		}
		if asked() != 1 {
			t.Errorf("the model was asked %d times, want once", asked())
		}
		if e := w.entry(asking, "urn:long"); !strings.Contains(e.Content, "Words of a long article.") || strings.Contains(e.Content, "Summary") {
			t.Errorf("the text of the entry changed: %.80q", e.Content)
		}

		// Entries that are stored already are not summarised again.
		w.later()
		w.run(Options{Force: true})
		w.r.Wait()
		if asked() != 1 {
			t.Errorf("the model was asked %d times after the second run, want once", asked())
		}
	})
}

// A model that fails does not fail the refresh: the entries are stored as
// they came, and the failure is logged.
func TestRefreshWithFailingModel(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		broken := true
		asked := modelWorld(t, w, &broken)
		u := w.user("alice", "")
		f := w.feed(u, "/asking.xml", func(f *store.Feed) { f.Attributes = json.RawMessage(`{"summarise_long":true}`) })
		articles(w, "/asking.xml")

		st := w.runOne()
		w.r.Wait()
		if st.Failed != 0 || st.NewEntries != 2 || len(w.entries(f)) != 2 {
			t.Errorf("the refresh with a failing model: %+v, %d entries stored", st, len(w.entries(f)))
		}
		if asked() != 1 {
			t.Errorf("the model was asked %d times, want once", asked())
		}
		for _, e := range w.entries(f) {
			if _, ok := translate.SummaryOf(e, language.English); ok {
				t.Errorf("entry %q has a summary", e.Title)
			}
		}
		if logs := w.logs.String(); !strings.Contains(logs, "entries were not summarised") {
			t.Errorf("the failure was not logged:\n%s", logs)
		}
	})
}
