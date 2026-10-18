package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// U111: with a service to summarise with, an entry has a button that asks
// for a summary, which is shown above the text and kept in the attributes
// of the entry; the title and the text are not touched. A model that fails
// leaves the entry as it was and the reader is told so. Without a service
// there is neither the button nor the address.
func TestSummariseEntry(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		s.asAlice()
		ids := s.stored("alice", store.Listing{Set: mainStream()})
		first := fmt.Sprintf("/entries/%d", ids[0])
		if page := s.page(first); strings.Contains(page, "/summary") {
			t.Error("an entry offers a summary without a service to make it")
		}
		if a := s.post(first+"/summary", url.Values{}); a.status != http.StatusNotFound {
			t.Errorf("summarising without a service: status %d, want 404", a.status)
		}

		in := interpret(t, s)
		path := fmt.Sprintf("/entries/%d", in.entry.ID)
		form := url.Values{"next": {"/feeds/1?state=all"}}
		page := s.page(path)
		if !strings.Contains(page, `action="`+path+`/summary"`) || !strings.Contains(page, ">Summarise</button>") || strings.Contains(page, "entry-summary") {
			t.Errorf("the page of an entry does not offer a summary:\n%s", page)
		}

		a := s.part(http.MethodPost, path+"/summary", "entry", form)
		said, _ := url.PathUnescape(a.header.Get(noticeHeader))
		if a.status != http.StatusOK || said != "" || !strings.Contains(a.body, `<details class="entry-summary" open>`) ||
			!strings.Contains(a.body, "<p>Title: Привет, дорогой читатель") || !strings.Contains(a.body, ">Summarise again</button>") {
			t.Errorf("the summary for the script: status %d, notice %q\n%.800s", a.status, said, a.body)
		}
		if system := in.systems[len(in.systems)-1]; !strings.Contains(system, "in English") {
			t.Errorf("the summary was not asked for in the language of the interface: %q", system)
		}
		if asked := in.asked[len(in.asked)-1]; strings.Contains(asked, "<p>") {
			t.Errorf("the model was sent markup: %.100q", asked)
		}
		// The summary stands above the text, on the page of the entry too.
		page = s.page(path)
		if at, text := strings.Index(page, "entry-summary"), strings.Index(page, `class="entry-content"`); at < 0 || text < 0 || at > text {
			t.Errorf("the summary does not stand above the text: %d, %d", at, text)
		}
		// It is stored with the entry, which keeps its title and its text.
		stored := s.entry("alice", in.entry.ID)
		if stored.Title != in.entry.Title || stored.Content != in.entry.Content || !strings.Contains(string(stored.Attributes), `"summary"`) {
			t.Errorf("the stored entry: title %q, attributes %.200s", stored.Title, stored.Attributes)
		}

		// A model that fails leaves the entry, and its summary, as it was.
		before := string(stored.Attributes)
		requests := in.requests()
		in.mu.Lock()
		in.finish = "length"
		in.mu.Unlock()
		a = s.part(http.MethodPost, path+"/summary", "entry", form)
		said, _ = url.PathUnescape(a.header.Get(noticeHeader))
		if a.status != http.StatusOK || !strings.Contains(said, "could not be summarised") || in.requests() != requests+1 {
			t.Errorf("an answer cut off: status %d, notice %q", a.status, said)
		}
		if after := s.entry("alice", in.entry.ID); string(after.Attributes) != before || after.Content != in.entry.Content {
			t.Errorf("an answer cut off changed the entry: %.200s", after.Attributes)
		}
		in.mu.Lock()
		in.finish = ""
		in.mu.Unlock()

		// A plain form goes back to the entry.
		if location, body := s.follow(path+"/summary", form); location != fmt.Sprintf("/feeds/1?state=all#e%d", in.entry.ID) || !strings.Contains(body, "entry-summary") {
			t.Errorf("a plain form: at %q", location)
		}

		// A summary in another language than the reader's is shown, and can
		// be made again in theirs.
		s.setting("alice", "translate_to", "de")
		if page := s.page(path); !strings.Contains(page, "entry-summary") || !strings.Contains(page, ">Summarise again</button>") {
			t.Error("a summary in English is not shown to a reader of German")
		}
		a = s.part(http.MethodPost, path+"/summary", "entry", form)
		if system := in.systems[len(in.systems)-1]; !strings.Contains(system, "in German") || !strings.Contains(a.body, "entry-summary") {
			t.Errorf("after the language was set to German: %q", system)
		}

		// A text that changed has no summary.
		changed := s.entry("alice", in.entry.ID)
		changed.Content = "<p>Bye.</p>"
		if err := s.db.UpdateEntry(ctx, changed); err != nil {
			t.Fatal(err)
		}
		if page := s.page(path); strings.Contains(page, "entry-summary") {
			t.Error("the summary of the old text is shown with the new one")
		}

		// A service that does not answer leaves the entry and says so.
		s.h.translator = translate.New(s.db, &translate.Client{URL: "http://127.0.0.1:1", Key: "k", Model: "m", HTTP: mustFetch(t)})
		a = s.part(http.MethodPost, first+"/summary", "entry", form)
		if said, _ := url.PathUnescape(a.header.Get(noticeHeader)); a.status != http.StatusOK || !strings.Contains(said, "could not be summarised") {
			t.Errorf("a service that is down: status %d, notice %q", a.status, said)
		}
	})
}

// U111: the form of a feed asks whether its long entries are summarised
// only where there is a service to do it, and without one leaves what is
// stored.
func TestFeedSummariseSetting(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		e := s.entry("alice", s.stored("alice", store.Listing{Set: mainStream()})[0])
		target := fmt.Sprintf("/subscriptions/feeds/%d", e.FeedID)
		summarised := func() any { return decoded(t, s.feed("alice", e.FeedID).Attributes)["summarise_long"] }

		if page := s.page(target); strings.Contains(page, "summarise_long") {
			t.Errorf("the form asks about summaries without a service:\n%s", page)
		}
		form := s.feedForm(e.FeedID)
		form.Set("summarise_long", "1")
		s.follow(target, form)
		if summarised() != nil {
			t.Errorf("a feed was set to summaries without a service: %v", summarised())
		}

		interpret(t, s)
		if page := s.page(target); !strings.Contains(page, `name="summarise_long" value="1" aria-describedby`) || !strings.Contains(page, "Summarise long entries") {
			t.Errorf("the form does not ask about summaries:\n%s", page)
		}
		form = s.feedForm(e.FeedID)
		form.Set("summarise_long", "1")
		s.follow(target, form)
		if summarised() != true {
			t.Errorf("the box checked: summarise_long %v", summarised())
		}
		if page := s.page(target); !strings.Contains(page, `name="summarise_long" value="1" checked`) {
			t.Error("the form does not show the box checked")
		}
		form.Del("summarise_long")
		s.follow(target, form)
		if summarised() != nil {
			t.Errorf("the box cleared: summarise_long %v", summarised())
		}
	})
}
