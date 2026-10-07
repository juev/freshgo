package web

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
	"github.com/juev/freshgo/internal/web/i18n"
)

// site is the interface over a database of its own.
type site struct {
	t  *testing.T
	db *store.Store
	h  *Handler
	// cookies are what the browser of the test holds.
	cookies map[string]*http.Cookie
}

// eachEngine runs a test against the interface on every database engine.
func eachEngine(t *testing.T, o Options, test func(t *testing.T, s *site)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			o := o
			o.DB, o.Log = db, slog.New(slog.NewTextHandler(io.Discard, nil))
			if o.Version == "" {
				o.Version = "1.2.3"
			}
			if o.Hooks == nil {
				o.Hooks = &hooks.Registry{}
			}
			if o.Refresher == nil {
				// The feeds of the tests are served from this machine.
				client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}})
				if err != nil {
					t.Fatal(err)
				}
				o.Refresher = refresh.New(db, client, o.Hooks, o.Log)
			}
			h, err := New(o)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			test(t, &site{t: t, db: db, h: h})
		})
	}
}

// answer is what the interface said to a request.
type answer struct {
	status int
	header http.Header
	body   string
}

func (s *site) do(method, target string, header map[string]string) answer {
	s.t.Helper()
	return s.send(httptest.NewRequest(method, target, nil), header)
}

// send passes a request through the interface, as a browser that keeps the
// cookies it was given.
func (s *site) send(r *http.Request, header map[string]string) answer {
	s.t.Helper()
	for name, value := range header {
		r.Header.Set(name, value)
	}
	for _, c := range s.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		delete(s.cookies, c.Name)
		if c.MaxAge >= 0 {
			if s.cookies == nil {
				s.cookies = map[string]*http.Cookie{}
			}
			s.cookies[c.Name] = c
		}
	}
	return answer{w.Code, w.Header(), w.Body.String()}
}

// post sends a form the way a browser does from a page of the interface.
func (s *site) post(target string, form url.Values) answer {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	return s.send(r, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin"})
}

func (s *site) get(target string) answer { return s.do(http.MethodGet, target, nil) }

func TestAboutPage(t *testing.T) {
	eachEngine(t, Options{}, func(t *testing.T, s *site) {
		a := s.get("/about")
		if a.status != http.StatusOK || !strings.HasPrefix(a.body, "<!DOCTYPE html>\n<html lang=\"en\" data-theme=\"auto\" data-look=\"classic\">") {
			t.Fatalf("GET /about: status %d, body starts %.80q", a.status, a.body)
		}
		for _, want := range []string{
			"<title>About · freshgo</title>", "Version 1.2.3", "<code>http://example.com</code>",
			`<a class="skip" href="#content">Skip to content</a>`, `<main class="page" id="content" tabindex="-1">`,
			`<a href="/about" aria-current="page">About</a>`, `id="messages" role="status" aria-live="polite"`,
		} {
			if !strings.Contains(a.body, want) {
				t.Errorf("GET /about: no %q in\n%s", want, a.body)
			}
		}
		for name, want := range map[string]string{
			"Content-Type":            "text/html; charset=utf-8",
			"Cache-Control":           "no-store",
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "same-origin",
			"Content-Security-Policy": contentSecurityPolicy,
		} {
			if got := a.header.Get(name); got != want {
				t.Errorf("GET /about: %s = %q, want %q", name, got, want)
			}
		}
		// Scripts and styles come from the server itself and from nowhere
		// else, whatever an entry carries.
		if csp := a.header.Get("Content-Security-Policy"); strings.Contains(csp, "unsafe") || !strings.HasPrefix(csp, "default-src 'self';") {
			t.Errorf("Content-Security-Policy = %q", csp)
		}

	})
}

// The language is the one the browser asks for, then the one of the
// installation; the name of the installation is the administrator's.
func TestLanguageAndTitle(t *testing.T) {
	eachEngine(t, Options{}, func(t *testing.T, s *site) {
		russian := s.do(http.MethodGet, "/about", map[string]string{"Accept-Language": "ru-RU,ru;q=0.9,en;q=0.8"})
		if !strings.Contains(russian.body, `<html lang="ru"`) || !strings.Contains(russian.body, "<title>О программе · freshgo</title>") ||
			!strings.Contains(russian.body, "Версия 1.2.3") || !strings.Contains(russian.body, "Перейти к содержимому") {
			t.Errorf("GET /about for a Russian reader:\n%s", russian.body)
		}

		system := store.DefaultSystem()
		system.Title, system.Language = "Новости & <b>", "ru"
		if err := s.db.SetSystem(context.Background(), system); err != nil {
			t.Fatal(err)
		}
		plain := s.get("/about")
		if !strings.Contains(plain.body, `<html lang="ru"`) || !strings.Contains(plain.body, "<title>О программе · Новости &amp; &lt;b&gt;</title>") {
			t.Errorf("GET /about on a Russian installation:\n%s", plain.body)
		}
		german := s.do(http.MethodGet, "/about", map[string]string{"Accept-Language": "de"})
		if !strings.Contains(german.body, `<html lang="ru"`) {
			t.Errorf("a reader whose language freshgo lacks does not get the language of the installation")
		}
		english := s.do(http.MethodGet, "/about", map[string]string{"Accept-Language": "en-GB"})
		if !strings.Contains(english.body, `<html lang="en"`) {
			t.Errorf("an English reader on a Russian installation does not get English")
		}
	})
}

func TestStaticFiles(t *testing.T) {
	eachEngine(t, Options{}, func(t *testing.T, s *site) {
		page := s.get("/about").body
		link := regexp.MustCompile(`<link rel="stylesheet" href="(/static/app\.css\?v=[0-9a-f]{12})">`).FindStringSubmatch(page)
		if link == nil {
			t.Fatalf("no versioned stylesheet link in\n%s", page)
		}
		css := s.get(link[1])
		if css.status != http.StatusOK || !strings.HasPrefix(css.header.Get("Content-Type"), "text/css") ||
			css.header.Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.Contains(css.body, ":focus-visible") {
			t.Errorf("GET %s: status %d, headers %v", link[1], css.status, css.header)
		}
		// Asked for without its version, or with another, a file may be
		// any version: it is not to be kept.
		for _, target := range []string{"/static/app.css", "/static/app.css?v=000000000000"} {
			if a := s.get(target); a.status != http.StatusOK || a.header.Get("Cache-Control") != "no-cache" {
				t.Errorf("GET %s: status %d, Cache-Control %q", target, a.status, a.header.Get("Cache-Control"))
			}
		}
		if a := s.get("/static/missing.css"); a.status != http.StatusNotFound {
			t.Errorf("GET /static/missing.css: status %d", a.status)
		}
		if a := s.get("/static/../web.go"); a.status == http.StatusOK {
			t.Errorf("GET /static/../web.go: status %d, the server gave out a file of its own", a.status)
		}
	})
}

func TestErrorPages(t *testing.T) {
	eachEngine(t, Options{}, func(t *testing.T, s *site) {
		missing := s.get("/nowhere")
		if missing.status != http.StatusNotFound || !strings.Contains(missing.body, "<h1>Page not found</h1>") ||
			!strings.Contains(missing.body, `<a href="/">Back to the start page</a>`) || missing.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET /nowhere: status %d, body\n%s", missing.status, missing.body)
		}
		russian := s.do(http.MethodGet, "/nowhere", map[string]string{"Accept-Language": "ru"})
		if !strings.Contains(russian.body, "<h1>Страница не найдена</h1>") {
			t.Errorf("GET /nowhere for a Russian reader:\n%s", russian.body)
		}
		wrong := s.do(http.MethodPost, "/about", nil)
		if wrong.status != http.StatusMethodNotAllowed || !strings.Contains(wrong.body, "<h1>Not possible here</h1>") {
			t.Errorf("POST /about: status %d, body\n%s", wrong.status, wrong.body)
		}
		// Every error the interface answers with has its texts.
		for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusInternalServerError} {
			w := httptest.NewRecorder()
			s.h.fail(w, httptest.NewRequest(http.MethodGet, "/", nil), status)
			if w.Code != status || strings.Contains(w.Body.String(), "error.") {
				t.Errorf("fail(%d): status %d, body\n%s", status, w.Code, w.Body.String())
			}
		}
	})
}

// Behind a reverse proxy that serves the interface under a path, links carry
// the path.
func TestPublicPath(t *testing.T) {
	eachEngine(t, Options{BaseURL: "https://example.org/reader"}, func(t *testing.T, s *site) {
		a := s.get("/about")
		for _, want := range []string{`href="/reader/static/app.css?v=`, `href="/reader/about"`, `href="/reader/"`, "<code>https://example.org/reader</code>"} {
			if !strings.Contains(a.body, want) {
				t.Errorf("GET /about: no %q in\n%s", want, a.body)
			}
		}
		if a := s.get("/"); a.header.Get("Location") != "/reader/login?next=%2F" {
			t.Errorf("GET /: Location %q, want the login page under the public path", a.header.Get("Location"))
		}
	})
}

var textKey = regexp.MustCompile(`\.(?:T|N) "([^"]+)"`)

// Every text a template asks for exists; the i18n tests see to it that it
// exists in every language.
func TestTemplatesUseKnownTexts(t *testing.T) {
	texts, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	en := texts.Match(i18n.Fallback)
	found := 0
	err = fs.WalkDir(templateFiles, "templates", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(templateFiles, name)
		if err != nil {
			return err
		}
		for _, m := range textKey.FindAllStringSubmatch(string(content), -1) {
			found++
			if en.T(m[1]) == m[1] && en.N(m[1], 1) == m[1] {
				t.Errorf("%s asks for the text %q, which does not exist", name, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Error("no texts found in the templates: the test looks for the wrong thing")
	}
}
