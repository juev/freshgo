package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/mediaproxy"
	"github.com/juev/freshgo/internal/store"
)

var proxied = regexp.MustCompile(`/proxy/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+`)

// The images of an entry are handed out by the server, to whoever has an
// address the server wrote.
func TestMediaProxy(t *testing.T) {
	const png = "\x89PNG\r\n\x1a\n not much of a picture"
	var asked atomic.Int32
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(png))
		case "/page.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(images.Close)
	client, err := fetch.New(fetch.Options{Allowlist: []string{strings.TrimPrefix(images.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}

	imported(t, Options{Images: client}, func(t *testing.T, s *site) {
		ctx := t.Context()
		s.asAlice()
		id := s.stored("alice", store.Listing{Set: mainStream()})[0]
		e := s.entry("alice", id)
		e.Content = `<p><img src="` + images.URL + `/a.png" alt="a"><img src="https://secure.example/b.png" alt="b"></p>`
		if err := s.db.UpdateEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
		entry := "/entries/" + strconv.FormatInt(id, 10)
		shown := func() string { return s.page(entry) }

		// Out of the box: what a page served over https would not show.
		page := shown()
		address := proxied.FindString(page)
		if address == "" || strings.Contains(page, images.URL) || !strings.Contains(page, `src="https://secure.example/b.png"`) {
			t.Fatalf("the entry by default: the http image is not behind the server, or the https one is:\n%s", page)
		}
		if stored := s.entry("alice", id).Content; stored != e.Content {
			t.Errorf("the stored text was changed: %s", stored)
		}
		s.system(func(system *store.System) { system.MediaProxy = mediaproxy.ModeAll })
		if page := shown(); len(proxied.FindAllString(page, -1)) != 2 || strings.Contains(page, "secure.example") {
			t.Errorf("the entry with every image behind the server:\n%s", page)
		}
		s.system(func(system *store.System) { system.MediaProxy = mediaproxy.ModeNone })
		if page := shown(); proxied.MatchString(page) || !strings.Contains(page, images.URL+"/a.png") {
			t.Errorf("the entry with no image behind the server:\n%s", page)
		}

		// An address the server wrote needs no login, and holds whatever
		// the setting has become since.
		_ = s.post("/logout", nil)
		before := asked.Load()
		a := s.get(address)
		if a.status != http.StatusOK || a.body != png || a.header.Get("Content-Type") != "image/png" ||
			a.header.Get("Cache-Control") != "private, max-age=259200" || a.header.Get("ETag") == "" ||
			!strings.Contains(a.header.Get("Content-Security-Policy"), "sandbox") || a.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s: status %d, header %v, body %q", address, a.status, a.header, a.body)
		}
		if again := s.do(http.MethodGet, address, map[string]string{"If-None-Match": a.header.Get("ETag")}); again.status != http.StatusNotModified || again.body != "" || asked.Load() != before+1 ||
			!strings.Contains(again.header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("the image a browser has: status %d, the site was asked %d times", again.status, asked.Load()-before)
		}

		salt, err := s.db.Salt(ctx)
		if err != nil {
			t.Fatal(err)
		}
		key := mediaproxy.Key(salt)
		for target, status := range map[string]int{
			images.URL + "/gone.png":         http.StatusNotFound,
			images.URL + "/page.html":        http.StatusUnsupportedMediaType,
			"http://127.0.0.1:1/internal.png": http.StatusForbidden,
			"http://images.invalid/a.png":    http.StatusBadGateway,
		} {
			if a := s.get(mediaproxy.Address(key, "", target)); a.status != status || a.header.Get("ETag") != "" || strings.Contains(a.body, "alert") {
				t.Errorf("GET the address of %s: status %d, want %d; body %q", target, a.status, status, a.body)
			}
		}
		// An address somebody else wrote is no permission.
		forged := mediaproxy.Address(mediaproxy.Key("another salt"), "", images.URL+"/a.png")
		before = asked.Load()
		if a := s.get(forged); a.status != http.StatusForbidden || asked.Load() != before {
			t.Errorf("a forged address: status %d, the site was asked %d times", a.status, asked.Load()-before)
		}
		if a := s.post(address, nil); a.status == http.StatusOK {
			t.Errorf("POST %s: status %d", address, a.status)
		}
	})
}

// A server that fetches no images leaves the addresses alone.
func TestMediaProxyOff(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		id := s.stored("alice", store.Listing{Set: mainStream()})[0]
		e := s.entry("alice", id)
		e.Content = `<p><img src="http://images.example/a.png" alt="a"></p>`
		if err := s.db.UpdateEntry(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		if page := s.page("/entries/" + strconv.FormatInt(id, 10)); proxied.MatchString(page) || !strings.Contains(page, `src="http://images.example/a.png"`) {
			t.Errorf("the entry on a server that fetches no images:\n%s", page)
		}
		salt, _ := s.db.Salt(t.Context())
		if a := s.get(mediaproxy.Address(mediaproxy.Key(salt), "", "http://images.example/a.png")); a.status != http.StatusNotFound {
			t.Errorf("an image from a server that fetches none: status %d", a.status)
		}
	})
}
