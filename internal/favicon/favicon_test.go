package favicon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// png is the smallest thing that is taken for a PNG image.
var png = []byte("\x89PNG\r\n\x1a\n" + "freshgo test icon")

const svg = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`

// site is a web site that keeps count of the requests for each path.
type site struct {
	*httptest.Server
	mu    sync.Mutex
	pages map[string]string
	hits  map[string]int
}

func newSite(t *testing.T, pages map[string]string) *site {
	t.Helper()
	s := &site{pages: pages, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		page, ok := s.pages[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, page)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *site) set(path, page string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if page == "" {
		delete(s.pages, path)
		return
	}
	s.pages[path] = page
}

func (s *site) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.hits {
		total += n
	}
	return total
}

type world struct {
	t       *testing.T
	db      *store.Store
	service *Service
	clock   time.Time
	user    *store.User
}

func eachEngine(t *testing.T, test func(t *testing.T, w *world)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			driver, dsn := e.New(t)
			db, err := store.Open(ctx, driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
			if err != nil {
				t.Fatal(err)
			}
			w := &world{t: t, db: db, clock: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), user: &store.User{Name: "alice"}}
			w.service = New(db, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
			w.service.now = func() time.Time { return w.clock }
			if err := db.CreateUser(ctx, w.user); err != nil {
				t.Fatal(err)
			}
			test(t, w)
		})
	}
}

func (w *world) feed(f *store.Feed) *store.Feed {
	w.t.Helper()
	f.UserID = w.user.ID
	if err := w.db.CreateFeed(context.Background(), f); err != nil {
		w.t.Fatal(err)
	}
	return f
}

// get asks the service for the icon of a feed.
func (w *world) get(f *store.Feed, header ...string) *http.Response {
	w.t.Helper()
	salt, err := w.db.Salt(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return w.request(http.MethodGet, Path+HashOf(salt, f), header...)
}

func (w *world) request(method, path string, header ...string) *http.Response {
	w.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	w.service.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// R13: the icon of the site is found, kept and served; a second request that
// names the time of the first is answered with 304.
func TestIconOfSite(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		s := newSite(t, map[string]string{
			"/blog/":                 `<html><head><base href="/static/"><link rel="stylesheet" href="x.css"><link rel="Shortcut Icon" href="icons/blog.png"></head></html>`,
			"/static/icons/blog.png": string(png),
		})
		f := w.feed(&store.Feed{URL: s.URL + "/blog/feed.xml", Website: s.URL + "/blog/"})

		// Nothing is known yet: the built-in icon stands in, for a short time.
		resp := w.get(f)
		if got := body(t, resp); resp.StatusCode != http.StatusOK || !bytes.Equal(got, defaultIcon) ||
			resp.Header.Get("Content-Type") != "image/svg+xml" || resp.Header.Get("Cache-Control") != "max-age=1800" {
			t.Errorf("before the refresh: status %d, %d bytes, headers %v; want the built-in icon for 1800 s", resp.StatusCode, len(got), resp.Header)
		}

		w.service.Refresh(ctx, f)
		resp = w.get(f)
		if got := body(t, resp); resp.StatusCode != http.StatusOK || !bytes.Equal(got, png) {
			t.Fatalf("after the refresh: status %d, body %q; want the icon of the site", resp.StatusCode, got)
		}
		for name, want := range map[string]string{
			"Content-Type":            "image/png",
			"Cache-Control":           "max-age=604800",
			"X-Content-Type-Options":  "nosniff",
			"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; sandbox",
			"Last-Modified":           "Tue, 06 Oct 2026 12:00:00 GMT",
		} {
			if got := resp.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		resp = w.get(f, "If-Modified-Since", resp.Header.Get("Last-Modified"))
		if got := body(t, resp); resp.StatusCode != http.StatusNotModified || len(got) != 0 {
			t.Errorf("conditional request: status %d with %d bytes, want 304 without a body", resp.StatusCode, len(got))
		}
		if resp := w.request(http.MethodHead, Path+"0000"); resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD: status %d, want 200", resp.StatusCode)
		}
		if resp := w.request(http.MethodPost, Path+"0000"); resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST: status %d, want 405", resp.StatusCode)
		}
	})
}

// The site is asked again only after a week, and an icon it had stays
// when it stops answering.
func TestIconIsKept(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		s := newSite(t, map[string]string{"/favicon.ico": string(png)})
		// Without a site of its own, a feed is looked up at the root of its host.
		f := w.feed(&store.Feed{URL: s.URL + "/deep/feed.xml"})

		w.service.Refresh(ctx, f)
		first := s.requests()
		if got := body(t, w.get(f)); !bytes.Equal(got, png) {
			t.Fatalf("icon = %q, want /favicon.ico of the host", got)
		}
		w.clock = w.clock.Add(6 * 24 * time.Hour)
		w.service.Refresh(ctx, f)
		if s.requests() != first {
			t.Errorf("%d requests after 6 days, want none", s.requests()-first)
		}

		// A new icon replaces the old one and is dated by the refresh.
		w.clock = w.clock.Add(2 * 24 * time.Hour)
		s.set("/favicon.ico", svg)
		w.service.Refresh(ctx, f)
		resp := w.get(f)
		if got := body(t, resp); string(got) != svg || resp.Header.Get("Content-Type") != "image/svg+xml" ||
			resp.Header.Get("Last-Modified") != "Wed, 14 Oct 2026 12:00:00 GMT" {
			t.Errorf("after 8 days: body %q, headers %v; want the new icon dated by its refresh", got, resp.Header)
		}

		// The site is gone: the icon stays as it was.
		w.clock = w.clock.Add(8 * 24 * time.Hour)
		s.set("/favicon.ico", "")
		before := s.requests()
		w.service.Refresh(ctx, f)
		if s.requests() == before {
			t.Error("the site was not asked after another 8 days")
		}
		resp = w.get(f)
		if got := body(t, resp); string(got) != svg || resp.Header.Get("Last-Modified") != "Wed, 14 Oct 2026 12:00:00 GMT" {
			t.Errorf("after the site is gone: body %q, Last-Modified %q; want the icon it had", got, resp.Header.Get("Last-Modified"))
		}
	})
}

// A site that had no icon is asked again after a day, not after a week.
func TestIconIsLookedForAgain(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		s := newSite(t, map[string]string{})
		f := w.feed(&store.Feed{URL: s.URL + "/feed.xml"})

		w.service.Refresh(ctx, f)
		first := s.requests()
		if got := body(t, w.get(f)); first == 0 || !bytes.Equal(got, defaultIcon) {
			t.Fatalf("a site without an icon: %d requests, icon %q; want it asked and the built-in icon", first, got)
		}
		s.set("/favicon.ico", string(png))
		w.clock = w.clock.Add(23 * time.Hour)
		w.service.Refresh(ctx, f)
		if s.requests() != first {
			t.Errorf("%d requests after 23 hours, want none", s.requests()-first)
		}
		w.clock = w.clock.Add(2 * time.Hour)
		w.service.Refresh(ctx, f)
		if got := body(t, w.get(f)); !bytes.Equal(got, png) {
			t.Errorf("after 25 hours: icon %q, want the one the site has by now", got)
		}
	})
}

// Where an icon is looked for, in order: the image the feed names, the page
// of the site, the page at the root of the site, /favicon.ico.
func TestIconSearch(t *testing.T) {
	const page = `<html><head><link rel="icon" href="%s"></head></html>`
	for name, tc := range map[string]struct {
		pages   map[string]string
		website string
		icon    string
		want    string
	}{
		"the image the feed names": {
			pages: map[string]string{"/logo.png": string(png), "/": strings.Replace(page, "%s", "/other.png", 1), "/other.png": svg},
			icon:  "/logo.png", website: "/", want: string(png),
		},
		"the site itself is an image": {
			pages: map[string]string{"/pic": string(png)}, website: "/pic", want: string(png),
		},
		"the first link that is an image": {
			pages: map[string]string{
				"/blog":     `<link rel="icon" href="missing.png"><link rel="ICON" href="/text.png"><link rel="icon" href="a/ok.png"><link rel="icon" href="/late.png">`,
				"/text.png": "not an image", "/a/ok.png": string(png), "/late.png": svg,
			},
			website: "/blog", want: string(png),
		},
		"the root of the site": {
			pages:   map[string]string{"/blog": "<html></html>", "/": strings.Replace(page, "%s", "root.png", 1), "/root.png": string(png)},
			website: "/blog", want: string(png),
		},
		"favicon.ico": {
			pages:   map[string]string{"/blog": "<html></html>", "/": "<html></html>", "/favicon.ico": string(png)},
			website: "/blog", want: string(png),
		},
		"nothing": {
			pages:   map[string]string{"/blog": "<html></html>", "/favicon.ico": "<html>not found, politely</html>"},
			website: "/blog", want: string(defaultIcon),
		},
		"an image too large to be an icon": {
			pages:   map[string]string{"/favicon.ico": string(png) + strings.Repeat("x", maxSize)},
			website: "/", want: string(defaultIcon),
		},
	} {
		t.Run(name, func(t *testing.T) {
			eachEngine(t, func(t *testing.T, w *world) {
				s := newSite(t, tc.pages)
				f := &store.Feed{URL: s.URL + "/feed.xml", Website: s.URL + tc.website}
				if tc.icon != "" {
					f.Attributes = []byte(`{"feedIconUrl":"` + s.URL + tc.icon + `"}`)
				}
				w.feed(f)
				w.service.Refresh(context.Background(), f)
				if got := body(t, w.get(f)); string(got) != tc.want {
					t.Errorf("icon = %.60q, want %.60q", got, tc.want)
				}
			})
		})
	}
}

// A page of many icon links costs a few requests: only the first maxLinks
// distinct links are tried, and a link named again is not tried again.
func TestIconLinksAreBounded(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		var many, repeated strings.Builder
		for i := range 1000 {
			fmt.Fprintf(&many, `<link rel="icon" href="/icon/%d.png">`, i)
		}
		many.WriteString(`<link rel="icon" href="/ok.png">`)
		for range maxLinks {
			repeated.WriteString(`<link rel="icon" href="/missing.png"><link rel="icon" href="missing.png">`)
		}
		repeated.WriteString(`<link rel="icon" href="/ok.png">`)
		s := newSite(t, map[string]string{"/many": many.String(), "/repeated": repeated.String(), "/ok.png": string(png)})

		f := w.feed(&store.Feed{URL: s.URL + "/many.xml", Website: s.URL + "/many"})
		w.service.Refresh(context.Background(), f)
		if got := body(t, w.get(f)); !bytes.Equal(got, defaultIcon) {
			t.Errorf("a page of 1000 links: icon %.60q, want the built-in one", got)
		}
		// The page and its links, the root and /favicon.ico.
		if got, want := s.requests(), 1+maxLinks+2; got != want {
			t.Errorf("a page of 1000 links: %d requests, want %d", got, want)
		}

		f = w.feed(&store.Feed{URL: s.URL + "/repeated.xml", Website: s.URL + "/repeated"})
		w.service.Refresh(context.Background(), f)
		if got := body(t, w.get(f)); !bytes.Equal(got, png) {
			t.Errorf("a page that names one link again and again: icon %.60q, want the next link", got)
		}
	})
}

// A site that does not answer holds the refresh of its feed for no longer
// than the search may take, and is left alone for a day like a site without
// an icon.
func TestIconSearchHasDeadline(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		var mu sync.Mutex
		requests := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests++
			mu.Unlock()
			if r.URL.Path == "/" {
				_, _ = io.WriteString(w, strings.Repeat(`<link rel="icon" href="/slow.png">`, 10))
				return
			}
			<-r.Context().Done()
		}))
		t.Cleanup(s.Close)
		w.service.timeout = 200 * time.Millisecond
		f := w.feed(&store.Feed{URL: s.URL + "/feed.xml"})

		start := time.Now()
		w.service.Refresh(ctx, f)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("the search took %v, want it cut at %v", elapsed, w.service.timeout)
		}
		mu.Lock()
		first := requests
		mu.Unlock()
		w.clock = w.clock.Add(23 * time.Hour)
		w.service.Refresh(ctx, f)
		mu.Lock()
		defer mu.Unlock()
		if requests != first {
			t.Errorf("%d requests 23 hours after a search that ran out of time, want none", requests-first)
		}
	})
}

// F37: the icon of a feed is requested through the proxy the feed goes
// through, and with nothing else of the settings of the feed.
func TestIconFollowsProxyOfFeed(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		// A proxy answers for sites that do not exist: nothing but it is
		// connected to.
		var mu sync.Mutex
		asked := map[string]http.Header{}
		proxy := func(name string) *httptest.Server {
			p := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				mu.Lock()
				asked[name+" "+r.URL.String()] = r.Header.Clone()
				mu.Unlock()
				_, _ = rw.Write(png)
			}))
			t.Cleanup(p.Close)
			return p
		}
		own, installation := proxy("own"), proxy("installation")
		installationURL, err := url.Parse(installation.URL)
		if err != nil {
			t.Fatal(err)
		}
		client, err := fetch.New(fetch.Options{
			Allowlist: []string{"*"},
			Proxy: func(context.Context) (fetch.InstallationProxy, error) {
				return fetch.InstallationProxy{URL: installationURL, ChosenOnly: true}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		w.service.client = client
		icon := func(f *store.Feed) []byte {
			t.Helper()
			w.service.Refresh(ctx, f)
			return body(t, w.get(f))
		}

		ownAddress := strings.TrimPrefix(own.URL, "http://")
		f := w.feed(&store.Feed{
			URL: "http://own.freshgo.test/feed.xml", HTTPAuth: "alice:secret",
			Attributes: []byte(`{"curl_params":{"101":0,"10004":"` + ownAddress + `","10022":"session=1","10023":["X-Key: secret"]}}`),
		})
		if got := icon(f); !bytes.Equal(got, png) {
			t.Errorf("a feed with a proxy of its own: icon %q, want the one its proxy answers with", got)
		}
		mu.Lock()
		header, ok := asked["own http://own.freshgo.test/"]
		mu.Unlock()
		if !ok {
			t.Fatalf("the proxy of the feed was not asked for the site: %v", asked)
		}
		for _, name := range []string{"Authorization", "Cookie", "X-Key"} {
			if got := header.Get(name); got != "" {
				t.Errorf("the request for the icon carries %s %q of the feed, want none", name, got)
			}
		}

		// What the proxy of one feed answered with is not the icon of a feed
		// that looks at the same place by another way.
		same := w.feed(&store.Feed{URL: "http://own.freshgo.test/other.xml"})
		if got := body(t, w.get(same)); !bytes.Equal(got, defaultIcon) {
			t.Errorf("a feed of the same site without the proxy: icon %q, want the built-in one", got)
		}

		f = w.feed(&store.Feed{URL: "http://chosen.freshgo.test/feed.xml", Attributes: []byte(`{"installation_proxy":true}`)})
		if got := icon(f); !bytes.Equal(got, png) {
			t.Errorf("a feed that chose the proxy of the installation: icon %q, want the one that proxy answers with", got)
		}
		mu.Lock()
		_, ok = asked["installation http://chosen.freshgo.test/"]
		mu.Unlock()
		if !ok {
			t.Errorf("the proxy of the installation was not asked for the site: %v", asked)
		}

		// A feed that chose nothing stays away from a proxy for chosen feeds.
		s := newSite(t, map[string]string{"/favicon.ico": svg})
		mu.Lock()
		before := len(asked)
		mu.Unlock()
		f = w.feed(&store.Feed{URL: s.URL + "/feed.xml"})
		if got := icon(f); string(got) != svg {
			t.Errorf("a feed without a choice: icon %q, want the one of its site", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(asked) != before {
			t.Errorf("a feed without a choice: a proxy was asked, %v", asked)
		}
	})
}

// R13: the icon a user chose is served as stored, and no site is asked.
func TestCustomIcon(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		ctx := context.Background()
		s := newSite(t, map[string]string{"/favicon.ico": svg})
		f := w.feed(&store.Feed{URL: s.URL + "/feed.xml", Attributes: []byte(`{"customFavicon":true}`)})
		other := w.feed(&store.Feed{URL: s.URL + "/other.xml", Attributes: []byte(`{"customFavicon":true}`)})
		salt, err := w.db.Salt(ctx)
		if err != nil {
			t.Fatal(err)
		}
		icon := store.CustomIcon{Hash: CustomHash(salt, w.user.ID, f.ID), Content: png, Modified: w.clock.Unix()}
		if err := w.db.SetCustomIcon(ctx, w.user.ID, f.ID, icon); err != nil {
			t.Fatal(err)
		}

		w.service.Refresh(ctx, f)
		resp := w.get(f)
		if got := body(t, resp); !bytes.Equal(got, png) || resp.Header.Get("Content-Type") != "image/png" ||
			resp.Header.Get("Last-Modified") != "Tue, 06 Oct 2026 12:00:00 GMT" {
			t.Errorf("custom icon: body %q, headers %v", got, resp.Header)
		}
		if resp := w.get(f, "If-Modified-Since", "Tue, 06 Oct 2026 12:00:00 GMT"); resp.StatusCode != http.StatusNotModified {
			t.Errorf("conditional request: status %d, want 304", resp.StatusCode)
		}
		// Feeds with a custom icon have hashes of their own.
		if got := body(t, w.get(other)); !bytes.Equal(got, defaultIcon) {
			t.Errorf("a feed whose custom icon is missing got %q, want the built-in icon", got)
		}
		if s.requests() != 0 {
			t.Errorf("%d requests to the site of a feed with a custom icon, want none", s.requests())
		}
	})
}

func TestHashes(t *testing.T) {
	site := Hash("salt", "https://example.org/")
	if len(site) != 16 || strings.Trim(site, "0123456789abcdef") != "" {
		t.Errorf("Hash = %q, want 16 hexadecimal digits", site)
	}
	for name, other := range map[string]string{
		"another site":  Hash("salt", "https://example.net/"),
		"another salt":  Hash("pepper", "https://example.org/"),
		"a custom icon": CustomHash("salt", 1, 2),
	} {
		if other == site {
			t.Errorf("%s has the hash of the site", name)
		}
	}
	if CustomHash("salt", 1, 23) == CustomHash("salt", 12, 3) {
		t.Error("custom icons of different feeds share a hash")
	}

	// Feeds that look for their icon at the same place share it.
	a := &store.Feed{UserID: 1, ID: 1, URL: "https://example.org/a.xml", Website: "https://example.org/"}
	b := &store.Feed{UserID: 2, ID: 7, URL: "https://example.org/b.xml"}
	if HashOf("salt", a) != site || HashOf("salt", b) != site {
		t.Errorf("HashOf = %q and %q, want both %q", HashOf("salt", a), HashOf("salt", b), site)
	}
	named := &store.Feed{URL: "https://example.org/a.xml", Website: "https://example.org/", Attributes: []byte(`{"feedIconUrl":"https://cdn.example.org/i.png"}`)}
	if got, want := HashOf("salt", named), Hash("salt", "https://cdn.example.org/i.png"); got != want {
		t.Errorf("HashOf a feed that names its icon = %q, want %q", got, want)
	}
}
