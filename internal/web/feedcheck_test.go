package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
)

// U105: the form of a feed requests it without a proxy and through every
// proxy there is, says what each came to and what to do, and stores nothing.
func TestCheckFeedFromItsForm(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		remote := newFeedSite(t)
		remote.serve("/feed.xml", "application/rss+xml", rssOf(remote.URL, "The Blog", "one"))
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, rssOf(remote.URL, "The Blog", "one"))
		}))
		t.Cleanup(proxy.Close)
		// The proxy of the installation is read as the server reads it.
		client, err := fetch.New(fetch.Options{Allowlist: []string{"*"}, Proxy: func(ctx context.Context) (fetch.InstallationProxy, error) {
			system, err := s.db.System(ctx)
			if err != nil {
				return fetch.InstallationProxy{}, err
			}
			address, err := fetch.ParseProxy(system.Proxy)
			return fetch.InstallationProxy{URL: address, ChosenOnly: system.ProxyChosenOnly}, err
		}})
		if err != nil {
			t.Fatal(err)
		}
		s.h.refresher = refresh.New(s.db, client, &hooks.Registry{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		installation := func(address string, chosenOnly bool) {
			t.Helper()
			system, err := s.db.System(ctx)
			if err != nil {
				t.Fatal(err)
			}
			system.Proxy, system.ProxyChosenOnly = address, chosenOnly
			if err := s.db.SetSystem(ctx, system); err != nil {
				t.Fatal(err)
			}
		}

		s.asAlice()
		s.follow("/subscriptions/feeds", url.Values{"url": {remote.URL + "/feed.xml"}})
		if body := s.page("/subscriptions/feeds/9"); !strings.Contains(body, `formaction="/subscriptions/feeds/9/check"`) {
			t.Fatalf("the form of the feed has no button to check it:\n%s", body)
		}
		before := s.feed("alice", 9)
		check := func(form url.Values) string {
			t.Helper()
			a := s.post("/subscriptions/feeds/9/check", form)
			if a.status != http.StatusOK {
				t.Fatalf("POST /subscriptions/feeds/9/check: status %d\n%s", a.status, a.body)
			}
			start := strings.Index(a.body, `aria-labelledby="check-heading"`)
			if start < 0 {
				t.Fatalf("the page shows no result of the check:\n%s", a.body)
			}
			return a.body[start : start+strings.Index(a.body[start:], "</section>")]
		}
		form := s.feedForm(9)

		// The feed answers: nothing to do about it.
		if got := check(form); !strings.Contains(got, "<li>Without a proxy: answers</li>") || strings.Contains(got, "<p>") ||
			strings.Contains(got, "installation") {
			t.Errorf("a feed that answers, no proxy anywhere:\n%s", got)
		}

		// The site refuses the server and there is no proxy to try: the
		// administrator is told where one is set.
		remote.mu.Lock()
		remote.pages["/feed.xml"] = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not from there", http.StatusForbidden) }
		remote.mu.Unlock()
		if got := check(form); !strings.Contains(got, "<li>Without a proxy: HTTP 403 Forbidden</li>") ||
			!strings.Contains(got, "the installation has no proxy to try. One is set on the page") {
			t.Errorf("a feed that is refused, no proxy anywhere:\n%s", got)
		}

		// Through the proxy of the installation it answers, and the feed
		// has not chosen it: the form says to.
		installation(proxy.URL, true)
		got := check(form)
		if !strings.Contains(got, "<li>Without a proxy: HTTP 403 Forbidden</li>") ||
			!strings.Contains(got, "<li>Through the proxy of the installation: answers</li>") ||
			!strings.Contains(got, "The feed answers through the proxy of the installation and not directly: choose") {
			t.Errorf("a feed that answers through the proxy of the installation alone:\n%s", got)
		}
		// Chosen in the form, saved or not, there is nothing more to say.
		form.Set("proxy_type", "installation")
		if got := check(form); !strings.Contains(got, "<li>Through the proxy of the installation: answers</li>") || strings.Contains(got, "<p>") {
			t.Errorf("a feed that has chosen the proxy of the installation:\n%s", got)
		}
		// Nor when the proxy is for every feed and the feed is left to that.
		form.Set("proxy_type", "")
		installation(proxy.URL, false)
		if got := check(form); !strings.Contains(got, "<li>Through the proxy of the installation: answers</li>") || strings.Contains(got, "<p>") {
			t.Errorf("a feed left to a proxy that is for every feed:\n%s", got)
		}
		// A feed set to go through none is told to choose it all the same.
		form.Set("proxy_type", "-1")
		if got := check(form); !strings.Contains(got, "not directly: choose") {
			t.Errorf("a feed set to go through no proxy while only the proxy answers:\n%s", got)
		}

		// A proxy of the feed's own is a way of its own; it answers, so
		// there is nothing to do.
		installation("", false)
		form.Set("proxy_type", "0")
		form.Set("proxy", strings.TrimPrefix(proxy.URL, "http://"))
		if got := check(form); !strings.Contains(got, "<li>Without a proxy: HTTP 403 Forbidden</li>") ||
			!strings.Contains(got, "<li>Through the proxy of the feed: answers</li>") || strings.Contains(got, "<p>") {
			t.Errorf("a feed with a proxy of its own:\n%s", got)
		}
		// A kind of proxy the server does not go through.
		form.Set("proxy_type", "4")
		if got := check(form); !strings.Contains(got, "<li>Through the proxy of the feed: ") || strings.Contains(got, "Through the proxy of the feed: answers") {
			t.Errorf("a feed with a proxy of a kind there is no way through:\n%s", got)
		}

		// Nothing of all this was stored.
		if after := s.feed("alice", 9); string(after.Attributes) != string(before.Attributes) || after.LastUpdate != before.LastUpdate ||
			after.Error != before.Error || after.Failures != before.Failures {
			t.Errorf("the feed after the checks = %+v, before them %+v", after, before)
		}
	})
}
