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

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// suggester is a chat endpoint of the test that suggests the labels in
// answer, or has its answer cut short when down is set.
type suggester struct {
	mu     sync.Mutex
	answer string
	down   bool
	asked  []string
}

func (g *suggester) set(answer string, down bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answer, g.down = answer, down
}

func (g *suggester) requests() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.asked)
}

// suggest gives the site a service to ask for labels.
func suggest(t *testing.T, s *site, answer string) *suggester {
	t.Helper()
	g := &suggester{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.asked = append(g.asked, request.Messages[1].Content)
		answer, down := g.answer, g.down
		g.mu.Unlock()
		finish := "stop"
		if down {
			finish = "length"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"finish_reason": finish, "message": map[string]string{"content": answer}}}})
	}))
	t.Cleanup(server.Close)
	s.h.translator = translate.New(s.db, &translate.Client{URL: server.URL, Key: "secret", Model: "m", HTTP: mustFetch(t)})
	return g
}

// U112: with a service to suggest with, an entry has a button that asks for
// labels; the names the model gives are shown with the entry, each with a
// button that adds it, and a button that drops them all; nothing is
// attached or made until the reader adds a name. Without a service there is
// neither the button nor the address.
func TestSuggestLabelsForEntry(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		s.asAlice()
		alice := s.user("alice")
		id := s.stored("alice", store.Listing{Set: mainStream()})[0]
		path := fmt.Sprintf("/entries/%d", id)
		if page := s.page(path); strings.Contains(page, "label-suggestions") {
			t.Error("an entry offers label suggestions without a service to make them")
		}
		if a := s.post(path+"/label-suggestions", url.Values{}); a.status != http.StatusNotFound {
			t.Errorf("suggesting without a service: status %d, want 404", a.status)
		}

		if err := s.db.CreateTag(ctx, &store.Tag{UserID: alice.ID, Name: "Existing"}); err != nil {
			t.Fatal(err)
		}
		g := suggest(t, s, "Existing\nFresh\nClash")
		labelsOf := func() []string {
			labels, err := s.db.EntryLabels(ctx, alice.ID, []int64{id})
			if err != nil {
				t.Fatal(err)
			}
			return labels[id]
		}
		tags := func() int {
			all, err := s.db.Tags(ctx, alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			return len(all)
		}
		base := tags()
		here := fmt.Sprintf("/feeds/%d?state=all", s.entry("alice", id).FeedID)
		form := url.Values{"next": {here}}
		if page := s.page(path); !strings.Contains(page, `action="`+path+`/label-suggestions"`) || !strings.Contains(page, ">Suggest labels</button>") ||
			strings.Contains(page, "entry-suggestions") {
			t.Errorf("the page of an entry does not offer to suggest labels:\n%s", page)
		}

		// The names are shown, and nothing is attached or made.
		a := s.part(http.MethodPost, path+"/label-suggestions", "entry", form)
		if said, _ := url.PathUnescape(a.header.Get(noticeHeader)); a.status != http.StatusOK || said != "" ||
			!strings.Contains(a.body, `<div class="entry-suggestions">`) || !strings.Contains(a.body, "<span>Existing</span>") ||
			!strings.Contains(a.body, "<span>Fresh</span>") || strings.Count(a.body, ">Add</button>") != 3 || !strings.Contains(a.body, ">Dismiss</button>") {
			t.Errorf("the suggestions for the script: status %d, notice %q\n%.1500s", a.status, said, a.body)
		}
		if len(labelsOf()) != 0 || tags() != base {
			t.Errorf("a suggestion was applied: labels %v, %d labels of the user (%d before)", labelsOf(), tags(), base)
		}
		if !strings.Contains(g.asked[0], "\nExisting\n") {
			t.Errorf("the model was not told the labels of the user: %.200q", g.asked[0])
		}
		// The page of the entry shows them too.
		if page := s.page(path); !strings.Contains(page, "<span>Fresh</span>") {
			t.Error("the page of the entry does not show the suggestions")
		}

		// Add makes the label when there is none and attaches it; the name goes from the suggestions.
		a = s.part(http.MethodPost, path+"/label-suggestions/add", "entry", url.Values{"name": {"Fresh"}, "next": form["next"]})
		if said, _ := url.PathUnescape(a.header.Get(noticeHeader)); a.status != http.StatusOK || said != "Label added." ||
			strings.Contains(a.body, "<span>Fresh</span>") || !strings.Contains(a.body, "<span>Existing</span>") || !strings.Contains(a.body, "Fresh") {
			t.Errorf("adding a new label: status %d, notice %q\n%.1500s", a.status, said, a.body)
		}
		if got := labelsOf(); len(got) != 1 || got[0] != "Fresh" || tags() != base+1 {
			t.Errorf("after adding Fresh: labels %v, %d labels of the user", got, tags())
		}
		// A label the user has is attached as it is.
		s.part(http.MethodPost, path+"/label-suggestions/add", "entry", url.Values{"name": {"Existing"}})
		if got := labelsOf(); strings.Join(got, ",") != "Existing,Fresh" && strings.Join(got, ",") != "Fresh,Existing" || tags() != base+1 {
			t.Errorf("after adding Existing: labels %v, %d labels of the user", got, tags())
		}
		// A name the model did not suggest is not added.
		s.part(http.MethodPost, path+"/label-suggestions/add", "entry", url.Values{"name": {"Invented"}})
		if tags() != base+1 || len(labelsOf()) != 2 {
			t.Errorf("a name that was not suggested was added: %v, %d labels", labelsOf(), tags())
		}
		// A category made meanwhile has the name: the label is not made and the name stays.
		if err := s.db.CreateCategory(ctx, &store.Category{UserID: alice.ID, Name: "Clash"}); err != nil {
			t.Fatal(err)
		}
		a = s.part(http.MethodPost, path+"/label-suggestions/add", "entry", url.Values{"name": {"Clash"}})
		if said, _ := url.PathUnescape(a.header.Get(noticeHeader)); !strings.Contains(said, "A category has this name") || !strings.Contains(a.body, "<span>Clash</span>") || tags() != base+1 {
			t.Errorf("a name of a category: notice %q, %d labels\n%.800s", said, tags(), a.body)
		}

		// Dismiss drops what is left.
		a = s.part(http.MethodPost, path+"/label-suggestions/dismiss", "entry", form)
		if a.status != http.StatusOK || strings.Contains(a.body, "entry-suggestions") {
			t.Errorf("dismissing: status %d\n%.800s", a.status, a.body)
		}
		if e := s.entry("alice", id); strings.Contains(string(e.Attributes), "label_suggestions") || len(labelsOf()) != 2 {
			t.Errorf("after dismissing: attributes %.200s, labels %v", e.Attributes, labelsOf())
		}

		// A model that fails leaves the entry as it was, and the reader is told so.
		before := string(s.entry("alice", id).Attributes)
		requests := g.requests()
		g.set("", true)
		a = s.part(http.MethodPost, path+"/label-suggestions", "entry", form)
		said, _ := url.PathUnescape(a.header.Get(noticeHeader))
		if a.status != http.StatusOK || !strings.Contains(said, "No labels could be suggested") || g.requests() != requests+1 {
			t.Errorf("a service that is down: status %d, notice %q", a.status, said)
		}
		if after := s.entry("alice", id); string(after.Attributes) != before || strings.Contains(a.body, "entry-suggestions") {
			t.Errorf("a failure changed the entry: %.200s", after.Attributes)
		}

		// A plain form goes back to the entry.
		g.set("Plain", false)
		if location, body := s.follow(path+"/label-suggestions", form); location != fmt.Sprintf("%s#e%d", here, id) || !strings.Contains(body, "<span>Plain</span>") {
			t.Errorf("a plain form: at %q, entry in page %v, suggestions in page %v", location, strings.Contains(body, fmt.Sprintf(`id="e%d"`, id)), strings.Contains(body, "entry-suggestions"))
		}
	})
}

// U112: the filters that ask for label suggestions are on the page of the
// reading settings and of a feed only where there is a service, and are
// kept beside the other filters of the same searches.
func TestSuggestFilterSettings(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		e := s.entry("alice", s.stored("alice", store.Listing{Set: mainStream()})[0])
		target := fmt.Sprintf("/subscriptions/feeds/%d", e.FeedID)
		filters := func(raw json.RawMessage) string {
			var all struct{ Filters json.RawMessage }
			_ = json.Unmarshal(raw, &all)
			return string(all.Filters)
		}

		if strings.Contains(s.page("/settings/reading"), "filters_suggest") || strings.Contains(s.page(target), "filters_suggest") {
			t.Error("the forms ask for label suggestions without a service")
		}
		form := s.formAt("/settings/reading", "/settings/reading")
		form.Set("filters_suggest", "intitle:go")
		s.follow("/settings/reading", form)
		if strings.Contains(filters(s.user("alice").Settings), "suggest") {
			t.Error("a filter was stored without a service")
		}

		suggest(t, s, "")
		form = s.formAt("/settings/reading", "/settings/reading")
		form.Set("filters_read", "intitle:old")
		form.Set("filters_suggest", "intitle:go\nintitle:old")
		s.follow("/settings/reading", form)
		if got := filters(s.user("alice").Settings); !strings.Contains(got, `{"search":"intitle:go","actions":["suggest"]}`) ||
			!strings.Contains(got, `{"search":"intitle:old","actions":["read","suggest"]}`) {
			t.Errorf("filters of the user: %s", got)
		}
		if page := s.page("/settings/reading"); !strings.Contains(page, "Ask the model for labels for the entries that match") || !strings.Contains(page, "intitle:old\nintitle:go") {
			t.Errorf("the settings do not show the filter:\n%s", page[max(0, strings.Index(page, "filters_suggest")-300):])
		}
		form.Set("filters_suggest", "")
		s.follow("/settings/reading", form)
		if got := filters(s.user("alice").Settings); strings.Contains(got, "suggest") || !strings.Contains(got, "intitle:old") {
			t.Errorf("filters of the user after clearing: %s", got)
		}

		feed := s.feedForm(e.FeedID)
		feed.Set("filters_suggest", "intitle:rust")
		s.follow(target, feed)
		if got := filters(s.feed("alice", e.FeedID).Attributes); !strings.Contains(got, `{"search":"intitle:rust","actions":["suggest"]}`) {
			t.Errorf("filters of the feed: %s", got)
		}
		if page := s.page(target); !strings.Contains(page, "intitle:rust") {
			t.Errorf("the form of the feed does not show the filter:\n%s", page)
		}
	})
}
