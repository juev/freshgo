package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
)

// An entry is saved to the Linkding of the user by the server, through the
// API of Linkding, once the user gave a token for it.
func TestSaveToLinkding(t *testing.T) {
	const token = "0123456789abcdef-secret"
	var (
		mu     sync.Mutex
		status = http.StatusCreated
		asked  []string
	)
	linkding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, strings.Join([]string{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)}, " "))
		w.WriteHeader(status)
	}))
	t.Cleanup(linkding.Close)
	answer := func(code int) {
		mu.Lock()
		defer mu.Unlock()
		status, asked = code, nil
	}
	requests := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
	client, err := fetch.New(fetch.Options{Allowlist: []string{strings.TrimPrefix(linkding.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}

	// place is where the Linkding of alice stands among her services.
	place := func(s *site) string {
		list, _ := s.settings("alice")["sharing"].([]any)
		for n, one := range list {
			if service, _ := one.(map[string]any); service["type"] == "linkding" {
				return strconv.Itoa(n)
			}
		}
		t.Fatal("alice has no Linkding among her services")
		return ""
	}
	stored := func(s *site) any {
		n, _ := strconv.Atoi(place(s))
		return s.settings("alice")["sharing"].([]any)[n].(map[string]any)["token"]
	}

	imported(t, Options{Services: client}, func(t *testing.T, s *site) {
		var logs bytes.Buffer
		s.h.log = slog.New(slog.NewTextHandler(&logs, nil))
		s.asAlice()
		id := s.stored("alice", store.Listing{Set: mainStream()})[0]
		e := s.entry("alice", id)
		e.Link = "https://example.org/a?x=1&y=2"
		if err := s.db.UpdateEntry(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		entry := "/entries/" + strconv.FormatInt(id, 10)
		form := `action="` + entry + `/save"`
		// save sends the entry to the service at a place and returns what
		// the page it comes back to says.
		save := func(service string) string {
			t.Helper()
			a := s.post(entry+"/save", url.Values{"service": {service}, "next": {entry}})
			if a.status != http.StatusSeeOther {
				t.Fatalf("saving: status %d, want a redirect", a.status)
			}
			back, _, _ := strings.Cut(a.header.Get("Location"), "#")
			return notice(s.shown(back))
		}

		// Without a token Linkding is a link like every other service.
		s.follow("/settings/integrations", url.Values{"add_type": {"linkding"}, "add_url": {linkding.URL + "/"}})
		if page := s.page(entry); strings.Contains(page, form) || !strings.Contains(page, linkding.URL+"/bookmarks/new?url=") {
			t.Error("without a token the page of an entry does not offer Linkding as a link")
		}
		n := place(s)
		if a := s.post(entry+"/save", url.Values{"service": {n}}); a.status != http.StatusNotFound || len(requests()) != 0 {
			t.Errorf("saving without a token: status %d, %d requests", a.status, len(requests()))
		}

		// The token is stored and never shown.
		settings := s.formAt("/settings/integrations", "/settings/integrations")
		settings.Set("token-"+n, "  "+token+" ")
		s.follow("/settings/integrations", settings)
		if got := stored(s); got != token {
			t.Errorf("stored token = %v", got)
		}
		for _, target := range []string{"/settings/integrations", entry, "/"} {
			if strings.Contains(s.page(target), token) {
				t.Errorf("%s shows the token", target)
			}
		}
		if page := s.page("/settings/integrations"); !strings.Contains(page, "A token is stored") {
			t.Error("the integrations do not say that a token is stored")
		}
		// An empty field keeps it.
		s.follow("/settings/integrations", s.formAt("/settings/integrations", "/settings/integrations"))
		if got := stored(s); got != token {
			t.Errorf("token after the form was saved as shown = %v", got)
		}

		// With a token the way is a form, and the server sends the address.
		if page := s.page(entry); !strings.Contains(page, form) || !strings.Contains(page, `name="service" value="`+n+`"`) {
			t.Error("with a token the page of an entry has no form that saves it")
		}
		if page := s.page("/"); !strings.Contains(page, form) {
			t.Error("with a token the stream has no form that saves the entry")
		}
		said := save(n)
		sent, _ := json.Marshal(map[string]string{"url": e.Link})
		want := "POST /api/bookmarks/ Token " + token + " application/json " + string(sent)
		if got := requests(); said != "Saved to Linkding." || len(got) != 1 || got[0] != want {
			t.Errorf("saving: notice %q, requests %q, want %q", said, got, want)
		}
		// An answer of 200 is a bookmark that was there and is updated.
		answer(http.StatusOK)
		if said := save(n); said != "Saved to Linkding." {
			t.Errorf("saving once more: notice %q", said)
		}

		// The script gets the entry and what to say.
		answer(http.StatusCreated)
		r := httptest.NewRequest(http.MethodPost, entry+"/save", strings.NewReader(url.Values{"service": {n}, "next": {"/"}}.Encode()))
		a := s.send(r, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin", fragmentHeader: "entry"})
		if said, _ := url.QueryUnescape(a.header.Get("X-Notice")); a.status != http.StatusOK || said != "Saved to Linkding." || !strings.Contains(a.body, form) {
			t.Errorf("saving by the script: status %d, notice %q", a.status, said)
		}

		// What Linkding refuses is said, and how.
		for code, want := range map[int]string{
			http.StatusUnauthorized:        "Linkding refused the token: see Settings, Integrations.",
			http.StatusInternalServerError: "The entry could not be saved to Linkding. The log of the server says why.",
		} {
			answer(code)
			if said := save(n); said != want {
				t.Errorf("Linkding answered %d: notice %q, want %q", code, said, want)
			}
		}
		if log := logs.String(); !strings.Contains(log, "entry was not saved to the service") || strings.Contains(log, token) {
			t.Errorf("the log does not say why, or says the token:\n%s", log)
		}

		// Not a service that saves, not a service, not an entry with a link.
		answer(http.StatusCreated)
		for _, service := range []string{"99", "-1", "x", ""} {
			if a := s.post(entry+"/save", url.Values{"service": {service}}); a.status != http.StatusNotFound {
				t.Errorf("saving to the service %q: status %d", service, a.status)
			}
		}
		answer(http.StatusCreated)
		e.Link = "javascript:alert(1)"
		if err := s.db.UpdateEntry(t.Context(), e); err != nil {
			t.Fatal(err)
		}
		if a := s.post(entry+"/save", url.Values{"service": {n}}); a.status != http.StatusNotFound || len(requests()) != 0 {
			t.Errorf("saving an entry without an address of the web: status %d, %d requests", a.status, len(requests()))
		}

		// The token is forgotten when asked, and the service is a link again.
		settings = s.formAt("/settings/integrations", "/settings/integrations")
		settings.Set("forget-"+n, "1")
		s.follow("/settings/integrations", settings)
		if got := stored(s); got != nil {
			t.Errorf("token after it was forgotten = %v", got)
		}
	})
}

// A server that sends nothing itself offers Linkding as a link, token or not.
func TestLinkdingOnAServerThatSendsNothing(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		s.follow("/settings/integrations", url.Values{"add_type": {"linkding"}, "add_url": {"https://links.example"}, "add_token": {"secret-token"}})
		id := s.stored("alice", store.Listing{Set: mainStream()})[0]
		entry := "/entries/" + strconv.FormatInt(id, 10)
		if a := s.post(entry+"/save", url.Values{"service": {"0"}}); a.status != http.StatusNotFound {
			t.Errorf("saving on a server that sends nothing: status %d", a.status)
		}
		if strings.Contains(s.page("/"), `/save"`) {
			t.Error("a server that sends nothing offers to save")
		}
	})
}
