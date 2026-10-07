package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

// R13: every sharing service of FreshRSS is there, and opens for an entry
// the address FreshRSS 1.30.1 opens: the oracle is what its own list and the
// encoders of PHP make of the cases.
func TestShareServicesMatchFreshRSS(t *testing.T) {
	var cases []struct{ ID, Base, Title, Link string }
	var oracle []struct {
		Type, Name, Help string
		Advanced, Button bool
		URLs             []string
	}
	for file, into := range map[string]any{"share-cases.json": &cases, "share.json": &oracle} {
		raw, err := os.ReadFile("../../testdata/reference/oracle/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatal(err)
		}
	}
	if len(oracle) != 34 || len(shareServices) != len(oracle) {
		t.Fatalf("%d services here, %d in FreshRSS; want 34 of each", len(shareServices), len(oracle))
	}
	for i, want := range oracle {
		got := shareServices[i]
		if got.id != want.Type || got.name != want.Name || got.advanced != want.Advanced || got.button != want.Button || got.help != want.Help {
			t.Errorf("service %d = %+v, want %+v", i, got, want)
			continue
		}
		for n, c := range cases {
			// FreshRSS writes the address for an HTML attribute.
			address := strings.ReplaceAll(want.URLs[n], "&amp;", "&")
			if target := got.target(c.Base, c.ID, c.Title, c.Link); target != address {
				t.Errorf("%s, case %d:\n got %s\nwant %s", got.id, n, target, address)
			}
		}
	}
}

// R13: the services a user switches on are offered at every entry; a
// setting FreshRSS left works as it is.
func TestSharing(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		if body := s.page("/feeds/1?state=all"); strings.Contains(body, `class="menu share"`) {
			t.Error("an entry offers to be shared before any service is switched on")
		}
		s.shown("/settings/integrations")
		// What FreshRSS stores: services by type, with the fields of its form.
		s.setting("alice", "sharing", []map[string]any{
			{"type": "mastodon", "name": "My Mastodon", "url": "https://social.example/", "method": "GET"},
			{"type": "email", "name": "Email"},
			{"type": "clipboard", "name": "Clipboard"},
			{"type": "gone-service", "name": "Unknown"},
			{"type": "wallabag", "name": "No address"},
			{"type": "shaarli", "name": "Bad", "url": "javascript:alert(1)//"},
		})
		id := s.stored("alice", store.Listing{Set: store.EntrySet{FeedID: 1}})[0]
		e := s.entry("alice", id)
		body := s.page("/feeds/1?state=all")
		link, title := url.QueryEscape(e.Link), strings.ReplaceAll(url.QueryEscape(e.Title), "+", "%20")
		for _, want := range []string{
			`<summary>Share…</summary>`,
			`<a href="https://social.example/share?title=` + title + `&amp;url=` + link + `" target="_blank" rel="noopener noreferrer">My Mastodon</a>`,
			`<a href="mailto:?subject=` + title + `&amp;body=` + link + `" target="_blank" rel="noopener noreferrer">Email</a>`,
			`<button type="button" class="link" data-share="clipboard" data-link="` + e.Link + `"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the entry lacks %q in its menu:\n%.3000s", want, body[strings.Index(body, `class="menu share"`):])
			}
		}
		for _, unwanted := range []string{"Unknown", "No address", "javascript:"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("the menu offers %q", unwanted)
			}
		}
		if page := s.page("/entries/" + strconv.FormatInt(id, 10)); !strings.Contains(page, `>My Mastodon</a>`) {
			t.Error("the page of an entry does not offer to share it")
		}

		// The form keeps, renames, removes and adds.
		form := s.formAt("/settings/integrations", "/settings/integrations")
		if form.Get("name-0") != "My Mastodon" || form.Get("url-0") != "https://social.example/" || form.Get("name-1") != "Email" {
			t.Errorf("form of the integrations = %v", form)
		}
		form.Set("name-0", "Fediverse")
		form.Set("remove-1", "1")
		form.Set("remove-4", "1")
		form.Set("remove-5", "1")
		form.Set("add_type", "wallabagv2")
		form.Set("add_url", "https://wallabag.example")
		s.follow("/settings/integrations", form)
		want := []any{
			map[string]any{"type": "mastodon", "name": "Fediverse", "url": "https://social.example/", "method": "GET"},
			map[string]any{"type": "clipboard", "name": "Clipboard"},
			map[string]any{"type": "wallabagv2", "name": "wallabag v2", "url": "https://wallabag.example"},
		}
		if got := s.settings("alice")["sharing"]; !reflect.DeepEqual(got, want) {
			t.Errorf("sharing after the form = %v\nwant %v", got, want)
		}
		if body := s.page("/feeds/1?state=all"); !strings.Contains(body, `<a href="https://wallabag.example/bookmarklet?url=`+strings.ReplaceAll(link, "+", "%20")+`" target="_blank"`) {
			t.Error("the service added by the form is not offered")
		}
		// A service that needs an address does not go without one.
		a := s.post("/settings/integrations", url.Values{"add_type": {"shaarli"}, "add_url": {"shaarli.example"}, "name-0": {"Kept"}, "url-0": {"https://social.example/"}})
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, "has to start with http") || !strings.Contains(a.body, `value="shaarli.example"`) {
			t.Errorf("a service without a usable address: status %d", a.status)
		}
		if got := s.settings("alice")["sharing"]; !reflect.DeepEqual(got, want) {
			t.Errorf("sharing after a refused form = %v", got)
		}
	})
}
