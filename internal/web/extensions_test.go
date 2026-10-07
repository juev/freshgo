package web

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/hooks"
)

// R18: a handler adds a place to the menu and to the palette and a block
// before the button of the login page, and is told whom the page is for.
func TestInterfaceHooks(t *testing.T) {
	registry := &hooks.Registry{}
	registry.NavMenu.Add(0, func(_ context.Context, p hooks.Page) []hooks.Link {
		who := "a visitor"
		if p.User != nil {
			who = p.User.Name
		}
		links := []hooks.Link{{Name: "Tool <" + p.Language + "> of " + who, URL: "/api/misc.php/tool"}}
		if p.Admin {
			links = append(links, hooks.Link{Name: "Elsewhere", URL: "https://example.org/x?a=1&b=2"})
		}
		return links
	})
	registry.NavMenu.Add(1, func(context.Context, hooks.Page) []hooks.Link {
		return []hooks.Link{{Name: "Not a link", URL: "javascript:alert(1)"}}
	})
	registry.BeforeLogin.Add(0, func(_ context.Context, p hooks.Page) []template.HTML {
		if p.User != nil {
			t.Errorf("the login page was made for %q", p.User.Name)
		}
		return []template.HTML{`<p class="sso"><a href="/api/misc.php/sso">Single sign-on</a></p>`}
	})
	imported(t, Options{Hooks: registry, BaseURL: "https://example.net/rss/"}, func(t *testing.T, s *site) {
		body := s.page("/login")
		block, button := strings.Index(body, `<p class="sso"><a href="/api/misc.php/sso">Single sign-on</a></p>`), strings.Index(body, `<button type="submit">Sign in</button>`)
		if block < 0 || button < block {
			t.Errorf("the block is at %d, the button at %d\n%s", block, button, body)
		}
		if !strings.Contains(body, `<li><a href="/rss/api/misc.php/tool">Tool &lt;en&gt; of a visitor</a></li>`) || strings.Contains(body, "Elsewhere") {
			t.Errorf("the menu of a visitor\n%s", body)
		}
		// A failed login shows the form, and the block, again.
		if a := s.login("alice", "wrong", nil); a.status != http.StatusUnauthorized || !strings.Contains(a.body, `<p class="sso">`) {
			t.Errorf("a failed login: status %d\n%s", a.status, a.body)
		}

		s.setting("alice", "language", "ru")
		s.asAlice()
		body = s.page("/about")
		if !strings.Contains(body, `<li><a href="/rss/api/misc.php/tool">Tool &lt;ru&gt; of alice</a></li>`) ||
			!strings.Contains(body, `<li><a href="https://example.org/x?a=1&amp;b=2">Elsewhere</a></li>`) {
			t.Errorf("the menu of an administrator\n%s", body)
		}
		// An address that is not that of a page is left out.
		if strings.Contains(body, "Not a link") || strings.Contains(s.page("/palette"), "Not a link") {
			t.Errorf("the menu links to a script\n%s", body)
		}
		var places []place
		if err := json.Unmarshal([]byte(s.page("/palette")), &places); err != nil {
			t.Fatal(err)
		}
		for _, want := range []place{{"Tool <ru> of alice", "/rss/api/misc.php/tool", "Расширение"}, {"Elsewhere", "https://example.org/x?a=1&b=2", "Расширение"}} {
			if !slices.Contains(places, want) {
				t.Errorf("the palette lacks %+v among %+v", want, places)
			}
		}
	})
}

// R18: without handlers the pages are what they were.
func TestNoInterfaceHooks(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		body := s.page("/login")
		if !strings.Contains(body, "</p>\n<p><button type=\"submit\">Sign in</button></p>") {
			t.Errorf("the login page\n%s", body)
		}
		s.asAlice()
		var places []place
		if err := json.Unmarshal([]byte(s.page("/palette")), &places); err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(places, func(p place) bool { return p.Kind == "Extension" }) {
			t.Errorf("the palette has places of extensions: %+v", places)
		}
	})
}
