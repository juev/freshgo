package fulltext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
)

const oracleDir = "../../testdata/reference/oracle"

// oracleHost is where the pages lived when FreshRSS read them.
const oracleHost = "http://127.0.0.1:8080"

func newClient(t *testing.T, server *httptest.Server) *fetch.Client {
	t.Helper()
	client, err := fetch.New(fetch.Options{UserAgent: "freshgo/test", Allowlist: []string{strings.TrimPrefix(server.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(oracleDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// R12: the selectors pick, and the result is cleaned, as in FreshRSS.
func TestAgainstFreshRSS(t *testing.T) {
	var cases []struct {
		Name     string `json:"name"`
		Page     string `json:"page"`
		Selector string `json:"selector"`
		Filter   string `json:"filter"`
	}
	readJSON(t, "fulltext-cases.json", &cases)
	var want map[string]string
	readJSON(t, "fulltext.json", &want)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(oracleDir, "articles"))))
	t.Cleanup(server.Close)
	client := newClient(t, server)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			expected, ok := want[c.Name]
			if !ok {
				t.Fatal("no result of FreshRSS for the case; run testdata/reference/oracle/generate.sh")
			}
			expected = strings.ReplaceAll(expected, oracleHost, server.URL)
			// When the filter removes something from the cleaned markup too,
			// FreshRSS returns the body element around the content.
			if inner, wrapped := strings.CutPrefix(expected, "<body>"); wrapped {
				if c.Name != "filter by attribute" {
					t.Errorf("FreshRSS wrapped the content in a body element: %q", expected)
				}
				expected = strings.TrimSuffix(inner, "</body>")
			}
			got, err := Article(context.Background(), client, Request{
				URL: server.URL + "/" + c.Page, Selector: c.Selector, Filter: c.Filter,
			})
			if errors.Is(err, ErrNoElements) && expected == "" {
				return
			}
			if err != nil {
				t.Fatalf("Article: %v", err)
			}
			if got != expected {
				t.Errorf("content\n got %q\nwant %q", got, expected)
			}
		})
	}
}

// readablePage is a page as sites make them: the article among a menu, a
// column of links and a footer.
func readablePage(article string) string {
	paragraph := strings.Repeat("The harbour was quiet that morning, and the boats lay still on the water. ", 4)
	return `<html><head><title>Harbour news</title></head><body>
<nav><ul><li><a href="/">Home</a></li><li><a href="/about">About the site</a></li></ul></nav>
<div id="content"><article><h1>Boats at rest</h1>
<p>First. ` + paragraph + `</p>
<p>Second, with a <a href="more.html">link</a>. ` + paragraph + `</p>
<div class="promo"><p>Subscribe to the newsletter of the harbour, every week in your mailbox.</p></div>
<p>Third. ` + paragraph + `</p>` + article + `
</article></div>
<aside><h2>Most read</h2><ul><li><a href="/a">Tides of the week</a></li><li><a href="/b">Fish prices</a></li></ul></aside>
<footer><p>Copyright of the harbour gazette.</p></footer>
</body></html>`
}

// T11: without a selector the article is found on its page, cleaned, with
// its addresses resolved and without what the filter names.
func TestAutomatic(t *testing.T) {
	mux := http.NewServeMux()
	page := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(body))
		})
	}
	page("/news/boats", readablePage(`<script>track()</script>`))
	page("/news/moved", `<html><head><meta http-equiv="refresh" content="0; url=boats"></head><body><p>Moved.</p></body></html>`)
	page("/news/based", strings.Replace(readablePage(""), "<head>", `<head><base href="/elsewhere/">`, 1))
	page("/news/bare", `<html><head><title>Bare</title></head><body></body></html>`)
	// A page that is an application: what its loading screen says.
	page("/news/game", `<html><head><title>Harbour, the game</title></head><body><div id="overlay"><h1>HARBOUR</h1><p>fetching engine…</p>`+
		`<p>click (or press Enter) to start · then press any key for the menu</p></div><canvas></canvas><script src="game.js"></script></body></html>`)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := newClient(t, server)
	article := func(path, filter string) (string, error) {
		return Article(context.Background(), client, Request{URL: server.URL + path, Automatic: true, Selector: "p[", Filter: filter})
	}

	got, err := article("/news/boats", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"First. The harbour", "Third. The harbour", `<a href="` + server.URL + `/news/more.html">link</a>`, "Subscribe to the newsletter"} {
		if !strings.Contains(got, want) {
			t.Errorf("the article lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"About the site", "Tides of the week", "Copyright of the harbour", "track()", "<script"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the article holds %q:\n%s", unwanted, got)
		}
	}

	if got, err = article("/news/boats", ".promo"); err != nil || strings.Contains(got, "Subscribe") || !strings.Contains(got, "Third. The harbour") {
		t.Errorf("with a filter: %v\n%s", err, got)
	}
	if got, err = article("/news/moved", ""); err != nil || !strings.Contains(got, "First. The harbour") {
		t.Errorf("a page that sends on: %v\n%s", err, got)
	}
	if got, err = article("/news/based", ""); err != nil || !strings.Contains(got, `<a href="`+server.URL+`/elsewhere/more.html">link</a>`) {
		t.Errorf("a page with a base address: %v\n%s", err, got)
	}
	if got, err = article("/news/bare", ""); !errors.Is(err, ErrNoArticle) {
		t.Errorf("a page without an article: %q, error = %v, want ErrNoArticle", got, err)
	}
	if got, err = article("/news/game", ""); !errors.Is(err, ErrNoArticle) {
		t.Errorf("a page with a few words and no article: %q, error = %v, want ErrNoArticle", got, err)
	}
	if _, err = article("/news/boats", "p["); !errors.Is(err, ErrSelector) {
		t.Errorf("a filter that is not CSS: error = %v, want ErrSelector", err)
	}
}

// A page that something else reads, a browser, is taken as it is handed
// over: no request is made, and where the page sends on is not followed.
func TestReadInPlaceOfARequest(t *testing.T) {
	asked := ""
	read := func(markup string, err error) func(context.Context, string) (string, string, error) {
		return func(_ context.Context, address string) (string, string, error) {
			asked = address
			return "https://site.example/news/boats/", markup, err
		}
	}
	// No client: a request would be a nil dereference.
	article := func(req Request) (string, error) {
		req.URL = "https://site.example/go/boats"
		return Article(context.Background(), nil, req)
	}

	got, err := article(Request{Automatic: true, Read: read(readablePage(`<meta http-equiv="refresh" content="0; url=/elsewhere">`), nil)})
	if err != nil {
		t.Fatal(err)
	}
	if asked != "https://site.example/go/boats" {
		t.Errorf("asked for %q", asked)
	}
	// Relative addresses are resolved against where the page ended.
	for _, want := range []string{"First. The harbour", `<a href="https://site.example/news/boats/more.html">link</a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("the article lacks %q:\n%s", want, got)
		}
	}

	got, err = article(Request{Selector: "h1", Read: read(`<html><body><h1>Boats</h1><p>Text.</p></body></html>`, nil)})
	if err != nil || got != "<h1>Boats</h1>" {
		t.Errorf("with a selector: %q, %v", got, err)
	}
	if _, err := article(Request{Selector: "h1", Read: read(" \n", nil)}); !errors.Is(err, ErrEmptyPage) {
		t.Errorf("a page without markup: %v, want ErrEmptyPage", err)
	}
	failure := errors.New("behind a check")
	if _, err := article(Request{Selector: "h1", Read: read("", failure)}); !errors.Is(err, failure) {
		t.Errorf("a page that was not read: %v, want the error of the reader", err)
	}
}
