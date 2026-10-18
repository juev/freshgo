package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/miniflux/minifluxtest"
	"github.com/juev/freshgo/internal/store"
)

// withMiniflux is the options of a site that may import from the server.
func withMiniflux(t *testing.T, s *minifluxtest.Server) Options {
	t.Helper()
	// Nothing listens on port 1 of this machine: a server that is down.
	client, err := fetch.New(fetch.Options{Allowlist: []string{s.Host, "127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	return Options{Sources: client}
}

func serverForm(s *minifluxtest.Server, more url.Values) url.Values {
	form := url.Values{"source": {"miniflux"}, "address": {s.URL}, "token": {minifluxtest.Token}}
	for k, v := range more {
		form[k] = v
	}
	return form
}

// R11: the page of import and export brings an account in from a Miniflux
// server: it says what it did and what it could not carry over, and a second
// run adds nothing twice.
func TestImportFromServer(t *testing.T) {
	mf := minifluxtest.New(t)
	imported(t, withMiniflux(t, mf), func(t *testing.T, s *site) {
		s.asAlice()
		page := s.shown("/subscriptions/transfer")
		for _, want := range []string{`action="/subscriptions/import-server"`, `<option value="miniflux">Miniflux</option>`, `type="password" id="token"`, "Import from another server"} {
			if !strings.Contains(page, want) {
				t.Errorf("GET /subscriptions/transfer: no %q", want)
			}
		}
		had := len(s.feeds("alice"))
		hadEntries := len(s.stored("alice", store.Listing{}))

		a := s.post("/subscriptions/import-server", serverForm(mf, nil))
		if a.status != http.StatusOK {
			t.Fatalf("import: status %d\n%s", a.status, a.body)
		}
		for _, want := range []string{
			"Imported from the server: 3 feeds and 230 entries added, 0 entries updated.", "Not carried over:",
			"<li>rewrite rules not carried over, set on 1 feeds: Plain</li>", "<li>block rules", "<li>keep rules", "<li>fetching through the proxy of Miniflux",
		} {
			if !strings.Contains(a.body, want) {
				t.Errorf("import: no %q in\n%s", want, a.body)
			}
		}
		if strings.Contains(a.body, minifluxtest.Token) {
			t.Error("the page shows the token")
		}
		if got := len(s.feeds("alice")); got != had+3 {
			t.Errorf("%d feeds, want %d", got, had+3)
		}
		if got := len(s.stored("alice", store.Listing{})); got != hadEntries+230 {
			t.Errorf("%d entries, want %d", got, hadEntries+230)
		}

		// The account of the server changed: the second run follows it.
		mf.Change(5, "read", true, "go")
		a = s.post("/subscriptions/import-server", serverForm(mf, nil))
		if a.status != http.StatusOK || !strings.Contains(a.body, "Imported from the server: 0 feeds and 0 entries added, 230 entries updated.") {
			t.Errorf("second import: status %d\n%s", a.status, a.body)
		}
		if got := len(s.stored("alice", store.Listing{})); got != hadEntries+230 {
			t.Errorf("%d entries after the second import, want %d", got, hadEntries+230)
		}
		starred := s.stored("alice", store.Listing{Set: store.EntrySet{OnlyFavorite: true}})
		found := false
		for _, id := range starred {
			if e := s.entry("alice", id); e.Title == "Entry 5" && e.IsRead {
				found = true
			}
		}
		if !found {
			t.Error("Entry 5 is not read and starred after the second import")
		}
	})
}

// R12: what cannot be imported is said, and changes nothing.
func TestImportFromServerRefusals(t *testing.T) {
	mf := minifluxtest.New(t)
	imported(t, withMiniflux(t, mf), func(t *testing.T, s *site) {
		s.asAlice()
		had := len(s.feeds("alice"))
		for name, c := range map[string]struct {
			form   url.Values
			status int
			want   string
		}{
			"unknown software": {serverForm(mf, url.Values{"source": {"other"}}), http.StatusBadRequest, "Choose the software of the server."},
			"no token":         {serverForm(mf, url.Values{"token": {" "}}), http.StatusBadRequest, "Give the address of the server and the API token."},
			"no address":       {serverForm(mf, url.Values{"address": {""}}), http.StatusBadRequest, "Give the address of the server and the API token."},
			"not a web address": {serverForm(mf, url.Values{"address": {"ftp://x.example"}}), http.StatusBadRequest,
				"not an http or https address"},
			"wrong token": {serverForm(mf, url.Values{"token": {"wrong"}}), http.StatusBadRequest, "The server does not accept the API token."},
			"server down": {serverForm(mf, url.Values{"address": {"http://127.0.0.1:1"}}), http.StatusBadGateway, "The server could not be read."},
		} {
			a := s.post("/subscriptions/import-server", c.form)
			if a.status != c.status || !strings.Contains(a.body, c.want) {
				t.Errorf("%s: status %d, want %d with %q", name, a.status, c.status, c.want)
			}
		}
		if got := len(s.feeds("alice")); got != had {
			t.Errorf("refused imports left %d feeds, was %d", got, had)
		}
		// Browsers of other sites cannot make the server read an account.
		req, _ := http.NewRequest(http.MethodPost, "/subscriptions/import-server", strings.NewReader(serverForm(mf, nil).Encode()))
		if a := s.send(req, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "cross-site"}); a.status != http.StatusForbidden {
			t.Errorf("cross-site import: status %d, want 403", a.status)
		}
	})
}

// R13: an address the server may not reach is refused by the guard of its
// client; without the client the page offers nothing.
func TestImportFromServerGuard(t *testing.T) {
	mf := minifluxtest.New(t)
	guarded, err := fetch.New(fetch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	imported(t, Options{Sources: guarded}, func(t *testing.T, s *site) {
		s.asAlice()
		had := len(s.feeds("alice"))
		a := s.post("/subscriptions/import-server", serverForm(mf, nil))
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, "not allowed to reach that address") || len(s.feeds("alice")) != had {
			t.Errorf("import from a private address: status %d, %d feeds, want 400 and %d", a.status, len(s.feeds("alice")), had)
		}
	})
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		if page := s.shown("/subscriptions/transfer"); strings.Contains(page, "import-server") {
			t.Error("a page without a source of accounts offers the import from a server")
		}
		if a := s.post("/subscriptions/import-server", serverForm(mf, nil)); a.status != http.StatusNotFound {
			t.Errorf("import without a source: status %d, want 404", a.status)
		}
	})
}

// R14: the limits of the installation hold for the import, and the page says
// what was left out.
func TestImportFromServerLimits(t *testing.T) {
	mf := minifluxtest.New(t)
	imported(t, withMiniflux(t, mf), func(t *testing.T, s *site) {
		s.asAlice()
		had := len(s.feeds("alice"))
		s.system(func(system *store.System) { system.Limits.MaxFeeds = had + 1 })
		a := s.post("/subscriptions/import-server", serverForm(mf, nil))
		if a.status != http.StatusOK || !strings.Contains(a.body, "2 feeds not imported, the limit of feeds is reached") ||
			!strings.Contains(a.body, "Imported from the server: 1 feeds") {
			t.Errorf("import past the limit of feeds: status %d\n%s", a.status, a.body)
		}
		if got := len(s.feeds("alice")); got != had+1 {
			t.Errorf("%d feeds, want %d", got, had+1)
		}
	})
}
