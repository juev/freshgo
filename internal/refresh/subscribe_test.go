package refresh

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

const blogFeed = `<?xml version="1.0"?><rss version="2.0"><channel>
<title>The Blog</title><link>http://blog.example/</link><description>About things</description>
<item><guid>a</guid><title>First</title><pubDate>Mon, 05 Oct 2026 10:00:00 GMT</pubDate></item>
<item><guid>b</guid><title>Second</title><pubDate>Tue, 06 Oct 2026 10:00:00 GMT</pubDate></item>
</channel></rss>`

// A new feed is checked, named after what it says of itself, stored and
// filled with its entries in one go.
func TestAddFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.serveBody("/feed.xml", "application/rss+xml", blogFeed)

		f := &store.Feed{URL: "  " + w.server.URL + "/feed.xml ", Priority: 10}
		if err := w.r.AddFeed(ctx, u, f); err != nil {
			t.Fatalf("AddFeed: %v", err)
		}
		if f.ID != 1 || f.URL != w.server.URL+"/feed.xml" || f.Name != "The Blog" || f.Website != "http://blog.example/" ||
			f.Description != "About things" || f.CategoryID != store.DefaultCategoryID || f.LastUpdate != start.Unix() {
			t.Errorf("added feed = %+v", f)
		}
		if entries := w.entries(f); len(entries) != 2 || entries[0].Title != "First" || entries[1].Title != "Second" {
			t.Errorf("%d entries after AddFeed, want the two of the feed in the order of their dates", len(entries))
		}
		if hits := w.hitCount("/feed.xml"); hits != 1 {
			t.Errorf("the feed was requested %d times, want once", hits)
		}

		// What the caller chose stands.
		w.serveBody("/other.xml", "application/rss+xml", blogFeed)
		named := &store.Feed{URL: w.server.URL + "/other.xml", Name: "Mine", Website: "http://mine.example/", Description: "Chosen"}
		if err := w.r.AddFeed(ctx, u, named); err != nil {
			t.Fatalf("AddFeed with a name: %v", err)
		}
		if named.Name != "Mine" || named.Website != "http://mine.example/" || named.Description != "Chosen" {
			t.Errorf("feed added with a name = %+v", named)
		}

		again := &store.Feed{URL: w.server.URL + "/feed.xml"}
		if err := w.r.AddFeed(ctx, u, again); !errors.Is(err, ErrAlreadySubscribed) {
			t.Errorf("AddFeed twice: error = %v, want ErrAlreadySubscribed", err)
		}
		// Another user subscribes to the same address on their own.
		bob := w.user("bob", "")
		if err := w.r.AddFeed(ctx, bob, &store.Feed{URL: w.server.URL + "/feed.xml"}); err != nil {
			t.Errorf("AddFeed by another user: %v", err)
		}
	})
}

// What does not answer with a feed is not added.
func TestAddFeedRefusals(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.serveBody("/page.html", "text/html", "<html><head><title>No feed here</title></head></html>")
		w.serveBody("/loop.html", "text/html", `<link rel="alternate" type="application/rss+xml" href="/page.html">`)
		w.serveBody("/feed.xml", "application/rss+xml", blogFeed)
		w.registry.CheckURLBeforeAdd.Add(0, func(_ context.Context, address string) (string, bool) {
			return strings.Replace(address, "/moved.xml", "/feed.xml", 1), !strings.Contains(address, "forbidden")
		})
		w.registry.FeedBeforeInsert.Add(0, func(_ context.Context, f *store.Feed) (*store.Feed, bool) {
			return f, f.Name != "Unwanted"
		})

		var status *fetch.StatusError
		for name, tc := range map[string]struct {
			feed  *store.Feed
			check func(error) bool
		}{
			"a missing document":              {&store.Feed{URL: w.server.URL + "/missing.xml"}, func(err error) bool { return errors.As(err, &status) && status.Code == 404 }},
			"a page without a feed":           {&store.Feed{URL: w.server.URL + "/page.html"}, func(err error) bool { return errors.Is(err, feed.ErrNotFeed) }},
			"a page that names a page":        {&store.Feed{URL: w.server.URL + "/loop.html"}, func(err error) bool { return errors.Is(err, feed.ErrNotFeed) }},
			"no address":                      {&store.Feed{URL: "  "}, func(err error) bool { return errors.Is(err, fetch.ErrBadURL) }},
			"an address without a host":       {&store.Feed{URL: "http://"}, func(err error) bool { return errors.Is(err, fetch.ErrBadURL) }},
			"an address an extension refuses": {&store.Feed{URL: w.server.URL + "/forbidden.xml"}, func(err error) bool { return errors.Is(err, ErrRefused) }},
			"a feed an extension refuses":     {&store.Feed{URL: w.server.URL + "/feed.xml", Name: "Unwanted"}, func(err error) bool { return errors.Is(err, ErrRefused) }},
		} {
			if err := w.r.AddFeed(ctx, u, tc.feed); !tc.check(err) {
				t.Errorf("%s: error = %v", name, err)
			}
		}
		if feeds, err := w.db.Feeds(ctx, u.ID); err != nil || len(feeds) != 0 {
			t.Errorf("%d feeds after the refusals, %v; want none", len(feeds), err)
		}

		// An extension may send the request elsewhere.
		f := &store.Feed{URL: w.server.URL + "/moved.xml"}
		if err := w.r.AddFeed(ctx, u, f); err != nil || f.URL != w.server.URL+"/feed.xml" {
			t.Errorf("AddFeed of an address an extension rewrites: %v, stored address %q", err, f.URL)
		}
	})
}

// The address of a web page stands for the feed the page announces, and an
// address that has moved for good for the one it moved to.
func TestAddFeedFollowsThePage(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.serveBody("/site/", "text/html; charset=utf-8", `<html><head><base href="/feeds/">
			<link rel="stylesheet" href="style.css">
			<link rel="alternate" type="text/html" href="mobile.html">
			<link rel="ALTERNATE nofollow" type="Application/Atom+XML; charset=utf-8" href="main.xml">
			<link rel="alternate" type="application/rss+xml" href="second.xml"></head></html>`)
		w.serveBody("/feeds/main.xml", "application/rss+xml", blogFeed)
		w.serve("/old.xml", func(rw http.ResponseWriter, req *http.Request) {
			http.Redirect(rw, req, "/new.xml", http.StatusMovedPermanently)
		})
		w.serveBody("/new.xml", "application/rss+xml", blogFeed)

		f := &store.Feed{URL: w.server.URL + "/site/"}
		if err := w.r.AddFeed(ctx, u, f); err != nil {
			t.Fatalf("AddFeed of a page: %v", err)
		}
		if f.URL != w.server.URL+"/feeds/main.xml" || f.Name != "The Blog" || len(w.entries(f)) != 2 {
			t.Errorf("feed added by its page = %+v with %d entries; want the first feed the page announces", f, len(w.entries(f)))
		}
		moved := &store.Feed{URL: w.server.URL + "/old.xml"}
		if err := w.r.AddFeed(ctx, u, moved); err != nil || moved.URL != w.server.URL+"/new.xml" {
			t.Errorf("AddFeed of a moved address: %v, stored address %q; want the new one", err, moved.URL)
		}
	})
}

// A feed that is not RSS or Atom is added without a check and stays even if
// its first refresh fails.
func TestAddFeedOfAnotherKind(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.serveBody("/feed.json", "application/feed+json",
			`{"version":"https://jsonfeed.org/version/1.1","title":"JSON blog","items":[{"id":"1","url":"http://blog.example/1","title":"One"}]}`)

		good := &store.Feed{URL: w.server.URL + "/feed.json", Kind: scrape.KindJSONFeed}
		if err := w.r.AddFeed(ctx, u, good); err != nil {
			t.Fatalf("AddFeed: %v", err)
		}
		if good.Name != "JSON blog" || good.Error != 0 || len(w.entries(good)) != 1 {
			t.Errorf("JSON feed = %+v with %d entries; want it named by its first refresh", good, len(w.entries(good)))
		}
		broken := &store.Feed{URL: w.server.URL + "/missing.json", Kind: scrape.KindJSONFeed}
		if err := w.r.AddFeed(ctx, u, broken); err != nil {
			t.Fatalf("AddFeed of a failing JSON feed: %v", err)
		}
		if broken.ID == 0 || broken.Error != start.Unix() {
			t.Errorf("failing JSON feed = %+v; want it stored and marked as failing", broken)
		}
		page := &store.Feed{URL: w.server.URL + "/page.html", Kind: scrape.KindHTMLXPath}
		if err := w.r.AddFeed(ctx, u, page); err != nil || page.Website != page.URL {
			t.Errorf("scraped feed: %v, site %q; want the page itself", err, page.Website)
		}
	})
}

func TestRefreshFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		w.serveBody("/feed.xml", "application/rss+xml", blogFeed)
		// Muted, and refreshed a moment ago: a run would pass it over.
		f := w.feed(u, "/feed.xml", func(f *store.Feed) { f.TTL, f.LastUpdate = -3600, start.Unix() })
		other := w.feed(u, "/other.xml", nil)

		if err := w.r.RefreshFeed(ctx, u, f.ID); err != nil {
			t.Fatalf("RefreshFeed: %v", err)
		}
		if len(w.entries(f)) != 2 || w.hitCount("/other.xml") != 0 {
			t.Errorf("%d entries, %d requests for the other feed; want the one feed refreshed", len(w.entries(f)), w.hitCount("/other.xml"))
		}
		var status *fetch.StatusError
		if err := w.r.RefreshFeed(ctx, u, other.ID); !errors.As(err, &status) || w.storedFeed(other).Error == 0 {
			t.Errorf("RefreshFeed of a missing document: %v; want the feed marked as failing", err)
		}
		if err := w.r.RefreshFeed(ctx, u, 99); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RefreshFeed of an unknown feed: error = %v, want ErrNotFound", err)
		}
	})
}

// R13: a refresh looks after the icon of the feed, once it knows its site.
func TestRefreshKeepsIcons(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		u := w.user("alice", "")
		site := w.server.URL + "/site/"
		w.serveBody("/feed.xml", "application/rss+xml", strings.Replace(blogFeed, "http://blog.example/", site, 1))
		w.serveBody("/site/", "text/html", `<link rel="icon" href="icon.png">`)
		w.serveBody("/site/icon.png", "image/png", "\x89PNG\r\n\x1a\nicon")
		client, err := fetch.New(fetch.Options{Allowlist: []string{strings.TrimPrefix(w.server.URL, "http://")}})
		if err != nil {
			t.Fatal(err)
		}
		w.r.Icons = favicon.New(w.db, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
		f := w.feed(u, "/feed.xml", func(f *store.Feed) { f.Name = "" })

		w.runOne()
		salt, err := w.db.Salt(ctx)
		if err != nil {
			t.Fatal(err)
		}
		icon, err := w.db.Icon(ctx, favicon.HashOf(salt, w.storedFeed(f)))
		if err != nil || !strings.HasSuffix(string(icon.Content), "icon") || icon.Source != site {
			t.Fatalf("icon after the refresh = %+v, %v; want the one the site of the feed names", icon, err)
		}
		// The next refreshes find it in place.
		w.later()
		w.runOne()
		if hits := w.hitCount("/site/icon.png"); hits != 1 {
			t.Errorf("the icon was requested %d times over two refreshes, want once", hits)
		}
		// A feed that fails is not asked for an icon.
		broken := w.feed(u, "/broken.xml", nil)
		w.later()
		w.runOne()
		if _, err := w.db.Icon(ctx, favicon.HashOf(salt, broken)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("icon of a failing feed: error = %v, want none stored", err)
		}
	})
}

func TestCheckURL(t *testing.T) {
	for address, want := range map[string]string{
		"http://example.org/feed":  "http://example.org/feed",
		"HTTPS://Example.org/feed": "HTTPS://Example.org/feed",
		"example.org/feed":         "https://example.org/feed",
		"//example.org/feed":       "https://example.org/feed",
	} {
		if got, err := CheckURL(address); err != nil || got != want {
			t.Errorf("CheckURL(%q) = %q, %v; want %q", address, got, err, want)
		}
	}
	for _, address := range []string{"", "http://", "https:///path", "http://exa mple.org/"} {
		if got, err := CheckURL(address); !errors.Is(err, fetch.ErrBadURL) {
			t.Errorf("CheckURL(%q) = %q, %v; want ErrBadURL", address, got, err)
		}
	}
}
