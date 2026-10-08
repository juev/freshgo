package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fulltext"
	"github.com/juev/freshgo/internal/store"
)

func (w *world) limit(change func(*store.Limits)) {
	w.t.Helper()
	ctx := context.Background()
	system, err := w.db.System(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	change(&system.Limits)
	if err := w.db.SetSystem(ctx, system); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) feedsOf(u *store.User) []*store.Feed {
	w.t.Helper()
	feeds, err := w.db.Feeds(context.Background(), u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return feeds
}

// A user cannot have more feeds than the installation allows.
func TestFeedLimit(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.limit(func(l *store.Limits) { l.MaxFeeds = 1 })
		w.serveBody("/one.xml", rssType, blogFeed)
		w.serveBody("/two.xml", rssType, blogFeed)
		if err := w.r.AddFeed(ctx, u, &store.Feed{URL: w.server.URL + "/one.xml"}); err != nil {
			t.Fatalf("AddFeed: %v", err)
		}
		if err := w.r.AddFeed(ctx, u, &store.Feed{URL: w.server.URL + "/two.xml"}); !errors.Is(err, ErrTooManyFeeds) {
			t.Errorf("AddFeed past the limit: error = %v, want ErrTooManyFeeds", err)
		}
		if n, hits := len(w.feedsOf(u)), w.hitCount("/two.xml"); n != 1 || hits != 0 {
			t.Errorf("%d feeds, the refused one requested %d times; want 1 and 0", n, hits)
		}
		// Another user has room of their own.
		if err := w.r.AddFeed(ctx, w.user("bob", ""), &store.Feed{URL: w.server.URL + "/two.xml"}); err != nil {
			t.Errorf("AddFeed for another user: %v", err)
		}
	})
}

// A refresh asked for by a user covers their due feeds and gives way to a
// run that is going on.
func TestRefreshUser(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		alice, bob := w.user("alice", ""), w.user("bob", "")
		w.serveBody("/a.xml", rssType, blogFeed)
		w.serveBody("/b.xml", rssType, blogFeed)
		w.feed(alice, "/a.xml", nil)
		w.feed(bob, "/b.xml", nil)

		st, err := w.r.RefreshUser(ctx, alice, Options{})
		if err != nil || st.User != "alice" || st.Refreshed != 1 || st.NewEntries != 2 {
			t.Errorf("RefreshUser = %+v, %v; want one feed with two new entries", st, err)
		}
		if hits := w.hitCount("/b.xml"); hits != 0 {
			t.Errorf("the feed of another user was requested %d times", hits)
		}
		// Not due again at once.
		if st, err := w.r.RefreshUser(ctx, alice, Options{}); err != nil || st.Refreshed != 0 {
			t.Errorf("RefreshUser at once again = %+v, %v; want nothing refreshed", st, err)
		}

		w.r.running.Lock()
		_, err = w.r.RefreshUser(ctx, alice, Options{Force: true})
		w.r.running.Unlock()
		if !errors.Is(err, ErrBusy) {
			t.Errorf("RefreshUser during a run: error = %v, want ErrBusy", err)
		}
	})
}

// Reloading a feed fetches it without validators and reads the pages of
// its newest entries again.
func TestReloadFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		page := "<html><body><article>first version</article></body></html>"
		w.serve("/articles/one", func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = rw.Write([]byte(page))
		})
		var validators []string
		w.serve("/feed", func(rw http.ResponseWriter, req *http.Request) {
			validators = append(validators, req.Header.Get("If-None-Match"))
			rw.Header().Set("Content-Type", rssType)
			rw.Header().Set("ETag", `"v1"`)
			_, _ = rw.Write([]byte(rss("Blog", item{guid: "a", title: "First", link: w.server.URL + "/articles/one", body: "Summary"})))
		})
		for _, action := range []string{"", "append", "prepend"} {
			t.Run("action "+action, func(t *testing.T) {
				validators, page = nil, "<html><body><article>first version</article></body></html>"
				f := w.feed(u, "/feed", func(f *store.Feed) {
					f.URL += "?" + action
					f.PathEntries = "article"
					if action != "" {
						f.Attributes = json.RawMessage(`{"content_action":"` + action + `"}`)
					}
				})
				w.clock = w.clock.Add(3 * time.Hour)
				if err := w.r.RefreshFeed(ctx, u, f.ID); err != nil {
					t.Fatalf("RefreshFeed: %v", err)
				}
				if got := w.entry(f, "a").Content; !strings.Contains(got, "first version") {
					t.Fatalf("content after the first refresh = %q", got)
				}
				page = "<html><body><article>second version</article></body></html>"
				w.clock = w.clock.Add(time.Minute)
				if err := w.r.ReloadFeed(ctx, u, f.ID, 10); err != nil {
					t.Fatalf("ReloadFeed: %v", err)
				}
				e := w.entry(f, "a")
				if !strings.Contains(e.Content, "second version") || strings.Contains(e.Content, "first version") ||
					strings.Count(e.Content, fullContentStart) != 1 || e.LastModified != w.clock.Unix() {
					t.Errorf("entry after the reload: content %q, modified %d (now %d)", e.Content, e.LastModified, w.clock.Unix())
				}
				wantSummary := action != ""
				if has := strings.Contains(e.Content, "Summary"); has != wantSummary {
					t.Errorf("the text of the feed in the content: %v, want %v: %q", has, wantSummary, e.Content)
				}
				if original, kept := originalContent(t, e); action == "" && (!kept || original != "Summary") {
					t.Errorf("text of the feed kept aside = %q, %v", original, kept)
				}
				if len(validators) != 2 || validators[1] != "" {
					t.Errorf("If-None-Match of the requests = %q, want the reload to send none", validators)
				}
			})
		}
	})
}

// T12: after the way a feed gets the text of its entries was changed, its
// unread entries are brought in line with it, and the read ones left alone.
func TestCompleteUnread(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		w.serveBody("/articles/one", "text/html; charset=utf-8", articlePage)
		link := func(page string) string { return w.server.URL + "/articles/" + page }
		w.serveBody("/feed", rssType, rss("Blog",
			item{guid: "gone", title: "Gone", link: link("gone"), body: "Summary"},
			item{guid: "read", title: "Read", link: link("one"), body: "Summary"},
			item{guid: "b", title: "Second", link: link("one"), body: "Summary"},
			item{guid: "a", title: "First", link: link("one"), body: "Summary"}))
		f := w.feed(u, "/feed", nil)
		w.runOne()
		if _, err := w.db.SetEntriesRead(ctx, u.ID, []int64{w.entry(f, "read").ID}, true, w.clock.Unix()); err != nil {
			t.Fatal(err)
		}
		set := func(selector string) {
			t.Helper()
			stored := w.storedFeed(f)
			stored.PathEntries = selector
			if err := w.db.UpdateFeed(ctx, stored); err != nil {
				t.Fatal(err)
			}
		}
		contents := func() map[string]string {
			got := map[string]string{}
			for _, e := range w.entries(f) {
				got[e.GUID] = e.Content
			}
			return got
		}
		full := fullContentStart + fmt.Sprintf(fullText, w.server.URL) + fullContentEnd

		// Nothing changes for a feed that reads no pages and has read none.
		if n, err := w.r.CompleteUnread(ctx, u, f.ID); err != nil || n != 0 || w.hitCount("/articles/one") != 0 {
			t.Errorf("without full text: %d entries changed, %v, %d requests", n, err, w.hitCount("/articles/one"))
		}

		set("article .body")
		w.later()
		n, err := w.r.CompleteUnread(ctx, u, f.ID)
		if err != nil || n != 2 {
			t.Errorf("full text turned on: %d entries changed, %v; want 2", n, err)
		}
		want := map[string]string{"a": full, "b": full, "read": "Summary", "gone": "Summary"}
		if got := contents(); !reflect.DeepEqual(got, want) {
			t.Errorf("contents after full text was turned on:\n got %q\nwant %q", got, want)
		}
		if e := w.entry(f, "a"); e.LastModified != w.clock.Unix() {
			t.Errorf("a completed entry was modified at %d, want now (%d)", e.LastModified, w.clock.Unix())
		}

		// Turned off again: the text of the feed comes back.
		set("")
		if n, err := w.r.CompleteUnread(ctx, u, f.ID); err != nil || n != 2 {
			t.Errorf("full text turned off: %d entries changed, %v; want 2", n, err)
		}
		want = map[string]string{"a": "Summary", "b": "Summary", "read": "Summary", "gone": "Summary"}
		if got := contents(); !reflect.DeepEqual(got, want) {
			t.Errorf("contents after full text was turned off:\n got %q\nwant %q", got, want)
		}
		if _, kept := originalContent(t, w.entry(f, "a")); kept {
			t.Error("the text of the feed is still kept aside after it was put back")
		}
	})
}

// T13: one entry takes the article of its page on request, whatever its
// feed says about full text, and gives the text of the feed back.
func TestCompleteEntry(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		w.serveBody("/articles/one", "text/html; charset=utf-8", readablePage)
		w.serveBody("/articles/bare", "text/html; charset=utf-8", `<html><head><title>Bare</title></head><body><nav></nav></body></html>`)
		link := func(page string) string { return w.server.URL + "/articles/" + page }
		w.serveBody("/feed", rssType, rss("Blog",
			item{guid: "bare", title: "Bare", link: link("bare"), body: "Summary"},
			item{guid: "nolink", title: "No link", body: "Summary"},
			item{guid: "a", title: "First", link: link("one"), body: "Summary"}))
		f := w.feed(u, "/feed", func(f *store.Feed) {
			f.Attributes = json.RawMessage(`{"path_entries_filter":".promo","content_action":"append"}`)
		})
		w.runOne()
		id := func(guid string) int64 { return w.entry(f, guid).ID }

		w.later()
		if err := w.r.CompleteEntry(ctx, u, id("a"), true); err != nil {
			t.Fatalf("CompleteEntry: %v", err)
		}
		e := w.entry(f, "a")
		if !HasPageText(e) || !strings.HasPrefix(e.Content, fullContentStart) || !strings.HasSuffix(e.Content, fullContentEnd) ||
			!strings.Contains(e.Content, "First. The harbour") || strings.Contains(e.Content, "Subscribe") || strings.Contains(e.Content, "Summary") ||
			e.LastModified != w.clock.Unix() {
			t.Errorf("the entry after its page was read: modified %d (now %d), content %q", e.LastModified, w.clock.Unix(), e.Content)
		}
		if original, kept := originalContent(t, e); !kept || original != "Summary" {
			t.Errorf("text of the feed kept aside = %q, %v", original, kept)
		}
		// Asked again, the page is read again and the text of the feed is
		// still the one kept aside.
		if err := w.r.CompleteEntry(ctx, u, id("a"), true); err != nil {
			t.Fatalf("CompleteEntry, again: %v", err)
		}
		if original, _ := originalContent(t, w.entry(f, "a")); original != "Summary" || w.hitCount("/articles/one") != 2 {
			t.Errorf("asked twice: text of the feed %q, %d requests for the page", original, w.hitCount("/articles/one"))
		}
		// A refresh of the unchanged feed leaves the text of the page.
		w.later()
		if st := w.runOne(); st.UpdatedEntries != 0 || !HasPageText(w.entry(f, "a")) {
			t.Errorf("after a refresh of the unchanged feed: stats %+v, content %q", st, w.entry(f, "a").Content)
		}

		if err := w.r.CompleteEntry(ctx, u, id("a"), false); err != nil {
			t.Fatalf("CompleteEntry, back: %v", err)
		}
		e = w.entry(f, "a")
		if _, kept := originalContent(t, e); kept || e.Content != "Summary" || HasPageText(e) {
			t.Errorf("the entry after the text of the feed was put back: %q, kept aside %v", e.Content, kept)
		}

		// What cannot be done leaves the entry alone.
		if err := w.r.CompleteEntry(ctx, u, id("bare"), true); !errors.Is(err, fulltext.ErrNoArticle) || w.entry(f, "bare").Content != "Summary" {
			t.Errorf("a page without an article: error = %v, content %q", err, w.entry(f, "bare").Content)
		}
		if err := w.r.CompleteEntry(ctx, u, id("nolink"), true); !errors.Is(err, ErrNoLink) {
			t.Errorf("an entry without a link: error = %v, want ErrNoLink", err)
		}
		if err := w.r.CompleteEntry(ctx, u, 12345, true); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("an entry there is not: error = %v, want ErrNotFound", err)
		}
		other := w.user("bob", `{}`)
		if err := w.r.CompleteEntry(ctx, other, id("a"), true); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the entry of another user: error = %v, want ErrNotFound", err)
		}
	})
}

const opmlTemplate = `<?xml version="1.0"?><opml version="2.0"><body>%s</body></opml>`

func opmlOf(server string, paths ...string) string {
	var b strings.Builder
	for _, path := range paths {
		fmt.Fprintf(&b, `<outline type="rss" text="feed %s" xmlUrl="%s%s" frss:CURLOPT_PROXY="proxy.example:8080"/>`, path, server, path)
	}
	return fmt.Sprintf(opmlTemplate, b.String())
}

// R9: a category that mirrors an OPML document gets the feeds the document
// lists and mutes those it drops.
func TestDynamicOPML(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		for _, path := range []string{"/a.xml", "/b.xml", "/c.xml"} {
			w.serveBody(path, rssType, blogFeed)
		}
		document := opmlOf(w.server.URL, "/a.xml", "/b.xml")
		w.serve("/list.opml", func(rw http.ResponseWriter, _ *http.Request) { _, _ = rw.Write([]byte(document)) })
		c := &store.Category{UserID: u.ID, Name: "Mirror", Kind: KindDynamicOPML,
			Attributes: json.RawMessage(`{"opml_url":"` + w.server.URL + `/list.opml"}`)}
		plain := &store.Category{UserID: u.ID, Name: "Plain"}
		for _, category := range []*store.Category{c, plain} {
			if err := w.db.CreateCategory(ctx, category); err != nil {
				t.Fatal(err)
			}
		}
		elsewhere := w.feed(u, "/c.xml", nil)
		byURL := func() map[string]*store.Feed {
			feeds := map[string]*store.Feed{}
			for _, f := range w.feedsOf(u) {
				feeds[strings.TrimPrefix(f.URL, w.server.URL)] = f
			}
			return feeds
		}

		// The run reads the document and fetches the new feeds in one go.
		st := w.runOne()
		feeds := byURL()
		if len(feeds) != 3 || feeds["/a.xml"] == nil || feeds["/b.xml"] == nil ||
			feeds["/a.xml"].CategoryID != c.ID || feeds["/a.xml"].Name != "feed /a.xml" || st.Refreshed != 3 {
			t.Fatalf("after the first run: feeds %v, stats %+v", feeds, st)
		}
		if strings.Contains(string(feeds["/a.xml"].Attributes), "proxy.example") {
			t.Errorf("a feed took its request settings from the document: %s", feeds["/a.xml"].Attributes)
		}
		categories, _ := w.db.Categories(ctx, u.ID)
		if got := categories[1]; got.LastUpdate != w.clock.Unix() || got.Error != 0 {
			t.Errorf("category after reading its document = %+v", got)
		}

		// Not read again before it is due.
		document = opmlOf(w.server.URL, "/a.xml", "/c.xml")
		w.later()
		w.runOne()
		if hits := w.hitCount("/list.opml"); hits != 1 {
			t.Errorf("the document was requested %d times within its period, want once", hits)
		}

		// A feed the document drops is muted; one the user has elsewhere stays there.
		if err := w.r.RefreshOPML(ctx, u, c.ID); err != nil {
			t.Fatalf("RefreshOPML: %v", err)
		}
		feeds = byURL()
		if feeds["/b.xml"].TTL != -defaultTTL || feeds["/a.xml"].TTL != 0 || len(feeds) != 3 || feeds["/c.xml"].CategoryID != elsewhere.CategoryID {
			t.Errorf("after the document dropped a feed: b ttl %d, a ttl %d, %d feeds, c in category %d",
				feeds["/b.xml"].TTL, feeds["/a.xml"].TTL, len(feeds), feeds["/c.xml"].CategoryID)
		}

		// Back in the document, it is refreshed again.
		document = opmlOf(w.server.URL, "/a.xml", "/b.xml")
		if err := w.r.RefreshOPML(ctx, u, c.ID); err != nil {
			t.Fatalf("RefreshOPML: %v", err)
		}
		if ttl := byURL()["/b.xml"].TTL; ttl != defaultTTL {
			t.Errorf("ttl of the feed that is back = %d, want %d", ttl, defaultTTL)
		}

		// A document that cannot be read leaves the feeds alone and is recorded.
		document = "not OPML"
		if err := w.r.RefreshOPML(ctx, u, c.ID); err == nil {
			t.Error("RefreshOPML of a document that is not OPML: no error")
		}
		categories, _ = w.db.Categories(ctx, u.ID)
		if got := categories[1]; got.Error != w.clock.Unix() || len(byURL()) != 3 {
			t.Errorf("category after a failed read = %+v, %d feeds", got, len(byURL()))
		}

		// The limit of feeds holds for what a document brings.
		w.limit(func(l *store.Limits) { l.MaxFeeds = 3 })
		w.serveBody("/d.xml", rssType, blogFeed)
		document = opmlOf(w.server.URL, "/a.xml", "/b.xml", "/d.xml")
		if err := w.r.RefreshOPML(ctx, u, c.ID); !errors.Is(err, ErrTooManyFeeds) || len(byURL()) != 3 {
			t.Errorf("RefreshOPML past the limit: error = %v, %d feeds", err, len(byURL()))
		}

		if err := w.r.RefreshOPML(ctx, u, plain.ID); !errors.Is(err, ErrNoOPML) {
			t.Errorf("RefreshOPML of a plain category: error = %v, want ErrNoOPML", err)
		}
		if err := w.r.RefreshOPML(ctx, u, 99); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RefreshOPML of a missing category: error = %v, want ErrNotFound", err)
		}
	})
}

// The selector of a feed can be tried on its newest entry without storing
// anything.
func TestPreviewArticle(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		w.serveBody("/articles/one", "text/html; charset=utf-8", articlePage)
		w.serveBody("/feed", rssType, rss("Blog", item{guid: "a", title: "First", link: w.server.URL + "/articles/one", body: "Summary"}))
		f := w.feed(u, "/feed", nil)
		if _, err := w.r.PreviewArticle(ctx, u, f.ID, "article", false, ""); !errors.Is(err, ErrNoEntries) {
			t.Errorf("PreviewArticle of a feed without entries: error = %v, want ErrNoEntries", err)
		}
		w.runOne()
		got, err := w.r.PreviewArticle(ctx, u, f.ID, "article .body", false, ".ad")
		if want := fmt.Sprintf(fullTextFiltered, w.server.URL); err != nil || got != want {
			t.Errorf("PreviewArticle = %q, %v; want %q", got, err, want)
		}
		if e := w.entry(f, "a"); e.Content != "Summary" {
			t.Errorf("the entry was changed by the preview: %q", e.Content)
		}
		if _, err := w.r.PreviewArticle(ctx, u, f.ID, "p[", false, ""); err == nil {
			t.Error("PreviewArticle with a selector that is not CSS: no error")
		}
		// Without a selector, the automatic way.
		w.serveBody("/articles/one", "text/html; charset=utf-8", readablePage)
		got, err = w.r.PreviewArticle(ctx, u, f.ID, "", true, ".promo")
		if err != nil || !strings.Contains(got, "First. The harbour") || strings.Contains(got, "Subscribe") || strings.Contains(got, "Tides of the week") {
			t.Errorf("PreviewArticle, automatic = %q, %v", got, err)
		}
		if e := w.entry(f, "a"); e.Content != "Summary" {
			t.Errorf("the entry was changed by the automatic preview: %q", e.Content)
		}
	})
}

// A feed of a mirrored document that has moved for good is still the feed
// the document lists.
func TestDynamicOPMLFollowsRedirects(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.serve("/old.xml", func(rw http.ResponseWriter, req *http.Request) {
			http.Redirect(rw, req, "/new.xml", http.StatusMovedPermanently)
		})
		w.serveBody("/new.xml", rssType, blogFeed)
		w.serveBody("/list.opml", "text/x-opml", opmlOf(w.server.URL, "/old.xml"))
		c := &store.Category{UserID: u.ID, Name: "Mirror", Kind: KindDynamicOPML,
			Attributes: json.RawMessage(`{"opml_url":"` + w.server.URL + `/list.opml"}`)}
		if err := w.db.CreateCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			w.runOne()
			w.clock = w.clock.Add(13 * time.Hour)
		}
		feeds := w.feedsOf(u)
		if len(feeds) != 1 || feeds[0].URL != w.server.URL+"/new.xml" || feeds[0].TTL != 0 {
			t.Fatalf("feeds after three readings of the document = %d, the first %+v", len(feeds), feeds[0])
		}
		if n := len(w.entries(feeds[0])); n != 2 {
			t.Errorf("the feed has %d entries, want 2", n)
		}
	})
}

// Fetching a feed anew keeps the text an entry has when its page does not
// answer, and what the reader did to the entry meanwhile.
func TestReloadFeedKeepsWhatItCannotReplace(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", `{}`)
		w.serveBody("/articles/one", "text/html; charset=utf-8", "<html><body><article>the full text</article></body></html>")
		w.serveBody("/feed", rssType, rss("Blog", item{guid: "a", title: "First", link: w.server.URL + "/articles/one", body: "Summary"}))
		f := w.feed(u, "/feed", func(f *store.Feed) { f.PathEntries = "article" })
		w.runOne()
		before := w.entry(f, "a")

		w.serve("/articles/one", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusNotFound) })
		w.later()
		if err := w.r.ReloadFeed(ctx, u, f.ID, 10); err != nil {
			t.Fatalf("ReloadFeed: %v", err)
		}
		after := w.entry(f, "a")
		if after.Content != before.Content || string(after.Attributes) != string(before.Attributes) || after.LastModified != before.LastModified {
			t.Errorf("entry whose page did not answer:\n got %q %s\nwant %q %s", after.Content, after.Attributes, before.Content, before.Attributes)
		}

		// The reader stars the entry while its page is being read.
		w.serve("/articles/one", func(rw http.ResponseWriter, _ *http.Request) {
			if err := w.db.SetEntriesFavorite(ctx, u.ID, []int64{before.ID}, true, w.clock.Unix()); err != nil {
				t.Error(err)
			}
			_, _ = rw.Write([]byte("<html><body><article>another text</article></body></html>"))
		})
		w.later()
		if err := w.r.ReloadFeed(ctx, u, f.ID, 10); err != nil {
			t.Fatalf("ReloadFeed: %v", err)
		}
		if e := w.entry(f, "a"); !e.IsFavorite || !strings.Contains(e.Content, "another text") {
			t.Errorf("entry starred during the reload: starred %v, content %q", e.IsFavorite, e.Content)
		}
	})
}

// Settings changed while a feed is being fetched are not undone by the
// validators of the answer that was on its way.
func TestValidatorsOfAnOvertakenFetch(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		var sent []string
		change := true
		var f *store.Feed
		w.serve("/feed", func(rw http.ResponseWriter, req *http.Request) {
			sent = append(sent, req.Header.Get("If-None-Match"))
			if change {
				change = false
				// What the form of the settings does.
				err := w.db.InTx(ctx, func(tx *store.Store) error {
					fresh, err := tx.LockFeed(ctx, u.ID, f.ID)
					if err != nil {
						return err
					}
					fresh.Attributes, fresh.HTTPETag = json.RawMessage(`{"unicityCriteria":"link"}`), ""
					return tx.UpdateFeed(ctx, fresh)
				})
				if err != nil {
					t.Error(err)
				}
			}
			rw.Header().Set("Content-Type", rssType)
			rw.Header().Set("ETag", `"v1"`)
			_, _ = rw.Write([]byte(blogFeed))
		})
		f = w.feed(u, "/feed", nil)
		w.runOne()
		if got := w.storedFeed(f); got.HTTPETag != "" || got.Error != 0 || got.LastUpdate == 0 {
			t.Errorf("feed after a fetch its settings overtook = %+v", got)
		}
		w.later()
		w.runOne()
		if got := w.storedFeed(f); len(sent) != 2 || sent[1] != "" || got.HTTPETag != `"v1"` {
			t.Errorf("If-None-Match of the requests = %q, validator now %q; want none sent and the new one kept", sent, got.HTTPETag)
		}
	})
}
