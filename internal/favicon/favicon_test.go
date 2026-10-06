package favicon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
			"Cache-Control":           "max-age=1209600",
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

// The site is asked again only after two weeks, and an icon it had stays
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
		w.clock = w.clock.Add(13 * 24 * time.Hour)
		w.service.Refresh(ctx, f)
		if s.requests() != first {
			t.Errorf("%d requests after 13 days, want none", s.requests()-first)
		}

		// A new icon replaces the old one and is dated by the refresh.
		w.clock = w.clock.Add(2 * 24 * time.Hour)
		s.set("/favicon.ico", svg)
		w.service.Refresh(ctx, f)
		resp := w.get(f)
		if got := body(t, resp); string(got) != svg || resp.Header.Get("Content-Type") != "image/svg+xml" ||
			resp.Header.Get("Last-Modified") != "Wed, 21 Oct 2026 12:00:00 GMT" {
			t.Errorf("after 15 days: body %q, headers %v; want the new icon dated by its refresh", got, resp.Header)
		}

		// The site is gone: the icon stays as it was.
		w.clock = w.clock.Add(15 * 24 * time.Hour)
		s.set("/favicon.ico", "")
		before := s.requests()
		w.service.Refresh(ctx, f)
		if s.requests() == before {
			t.Error("the site was not asked after another 15 days")
		}
		resp = w.get(f)
		if got := body(t, resp); string(got) != svg || resp.Header.Get("Last-Modified") != "Wed, 21 Oct 2026 12:00:00 GMT" {
			t.Errorf("after the site is gone: body %q, Last-Modified %q; want the icon it had", got, resp.Header.Get("Last-Modified"))
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
