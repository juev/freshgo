package refresh

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

const articlePage = `<html><head><title>Page</title></head><body>
<nav>Menu</nav>
<article><h1>Headline</h1><div class="body"><p>Full text with a <a href="more.html">link</a>.</p><div class="ad">Buy</div></div></article>
</body></html>`

const (
	fullText         = `<div data-sanitized-class="body"><p>Full text with a <a href="%s/articles/more.html">link</a>.</p><div data-sanitized-class="ad">Buy</div></div>`
	fullTextFiltered = `<div data-sanitized-class="body"><p>Full text with a <a href="%s/articles/more.html">link</a>.</p></div>`
)

func originalContent(t *testing.T, e *store.Entry) (content string, kept bool) {
	t.Helper()
	var attrs map[string]json.RawMessage
	if err := json.Unmarshal(e.Attributes, &attrs); err != nil {
		t.Fatal(err)
	}
	raw, kept := attrs["original_content"]
	if kept {
		if err := json.Unmarshal(raw, &content); err != nil {
			t.Fatal(err)
		}
	}
	return content, kept
}

// R12: the text of an article is taken from its page.
func TestFullText(t *testing.T) {
	cases := []struct {
		name           string
		feedAttributes string
		wantContent    string
		// wantOriginal is the text of the feed kept aside, empty for none.
		wantOriginal string
	}{
		{"replaces the summary", `{}`, fullContentStart + fullText + fullContentEnd, "Summary"},
		{"replace by name", `{"content_action":"replace"}`, fullContentStart + fullText + fullContentEnd, "Summary"},
		{"after the summary", `{"content_action":"append"}`, "Summary" + fullContentStart + fullText + fullContentEnd, ""},
		{"before the summary", `{"content_action":"prepend"}`, fullContentStart + fullText + fullContentEnd + "Summary", ""},
		{"without the unwanted elements", `{"path_entries_filter":".ad, .absent"}`, fullContentStart + fullTextFiltered + fullContentEnd, "Summary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				u := w.user("alice", `{}`)
				w.serveBody("/articles/one", "text/html; charset=utf-8", articlePage)
				w.serveBody("/feed", rssType, rss("Blog", item{guid: "a", title: "First", link: w.server.URL + "/articles/one", body: "Summary"}))
				f := w.feed(u, "/feed", func(f *store.Feed) {
					f.PathEntries, f.Attributes = "article .body", json.RawMessage(c.feedAttributes)
				})
				if st := w.runOne(); st.NewEntries != 1 || st.Failed != 0 {
					t.Fatalf("stats %+v", st)
				}
				e := w.entry(f, "a")
				if want := strings.ReplaceAll(c.wantContent, "%s", w.server.URL); e.Content != want {
					t.Errorf("content\n got %q\nwant %q", e.Content, want)
				}
				if original, kept := originalContent(t, e); original != c.wantOriginal || kept != (c.wantOriginal != "") {
					t.Errorf("original content %q, kept %v; want %q", original, kept, c.wantOriginal)
				}

				// The feed has not changed: the entry is not taken as changed
				// because of its new text, and the page is left alone.
				w.later()
				if st := w.runOne(); st.UpdatedEntries != 0 || st.NewEntries != 0 {
					t.Errorf("second refresh: stats %+v", st)
				}
				if hits := w.hitCount("/articles/one"); hits != 1 {
					t.Errorf("the page was requested %d times, want 1", hits)
				}

				// The entry changes in the feed: the page is read again.
				w.serveBody("/feed", rssType, rss("Blog", item{guid: "a", title: "First", link: w.server.URL + "/articles/one", body: "Summary, corrected"}))
				w.later()
				if st := w.runOne(); st.UpdatedEntries != 1 {
					t.Errorf("third refresh: stats %+v", st)
				}
				if hits := w.hitCount("/articles/one"); hits != 2 {
					t.Errorf("the page was requested %d times, want 2", hits)
				}
				want := strings.ReplaceAll(strings.ReplaceAll(c.wantContent, "Summary", "Summary, corrected"), "%s", w.server.URL)
				if got := w.entry(f, "a").Content; got != want {
					t.Errorf("content after the change\n got %q\nwant %q", got, want)
				}
			})
		})
	}
}

// readablePage is a page as sites make them: the article among a menu, a
// column of links and a footer.
var readablePage = func() string {
	paragraph := strings.Repeat("The harbour was quiet that morning, and the boats lay still on the water. ", 4)
	return `<html><head><title>Harbour news</title></head><body>
<nav><ul><li><a href="/">Home</a></li><li><a href="/about">About the site</a></li></ul></nav>
<div id="content"><article><h1>Boats at rest</h1>
<p>First. ` + paragraph + `</p>
<div class="promo"><p>Subscribe to the newsletter of the harbour, every week in your mailbox.</p></div>
<p>Second. ` + paragraph + `</p>
<p>Third. ` + paragraph + `</p>
</article></div>
<aside><h2>Most read</h2><ul><li><a href="/a">Tides of the week</a></li><li><a href="/b">Fish prices</a></li></ul></aside>
<footer><p>Copyright of the harbour gazette.</p></footer>
</body></html>`
}()

// T11: a feed set to find the article without a selector stores the text
// of the page like a feed with a selector does, whatever its selector says.
func TestFullTextAutomatic(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serveBody("/articles/one", "text/html; charset=utf-8", readablePage)
		w.serveBody("/articles/bare", "text/html; charset=utf-8", `<html><head><title>Bare</title></head><body><nav></nav></body></html>`)
		link := func(page string) string { return w.server.URL + "/articles/" + page }
		w.serveBody("/feed", rssType, rss("Blog",
			item{guid: "bare", title: "Bare", link: link("bare"), body: "Summary"},
			item{guid: "nolink", title: "No link", body: "Summary"},
			item{guid: "a", title: "First", link: link("one"), body: "Summary"}))
		f := w.feed(u, "/feed", func(f *store.Feed) {
			f.PathEntries, f.Attributes = "p[", json.RawMessage(`{"path_entries_auto":true,"path_entries_filter":".promo"}`)
		})
		if st := w.runOne(); st.NewEntries != 3 || st.Failed != 0 {
			t.Fatalf("stats %+v", st)
		}
		e := w.entry(f, "a")
		if !strings.HasPrefix(e.Content, fullContentStart) || !strings.HasSuffix(e.Content, fullContentEnd) {
			t.Errorf("the text of the page is not between its markers: %q", e.Content)
		}
		for _, want := range []string{"First. The harbour", "Second. The harbour", "Third. The harbour"} {
			if !strings.Contains(e.Content, want) {
				t.Errorf("the content lacks %q: %q", want, e.Content)
			}
		}
		for _, unwanted := range []string{"About the site", "Tides of the week", "Copyright of the harbour", "Subscribe"} {
			if strings.Contains(e.Content, unwanted) {
				t.Errorf("the content holds %q: %q", unwanted, e.Content)
			}
		}
		if original, kept := originalContent(t, e); original != "Summary" || !kept {
			t.Errorf("original content %q, kept %v; want the summary", original, kept)
		}
		// No article, no link: the text of the feed stays.
		for _, guid := range []string{"bare", "nolink"} {
			if got := w.entry(f, guid).Content; got != "Summary" {
				t.Errorf("%s: content %q, want the summary", guid, got)
			}
		}

		if logs := w.logs.String(); !strings.Contains(logs, "article page was not read") {
			t.Errorf("a page without an article is not in the log: %s", logs)
		}

		// The feed has not changed: no page is read again.
		w.later()
		if st := w.runOne(); st.UpdatedEntries != 0 || st.NewEntries != 0 {
			t.Errorf("second refresh: stats %+v", st)
		}
		if hits := w.hitCount("/articles/one"); hits != 1 {
			t.Errorf("the page was requested %d times, want 1", hits)
		}
	})
}

// R12: pages are read only for the entries a condition asks for, and a page
// that cannot be read leaves the text of the feed.
func TestFullTextConditionsAndFailures(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serveBody("/articles/long", "text/html", articlePage)
		w.serveBody("/articles/short", "text/html", articlePage)
		w.serveBody("/articles/elsewhere", "text/html", `<html><body><p>No article here.</p></body></html>`)
		w.serve("/articles/moved", func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "text/html")
			_, _ = rw.Write([]byte(`<html><head><meta http-equiv="refresh" content="0; url=long"></head><body></body></html>`))
		})
		link := func(page string) string { return w.server.URL + "/articles/" + page }
		w.serveBody("/feed", rssType, rss("Blog",
			item{guid: "nolink", title: "Read more: no link", body: "Summary"},
			item{guid: "moved", title: "Read more: moved", link: link("moved"), body: "Summary"},
			item{guid: "elsewhere", title: "Read more: nothing to pick", link: link("elsewhere"), body: "Summary"},
			item{guid: "gone", title: "Read more: gone", link: link("gone"), body: "Summary"},
			item{guid: "short", title: "Complete already", link: link("short"), body: "Summary"},
			item{guid: "long", title: "Read more: long", link: link("long"), body: "Summary"}))
		// The second condition has a regular expression Go does not take.
		f := w.feed(u, "/feed", func(f *store.Feed) {
			f.PathEntries = "article .body"
			f.Attributes = json.RawMessage(`{"path_entries_conditions":["intitle:\"read more\"", "intitle:/(a)\\1/", " "]}`)
		})
		// A feed whose only condition cannot be used reads no pages at all.
		w.serveBody("/articles/never", "text/html", articlePage)
		w.serveBody("/other", rssType, rss("Other", item{guid: "never", title: "Read more", link: link("never"), body: "Summary"}))
		other := w.feed(u, "/other", func(f *store.Feed) {
			f.PathEntries = "article .body"
			f.Attributes = json.RawMessage(`{"path_entries_conditions":["intitle:/(a)\\1/"]}`)
		})
		if st := w.runOne(); st.NewEntries != 7 || st.Failed != 0 {
			t.Fatalf("stats %+v", st)
		}
		if got := w.entry(other, "never").Content; got != "Summary" || w.hitCount("/articles/never") != 0 {
			t.Errorf("with an unusable condition alone: content %q, %d requests for the page", got, w.hitCount("/articles/never"))
		}
		full := fullContentStart + strings.ReplaceAll(fullText, "%s", w.server.URL) + fullContentEnd
		for guid, want := range map[string]string{
			"long": full, "moved": full, "short": "Summary", "gone": "Summary", "elsewhere": "Summary", "nolink": "Summary",
		} {
			if got := w.entry(f, guid).Content; got != want {
				t.Errorf("%s: content %q, want %q", guid, got, want)
			}
		}
		// One request for the page itself and one for where "moved" sent on.
		for path, want := range map[string]int{"/articles/long": 2, "/articles/short": 0, "/articles/gone": 1, "/articles/moved": 1} {
			if got := w.hitCount(path); got != want {
				t.Errorf("%s was requested %d times, want %d", path, got, want)
			}
		}
		logs := w.logs.String()
		for _, want := range []string{"article page was not read", "condition for reading article pages is not usable"} {
			if !strings.Contains(logs, want) {
				t.Errorf("no %q in the log: %s", want, logs)
			}
		}
	})
}

// R12: without a selector for the page, the filter cuts elements out of the
// text the feed gives.
func TestContentFilterWithoutFullText(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		w.serveBody("/feed", rssType, rss("Blog",
			item{guid: "clean", title: "Clean", body: "&lt;p&gt;Nothing to cut.&lt;/p&gt;"},
			item{guid: "ad", title: "With an ad", body: "&lt;p&gt;Text.&lt;/p&gt;&lt;div class=\"ad\"&gt;Buy&lt;/div&gt;&lt;p&gt;More.&lt;/p&gt;"}))
		f := w.feed(u, "/feed", func(f *store.Feed) {
			f.Attributes = json.RawMessage(`{"path_entries_filter":"[data-sanitized-class=ad]"}`)
		})
		w.runOne()
		e := w.entry(f, "ad")
		if want := "<p>Text.</p><p>More.</p>"; e.Content != want {
			t.Errorf("content %q, want %q", e.Content, want)
		}
		if original, kept := originalContent(t, e); !kept || original != `<p>Text.</p><div data-sanitized-class="ad">Buy</div><p>More.</p>` {
			t.Errorf("original content %q, kept %v", original, kept)
		}
		e = w.entry(f, "clean")
		if _, kept := originalContent(t, e); kept || e.Content != "<p>Nothing to cut.</p>" {
			t.Errorf("an entry without unwanted elements: content %q, original kept %v", e.Content, kept)
		}
		w.later()
		if st := w.runOne(); st.UpdatedEntries != 0 {
			t.Errorf("second refresh: stats %+v", st)
		}
	})
}
