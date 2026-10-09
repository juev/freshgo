package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// interpreter is a chat endpoint of the test that translates a few words
// into Russian, and the entry of alice it is tried on.
type interpreter struct {
	mu sync.Mutex
	// asked are the messages of the user; systems the instructions.
	asked, systems []string
	// hold, when set, is waited for before each answer but the first two.
	hold chan struct{}
	// entry is long enough to take three requests.
	entry *store.Entry
}

var russian = strings.NewReplacer("Hello", "Привет", "dear", "дорогой", "reader", "читатель")

// interpret gives the site a service to translate with.
func interpret(t *testing.T, s *site) *interpreter {
	t.Helper()
	in := &interpreter{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		system, user := request.Messages[0].Content, request.Messages[1].Content
		in.mu.Lock()
		in.asked, in.systems = append(in.asked, user), append(in.systems, system)
		n, hold := len(in.asked), in.hold
		in.mu.Unlock()
		if hold != nil && n > 3 {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		answer := russian.Replace(user)
		if strings.HasPrefix(system, "Answer with one word") {
			answer = "no"
			if strings.Contains(user, "Уже") {
				answer = "yes"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": answer}}}})
	}))
	t.Cleanup(server.Close)
	client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	s.h.translator = translate.New(s.db, &translate.Client{URL: server.URL, Key: "secret", Model: "m", HTTP: client})
	in.entry = &store.Entry{FeedID: 1, GUID: "to translate", Title: "Hello, dear reader", Link: "https://example.org/hello",
		Content: strings.Repeat("<p>Hello "+strings.Repeat("x", 1400)+"</p>", 5)}
	own := &store.Entry{FeedID: 1, GUID: "translated", Title: "Привет", Content: "<p>Уже по-русски.</p>"}
	if err := s.db.InsertEntries(context.Background(), s.user("alice").ID, []*store.Entry{in.entry, own}); err != nil {
		t.Fatal(err)
	}
	return in
}

func (in *interpreter) requests() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.asked)
}

// U98: an entry is translated on request, part by part for the script and
// whole for a plain form; the translation is shown in place of the text
// and gives way to the original and back without another request.
func TestTranslateEntry(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		ids := s.stored("alice", store.Listing{Set: mainStream()})
		first := fmt.Sprintf("/entries/%d", ids[0])
		// Without a service nothing offers to translate, and nothing does.
		if page := s.page(first); strings.Contains(page, "/translate") {
			t.Error("an entry offers to be translated without a service to do it")
		}
		if a := s.post(first+"/translate", url.Values{"do": {"translate"}}); a.status != http.StatusNotFound {
			t.Errorf("translating without a service: status %d, want 404", a.status)
		}
		if page := s.page("/settings/reading"); strings.Contains(page, "translate_to") {
			t.Error("the settings ask for a language to translate into without a service")
		}

		in := interpret(t, s)
		path := fmt.Sprintf("/entries/%d", in.entry.ID)
		form := func(do string) url.Values { return url.Values{"do": {do}, "next": {"/feeds/1?state=all"}} }
		if page := s.page(path); !strings.Contains(page, `action="`+path+`/translate"`) || !strings.Contains(page, `name="do" value="translate"`) ||
			!strings.Contains(page, ">Translate</button>") {
			t.Errorf("the page of an entry does not offer to translate it:\n%s", page)
		}

		// For the script: the next paragraphs, and how far it is.
		a := s.part(http.MethodPost, path+"/translate", "entry", form("translate"))
		if a.status != http.StatusOK || a.header.Get(progressHeader) != "2/5" || strings.Count(a.body, "<p>Привет") != 2 || strings.Count(a.body, "<p>Hello") != 3 ||
			!strings.Contains(a.body, ">Привет, дорогой читатель</h2>") || !strings.Contains(a.body, `data-percent="40">Translate on (40%)</button>`) {
			t.Errorf("one step for the script: status %d, progress %q\n%.600s", a.status, a.header.Get(progressHeader), a.body)
		}
		if !strings.Contains(in.systems[0], "one word") || !strings.Contains(in.asked[0], "written in English") {
			t.Errorf("the language was asked for as %q", in.asked[0])
		}
		// The entry itself keeps its text: other clients read the original.
		if e := s.entry("alice", in.entry.ID); e.Content != in.entry.Content || e.Title != "Hello, dear reader" {
			t.Errorf("the stored entry changed: %q", e.Title)
		}

		// A plain form waits for the rest.
		location, body := s.follow(path+"/translate", form("translate"))
		if location != fmt.Sprintf("/feeds/1?state=all#e%d", in.entry.ID) || strings.Count(body, "<p>Привет") != 5 ||
			!strings.Contains(body, `name="do" value="original"`) || !strings.Contains(body, ">Show the original</button>") {
			t.Errorf("the rest by a plain form: at %q\n%.600s", location, body)
		}
		// The original and back, without asking the service.
		before := in.requests()
		a = s.part(http.MethodPost, path+"/translate", "entry", form("original"))
		if strings.Contains(a.body, "Привет") || !strings.Contains(a.body, ">Hello, dear reader</h2>") || !strings.Contains(a.body, ">Show the translation</button>") {
			t.Errorf("the original shown:\n%.600s", a.body)
		}
		a = s.part(http.MethodPost, path+"/translate", "entry", form("translation"))
		if strings.Count(a.body, "<p>Привет") != 5 || in.requests() != before {
			t.Errorf("the translation shown again: %d requests more\n%.300s", in.requests()-before, a.body)
		}

		// An entry in the language already says so and stays as it is.
		own := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 1}})
		var same int64
		for _, id := range own {
			if s.entry("alice", id).GUID == "translated" {
				same = id
			}
		}
		a = s.part(http.MethodPost, fmt.Sprintf("/entries/%d/translate", same), "entry", form("translate"))
		if said, _ := url.PathUnescape(a.header.Get(noticeHeader)); a.status != http.StatusOK || !strings.Contains(said, "in that language already") || !strings.Contains(a.body, ">Translate</button>") {
			t.Errorf("an entry in the language already: status %d, notice %q", a.status, said)
		}

		// The language is the one the reader set, when there is one.
		page := s.page("/settings/reading")
		if !strings.Contains(page, `name="translate_to" value=""`) {
			t.Errorf("the settings do not ask for the language to translate into")
		}
		settings := s.formAt("/settings/reading", "/settings/reading")
		settings.Set("translate_to", "no such language")
		if a := s.post("/settings/reading", settings); a.status != http.StatusBadRequest || !strings.Contains(a.body, "There is no such language tag.") {
			t.Errorf("a language there is not: status %d", a.status)
		}
		settings.Set("translate_to", " de ")
		s.follow("/settings/reading", settings)
		if a := s.part(http.MethodPost, path+"/translate", "entry", form("translate")); !strings.Contains(in.asked[len(in.asked)-1], "[[1]]") ||
			!strings.Contains(in.systems[len(in.systems)-1], "Translate into German") || a.header.Get(progressHeader) != "2/5" {
			t.Errorf("after the language was set to German: instruction %q, progress %q", in.systems[len(in.systems)-1], a.header.Get(progressHeader))
		}

		// A service that does not answer leaves the entry and says so.
		s.h.translator = translate.New(s.db, &translate.Client{URL: "http://127.0.0.1:1", Key: "k", Model: "m", HTTP: mustFetch(t)})
		a = s.part(http.MethodPost, first+"/translate", "entry", form("translate"))
		if said, _ := url.PathUnescape(a.header.Get(noticeHeader)); a.status != http.StatusOK || !strings.Contains(said, "could not be translated") {
			t.Errorf("a service that is down: status %d, notice %q", a.status, said)
		}
	})
}

func mustFetch(t *testing.T) *fetch.Client {
	t.Helper()
	client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
