package refresh

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

// A feed read by an address with a password keeps the password to itself:
// the relative links of its document are stored without it.
func TestRelativeLinksWithoutCredentials(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", "")
		w.serveBody("/feed.xml", "application/atom+xml", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title>`+
			`<link href="/"/><entry><id>urn:a</id><title>a</title><updated>2026-10-06T10:00:00Z</updated>`+
			`<link href="/a.html"/><link rel="enclosure" href="/a.mp3" type="audio/mpeg"/>`+
			`<content type="html">&lt;img src="/a.png"&gt; &lt;a href="/more"&gt;more&lt;/a&gt;</content></entry></feed>`)
		w.serveBody("/page.html", "text/html", `<html><head><title>Page</title></head><body>`+
			`<div class="post"><a href="/b.html">b</a></div></body></html>`)
		withPassword := func(f *store.Feed) { f.URL = strings.Replace(f.URL, "http://", "http://alice:secret@", 1) }
		atom := w.feed(u, "/feed.xml", withPassword)
		scraped := w.feed(u, "/page.html", func(f *store.Feed) {
			withPassword(f)
			f.Kind = scrape.KindHTMLXPath
			f.Attributes = json.RawMessage(`{"xpath":{"item":"//div[@class='post']","itemTitle":"a","itemUri":"a/@href"}}`)
		})
		if st := w.runOne(); st.Refreshed != 2 {
			t.Fatalf("refreshed %d feeds, want 2: %s", st.Refreshed, w.logs)
		}
		for _, f := range []*store.Feed{atom, scraped} {
			entries := w.entries(f)
			if len(entries) != 1 {
				t.Fatalf("feed %s has %d entries, want 1", f.URL, len(entries))
			}
			e, stored := entries[0], w.storedFeed(f)
			if !strings.HasPrefix(e.Link, w.server.URL+"/") {
				t.Errorf("link of the entry = %q, want it under %s", e.Link, w.server.URL)
			}
			for what, value := range map[string]string{
				"link": e.Link, "content": e.Content, "attributes": string(e.Attributes), "website": stored.Website,
			} {
				if strings.Contains(value, "secret") {
					t.Errorf("feed %s: the %s carries the password: %s", f.URL, what, value)
				}
			}
		}
	})
}
