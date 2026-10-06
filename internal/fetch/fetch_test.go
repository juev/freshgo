package fetch

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newClient returns a client that may reach the given test servers, which
// all listen on loopback.
func newClient(t *testing.T, o Options, servers ...*httptest.Server) *Client {
	t.Helper()
	for _, s := range servers {
		o.Allowlist = append(o.Allowlist, hostOf(s))
	}
	if o.UserAgent == "" {
		o.UserAgent = "freshgo/test"
	}
	c, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func hostOf(s *httptest.Server) string {
	u, _ := url.Parse(s.URL)
	return u.Host
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestFetchSendsDefaults(t *testing.T) {
	var got http.Header
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
		_, _ = io.WriteString(w, "<feed/>")
	})
	c := newClient(t, Options{}, s)

	res, err := c.Fetch(context.Background(), Request{URL: s.URL + "/atom.xml", Accept: AcceptFeed})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "<feed/>" || res.NotModified {
		t.Errorf("body %q, not modified %v", res.Body, res.NotModified)
	}
	if res.URL != s.URL+"/atom.xml" || res.PermanentURL != "" {
		t.Errorf("URL %q, permanent %q", res.URL, res.PermanentURL)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/atom+xml; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	if got.Get("Accept") != AcceptFeed || got.Get("User-Agent") != "freshgo/test" {
		t.Errorf("request headers: Accept %q, User-Agent %q", got.Get("Accept"), got.Get("User-Agent"))
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" || got.Get("If-None-Match") != "" {
		t.Errorf("unexpected request headers: %v", got)
	}
}

func TestConditionalRequest(t *testing.T) {
	const etag, modified = `"v1"`, "Tue, 06 Oct 2026 10:00:00 GMT"
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", modified)
		if r.Header.Get("If-None-Match") == etag || r.Header.Get("If-Modified-Since") == modified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, "content")
	})
	c := newClient(t, Options{}, s)

	first, err := c.Fetch(context.Background(), Request{URL: s.URL})
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.NotModified || first.Header.Get("ETag") != etag || first.Header.Get("Last-Modified") != modified {
		t.Fatalf("first response: not modified %v, headers %v", first.NotModified, first.Header)
	}
	for name, req := range map[string]Request{
		"etag":          {URL: s.URL, ETag: etag},
		"last modified": {URL: s.URL, LastModified: modified},
	} {
		res, err := c.Fetch(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !res.NotModified || len(res.Body) != 0 {
			t.Errorf("%s: not modified %v, body %q", name, res.NotModified, res.Body)
		}
	}
	res, err := c.Fetch(context.Background(), Request{URL: s.URL, ETag: `"v0"`})
	if err != nil || res.NotModified || string(res.Body) != "content" {
		t.Errorf("stale validator: %v, %+v", err, res)
	}
}

// chain serves /0, /1, … where step i answers with statuses[i] and points at
// the next step; the step after the last status answers 200.
func chain(t *testing.T, statuses ...int) *httptest.Server {
	t.Helper()
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		var i int
		_, _ = fmt.Sscanf(r.URL.Path, "/%d", &i)
		if i < len(statuses) {
			w.Header().Set("Location", fmt.Sprintf("/%d", i+1))
			w.WriteHeader(statuses[i])
			return
		}
		_, _ = io.WriteString(w, "end")
	})
}

func TestRedirectPermanentURL(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []int
		permanent string
	}{
		{"none", nil, ""},
		{"single 301", []int{301}, "/1"},
		{"single 308", []int{308}, "/1"},
		{"chain of permanent", []int{301, 308, 301}, "/3"},
		{"single 302", []int{302}, ""},
		{"single 307", []int{307}, ""},
		{"single 303", []int{303}, ""},
		{"permanent then temporary", []int{301, 302}, "/1"},
		{"temporary then permanent", []int{302, 301}, ""},
		{"permanent, temporary, permanent", []int{301, 302, 301}, "/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := chain(t, tt.statuses...)
			c := newClient(t, Options{}, s)
			res, err := c.Fetch(context.Background(), Request{URL: s.URL + "/0"})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if want := fmt.Sprintf("%s/%d", s.URL, len(tt.statuses)); res.URL != want {
				t.Errorf("URL %q, want %q", res.URL, want)
			}
			want := ""
			if tt.permanent != "" {
				want = s.URL + tt.permanent
			}
			if res.PermanentURL != want {
				t.Errorf("PermanentURL %q, want %q", res.PermanentURL, want)
			}
			if string(res.Body) != "end" {
				t.Errorf("body %q", res.Body)
			}
		})
	}
}

func TestRedirectLimit(t *testing.T) {
	tests := []struct {
		name      string
		redirects int
		params    Params
		ok        bool
	}{
		{"default allows four", 4, Params{}, true},
		{"default refuses five", 5, Params{}, false},
		{"feed limit allows", 6, Params{MaxRedirects: 6}, true},
		{"feed limit refuses", 3, Params{MaxRedirects: 2}, false},
		{"unlimited is capped", redirectsCap, Params{MaxRedirects: -1}, true},
		{"unlimited over the cap", redirectsCap + 1, Params{MaxRedirects: -1}, false},
		{"redirects off", 1, Params{NoRedirects: true}, false},
		{"redirects off, none met", 0, Params{NoRedirects: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statuses := make([]int, tt.redirects)
			for i := range statuses {
				statuses[i] = http.StatusFound
			}
			s := chain(t, statuses...)
			c := newClient(t, Options{}, s)
			_, err := c.Fetch(context.Background(), Request{URL: s.URL + "/0", Params: tt.params})
			if tt.ok && err != nil {
				t.Errorf("Fetch: %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrTooManyRedirects) {
				t.Errorf("Fetch: %v, want ErrTooManyRedirects", err)
			}
		})
	}
}

func TestRedirectToOtherSchemeIsRefused(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "file:///etc/passwd")
		w.WriteHeader(http.StatusFound)
	})
	c := newClient(t, Options{}, s)
	if _, err := c.Fetch(context.Background(), Request{URL: s.URL}); !errors.Is(err, ErrBadURL) {
		t.Errorf("Fetch: %v, want ErrBadURL", err)
	}
	for _, raw := range []string{"ftp://example.org/feed", "example.org/feed", "http://", ""} {
		if _, err := c.Fetch(context.Background(), Request{URL: raw}); !errors.Is(err, ErrBadURL) {
			t.Errorf("Fetch(%q): %v, want ErrBadURL", raw, err)
		}
	}
}

func TestRedirectAcrossOriginsDropsCredentials(t *testing.T) {
	var got http.Header
	target := serve(t, func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() })
	source := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, target.URL+"/landed", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/landed", http.StatusFound)
		default:
			got = r.Header.Clone()
		}
	})
	c := newClient(t, Options{}, source, target)
	params := Params{
		BasicAuth: "alice:secret",
		Cookie:    "session=1",
		Header:    http.Header{"X-Api-Key": {"k"}, "Cookie": {"extra=2"}},
	}

	if _, err := c.Fetch(context.Background(), Request{URL: source.URL + "/here", Params: params}); err != nil {
		t.Fatalf("same origin: %v", err)
	}
	if user, password, ok := basicAuth(got); !ok || user != "alice" || password != "secret" {
		t.Errorf("same origin: credentials %q:%q, sent %v", user, password, ok)
	}
	if cookies := strings.Join(got.Values("Cookie"), "; "); cookies != "extra=2; session=1" {
		t.Errorf("same origin: cookies %q", cookies)
	}

	if _, err := c.Fetch(context.Background(), Request{URL: source.URL + "/away", Params: params}); err != nil {
		t.Fatalf("other origin: %v", err)
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" {
		t.Errorf("other origin: credentials leaked: %v", got)
	}
	if got.Get("X-Api-Key") != "k" {
		t.Errorf("other origin: custom header lost: %v", got)
	}

	// The same for credentials the feed carries as a header of its own.
	bearer := Params{Header: http.Header{"Authorization": {"Bearer token"}}}
	if _, err := c.Fetch(context.Background(), Request{URL: source.URL + "/here", Params: bearer}); err != nil {
		t.Fatalf("same origin, header: %v", err)
	}
	if got.Get("Authorization") != "Bearer token" {
		t.Errorf("same origin: Authorization %q", got.Get("Authorization"))
	}
	if _, err := c.Fetch(context.Background(), Request{URL: source.URL + "/away", Params: bearer}); err != nil {
		t.Fatalf("other origin, header: %v", err)
	}
	if got.Get("Authorization") != "" {
		t.Errorf("other origin: Authorization header leaked: %v", got)
	}
}

func basicAuth(h http.Header) (string, string, bool) {
	r := http.Request{Header: h}
	return r.BasicAuth()
}

func TestAuthorizationHeaderReplacesBasicAuth(t *testing.T) {
	var got http.Header
	s := serve(t, func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() })
	c := newClient(t, Options{}, s)
	params := Params{BasicAuth: "alice:secret", Header: http.Header{"Authorization": {"Bearer token"}}}
	if _, err := c.Fetch(context.Background(), Request{URL: s.URL, Params: params}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if auth := got.Values("Authorization"); len(auth) != 1 || auth[0] != "Bearer token" {
		t.Errorf("Authorization %q", auth)
	}
}

func TestRedirectOfPost(t *testing.T) {
	type seen struct{ method, body, contentType string }
	for _, tt := range []struct {
		status int
		want   seen
	}{
		{http.StatusMovedPermanently, seen{"GET", "", ""}},
		{http.StatusFound, seen{"GET", "", ""}},
		{http.StatusSeeOther, seen{"GET", "", ""}},
		{http.StatusTemporaryRedirect, seen{"POST", `{"q":1}`, "application/json"}},
		{http.StatusPermanentRedirect, seen{"POST", `{"q":1}`, "application/json"}},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			var first, last seen
			s := serve(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				got := seen{r.Method, string(body), r.Header.Get("Content-Type")}
				if r.URL.Path == "/start" {
					first = got
					http.Redirect(w, r, "/end", tt.status)
					return
				}
				last = got
			})
			c := newClient(t, Options{}, s)
			params := Params{Post: true, PostBody: `{"q":1}`, Header: http.Header{"Content-Type": {"application/json"}}}
			if _, err := c.Fetch(context.Background(), Request{URL: s.URL + "/start", Params: params}); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if want := (seen{"POST", `{"q":1}`, "application/json"}); first != want {
				t.Errorf("first request %+v, want %+v", first, want)
			}
			if last != tt.want {
				t.Errorf("request after redirect %+v, want %+v", last, tt.want)
			}
		})
	}
}

func TestPostDefaultsToFormContentType(t *testing.T) {
	var contentType, body string
	s := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		contentType, body = r.Header.Get("Content-Type"), string(b)
	})
	c := newClient(t, Options{}, s)
	if _, err := c.Fetch(context.Background(), Request{URL: s.URL, Params: Params{Post: true, PostBody: "a=1&b=2"}}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if contentType != "application/x-www-form-urlencoded" || body != "a=1&b=2" {
		t.Errorf("Content-Type %q, body %q", contentType, body)
	}
}

func TestCookieEngineKeepsCookiesAlongRedirects(t *testing.T) {
	for _, engine := range []bool{true, false} {
		var cookie string
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" {
				http.SetCookie(w, &http.Cookie{Name: "pass", Value: "granted", Path: "/"})
				http.Redirect(w, r, "/feed", http.StatusFound)
				return
			}
			cookie = r.Header.Get("Cookie")
		})
		c := newClient(t, Options{}, s)
		if _, err := c.Fetch(context.Background(), Request{URL: s.URL + "/login", Params: Params{CookieEngine: engine}}); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		want := ""
		if engine {
			want = "pass=granted"
		}
		if cookie != want {
			t.Errorf("cookie engine %v: cookie %q, want %q", engine, cookie, want)
		}
	}
}

func TestStatusErrors(t *testing.T) {
	for _, status := range []int{204, 400, 401, 403, 404, 410, 500} {
		s := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		c := newClient(t, Options{}, s)
		_, err := c.Fetch(context.Background(), Request{URL: s.URL})
		var se *StatusError
		if !errors.As(err, &se) || se.Code != status {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

func TestRedirectStatusWithoutLocationIsAnError(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) })
	c := newClient(t, Options{}, s)
	_, err := c.Fetch(context.Background(), Request{URL: s.URL})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusFound {
		t.Errorf("Fetch: %v", err)
	}
}

func TestRetryAfterPausesHost(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var hits atomic.Int32
			s := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 1 {
					w.Header().Set("Retry-After", "120")
					w.WriteHeader(status)
				}
			})
			other := serve(t, func(http.ResponseWriter, *http.Request) {})
			c := newClient(t, Options{}, s, other)
			now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
			c.now = func() time.Time { return now }

			_, err := c.Fetch(context.Background(), Request{URL: s.URL + "/a"})
			var se *StatusError
			if !errors.As(err, &se) || se.Code != status {
				t.Fatalf("first Fetch: %v", err)
			}

			// Another document of the same host is not requested either.
			now = now.Add(119 * time.Second)
			_, err = c.Fetch(context.Background(), Request{URL: s.URL + "/b"})
			var ra *RetryAfterError
			if !errors.As(err, &ra) {
				t.Fatalf("Fetch during the pause: %v", err)
			}
			if want := time.Date(2026, 10, 6, 12, 2, 0, 0, time.UTC); !ra.Until.Equal(want) || ra.Host != hostOf(s) {
				t.Errorf("pause of %q until %v, want %q until %v", ra.Host, ra.Until, hostOf(s), want)
			}
			if hits.Load() != 1 {
				t.Errorf("%d requests reached the host during the pause", hits.Load()-1)
			}
			if _, err := c.Fetch(context.Background(), Request{URL: other.URL}); err != nil {
				t.Errorf("another host during the pause: %v", err)
			}

			now = now.Add(time.Second)
			if _, err := c.Fetch(context.Background(), Request{URL: s.URL + "/b"}); err != nil {
				t.Errorf("Fetch after the pause: %v", err)
			}
			if hits.Load() != 2 {
				t.Errorf("%d requests in total, want 2", hits.Load())
			}
		})
	}
}

func TestRetryAfterValues(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"60", time.Minute},
		{" 60 ", time.Minute},
		{"0", 0},
		{"Tue, 06 Oct 2026 12:30:00 GMT", 30 * time.Minute},
		{"", retryAfterDefault},
		{"soon", retryAfterDefault},
		{"-5", retryAfterDefault},
		{"999999999", retryAfterMax},
		{"4294967296", retryAfterMax},
		{"99999999999999999999999", retryAfterDefault},
		{"Fri, 06 Nov 2026 12:00:00 GMT", retryAfterMax},
	}
	for _, tt := range tests {
		c := newClient(t, Options{})
		c.now = func() time.Time { return now }
		c.pause("host", tt.header)
		until := c.retryAfter["host"]
		if got := until.Sub(now); got != tt.want {
			t.Errorf("Retry-After %q: pause of %v, want %v", tt.header, got, tt.want)
		}
	}
}

func TestInternalAddressesAreRefused(t *testing.T) {
	var hits atomic.Int32
	s := serve(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	u, _ := url.Parse(s.URL)

	refused := func(name string, allowlist ...string) {
		t.Helper()
		c := newClient(t, Options{Allowlist: allowlist})
		if _, err := c.Fetch(context.Background(), Request{URL: s.URL}); !errors.Is(err, ErrForbiddenAddress) {
			t.Errorf("%s: %v, want ErrForbiddenAddress", name, err)
		}
	}
	allowed := func(name, target string, allowlist ...string) {
		t.Helper()
		c := newClient(t, Options{Allowlist: allowlist})
		if _, err := c.Fetch(context.Background(), Request{URL: target}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	refused("empty allowlist")
	refused("another port", "127.0.0.1:1")
	refused("another range", "10.0.0.0/8")
	refused("another name", "localhost:1")
	if hits.Load() != 0 {
		t.Fatalf("%d requests reached a refused address", hits.Load())
	}
	allowed("ip:port", s.URL, u.Host)
	allowed("range", s.URL, "127.0.0.0/8")
	allowed("everything", s.URL, "*")
	allowed("name:port", "http://localhost:"+u.Port(), "LOCALHOST:"+u.Port())

	if _, err := New(Options{Allowlist: []string{"10.0.0.0/40"}}); err == nil {
		t.Error("a malformed range in the allowlist was accepted")
	}
}

func TestRedirectToInternalAddressIsRefused(t *testing.T) {
	var hits atomic.Int32
	inner := serve(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	outer := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL+"/admin", http.StatusFound)
	})
	c := newClient(t, Options{}, outer)
	if _, err := c.Fetch(context.Background(), Request{URL: outer.URL}); !errors.Is(err, ErrForbiddenAddress) {
		t.Errorf("Fetch: %v, want ErrForbiddenAddress", err)
	}
	if hits.Load() != 0 {
		t.Error("the redirect target was requested")
	}
}

func TestGuardAddresses(t *testing.T) {
	g, err := newGuard([]string{"192.168.1.0/24", "10.0.0.5:8080", "[fd00::1]:443"})
	if err != nil {
		t.Fatalf("newGuard: %v", err)
	}
	tests := []struct {
		address string
		allowed bool
	}{
		{"93.184.216.34:80", true},
		{"[2606:2800:220:1:248:1893:25c8:1946]:443", true},
		{"127.0.0.1:80", false},
		{"0.0.0.0:80", false},
		{"10.1.2.3:80", false},
		{"172.16.0.1:80", false},
		{"172.32.0.1:80", true},
		{"192.168.0.1:80", false},
		{"169.254.169.254:80", false},
		{"100.64.0.1:80", false},
		{"224.0.0.1:80", false},
		{"255.255.255.255:80", false},
		{"[::1]:80", false},
		{"[::]:80", false},
		{"[fe80::1%eth0]:80", false},
		{"[fc00::1]:80", false},
		{"[ff02::1]:80", false},
		// IPv4 inside IPv6.
		{"[::ffff:127.0.0.1]:80", false},
		{"[::ffff:93.184.216.34]:80", true},
		{"[64:ff9b::7f00:1]:80", false},
		{"[64:ff9b::a00:1]:80", false},
		{"[64:ff9b::5db8:d822]:80", true},
		{"[64:ff9b:1::1]:80", false},
		// The allowlist.
		{"192.168.1.20:80", true},
		{"[::ffff:192.168.1.20]:80", true},
		{"192.168.2.20:80", false},
		{"10.0.0.5:8080", true},
		{"10.0.0.5:80", false},
		{"[fd00::1]:443", true},
		{"[fd00::1]:80", false},
	}
	for _, tt := range tests {
		ap, err := netip.ParseAddrPort(tt.address)
		if err != nil {
			t.Fatalf("%s: %v", tt.address, err)
		}
		if got := g.allowed(ap); got != tt.allowed {
			t.Errorf("%s: allowed %v, want %v", tt.address, got, tt.allowed)
		}
	}
}

func TestBodySizeLimit(t *testing.T) {
	const limit = 1024
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/%d", &n)
		body := strings.Repeat("a", n)
		if r.URL.Query().Has("gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = io.WriteString(zw, body)
			_ = zw.Close()
			return
		}
		_, _ = io.WriteString(w, body)
	})
	c := newClient(t, Options{MaxBodySize: limit}, s)

	for _, target := range []string{"/1024", "/1024?gzip"} {
		res, err := c.Fetch(context.Background(), Request{URL: s.URL + target})
		if err != nil || len(res.Body) != limit {
			t.Errorf("%s: %v, want a body of %d bytes", target, err, limit)
		}
	}
	// The compressed body of the second request is a few dozen bytes: the
	// limit counts what it unpacks to.
	for _, target := range []string{"/1025", "/1000000?gzip"} {
		if _, err := c.Fetch(context.Background(), Request{URL: s.URL + target}); !errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("%s: %v, want ErrBodyTooLarge", target, err)
		}
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	c := newClient(t, Options{Timeout: 50 * time.Millisecond}, s)
	for _, path := range []string{"/headers", "/body"} {
		if _, err := c.Fetch(context.Background(), Request{URL: s.URL + path}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v, want a deadline error", path, err)
		}
	}

	// The feed's own timeout replaces the client's.
	slow := newClient(t, Options{Timeout: 3 * time.Second}, s)
	start := time.Now()
	_, err := slow.Fetch(context.Background(), Request{URL: s.URL, Params: Params{Timeout: 50 * time.Millisecond}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Errorf("feed timeout: %v after %v, want a deadline error after 50ms", err, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := slow.Fetch(ctx, Request{URL: s.URL}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}

func TestHostConcurrency(t *testing.T) {
	const limit, requests = 2, 8
	var (
		mu                sync.Mutex
		inFlight, highest int
	)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release() // lets the handlers go when the test fails midway
	handler := func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		inFlight++
		highest = max(highest, inFlight)
		mu.Unlock()
		<-gate
		mu.Lock()
		inFlight--
		mu.Unlock()
	}
	a, b := serve(t, handler), serve(t, handler)
	c := newClient(t, Options{HostConcurrency: limit}, a, b)

	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Fetch(context.Background(), Request{URL: fmt.Sprintf("%s/%d", a.URL, i)}); err != nil {
				t.Errorf("Fetch: %v", err)
			}
		}()
	}
	// The limit is per host: with host a saturated, host b still answers.
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return inFlight >= limit })
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Fetch(context.Background(), Request{URL: b.URL}); err != nil {
			t.Errorf("Fetch of the other host: %v", err)
		}
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return inFlight >= limit+1 })
	// Requests over the limit, if the client let any through, arrive now.
	time.Sleep(50 * time.Millisecond)
	release()
	wg.Wait()

	if highest != limit+1 {
		t.Errorf("%d requests in flight at most, want %d to one host and 1 to the other", highest, limit)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWaitingForHostSlotHonoursContext(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	s := serve(t, func(http.ResponseWriter, *http.Request) {
		started <- struct{}{}
		<-gate
	})
	defer close(gate)
	c := newClient(t, Options{HostConcurrency: 1}, s)

	go func() { _, _ = c.Fetch(context.Background(), Request{URL: s.URL}) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(ctx, Request{URL: s.URL}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Fetch: %v, want a deadline error", err)
	}
}

func TestProxy(t *testing.T) {
	var requested string
	proxy := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.String()
		_, _ = io.WriteString(w, "via proxy")
	})
	proxyURL, _ := url.Parse(proxy.URL)

	// The feed host does not exist: only the proxy is connected to.
	const target = "http://feeds.freshgo.test/atom.xml"
	c := newClient(t, Options{}, proxy)
	res, err := c.Fetch(context.Background(), Request{URL: target, Params: Params{Proxy: proxyURL}})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "via proxy" || requested != target {
		t.Errorf("body %q, proxy was asked for %q", res.Body, requested)
	}

	guarded := newClient(t, Options{})
	_, err = guarded.Fetch(context.Background(), Request{URL: target, Params: Params{Proxy: proxyURL}})
	if !errors.Is(err, ErrForbiddenAddress) {
		t.Errorf("proxy on an internal address: %v, want ErrForbiddenAddress", err)
	}

	// A pause the proxy asked for holds for requests through it, not for direct ones.
	direct := serve(t, func(http.ResponseWriter, *http.Request) {})
	busy := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	busyURL, _ := url.Parse(busy.URL)
	both := newClient(t, Options{}, direct, busy)
	_, _ = both.Fetch(context.Background(), Request{URL: direct.URL, Params: Params{Proxy: busyURL}})
	var ra *RetryAfterError
	if _, err := both.Fetch(context.Background(), Request{URL: direct.URL, Params: Params{Proxy: busyURL}}); !errors.As(err, &ra) {
		t.Errorf("second request through a proxy that answered 429: %v", err)
	}
	if _, err := both.Fetch(context.Background(), Request{URL: direct.URL}); err != nil {
		t.Errorf("direct request during the proxy's pause: %v", err)
	}

	socks4, _ := url.Parse("socks4://" + proxyURL.Host)
	_, err = c.Fetch(context.Background(), Request{URL: target, Params: Params{Proxy: socks4}})
	if !errors.Is(err, ErrUnsupportedProxy) {
		t.Errorf("SOCKS4 proxy: %v, want ErrUnsupportedProxy", err)
	}
}

func TestInsecureSkipsCertificateCheck(t *testing.T) {
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
	s.StartTLS()
	t.Cleanup(s.Close)
	c := newClient(t, Options{}, s)

	if _, err := c.Fetch(context.Background(), Request{URL: s.URL}); err == nil {
		t.Error("a self-signed certificate was accepted")
	}
	res, err := c.Fetch(context.Background(), Request{URL: s.URL, Params: Params{Insecure: true}})
	if err != nil || string(res.Body) != "ok" {
		t.Errorf("Insecure: %v", err)
	}
	// The setting of one feed does not leak into the requests of another.
	if _, err := c.Fetch(context.Background(), Request{URL: s.URL}); err == nil {
		t.Error("a self-signed certificate was accepted after an insecure request")
	}
}

func TestErrorsDoNotRevealPasswords(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	c := newClient(t, Options{}, s)
	target := strings.Replace(s.URL, "http://", "http://alice:hunter2@", 1)
	_, err := c.Fetch(context.Background(), Request{URL: target})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error: %v", err)
	}
}
