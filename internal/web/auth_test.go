package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/store"
)

// referenceData is an installation of FreshRSS with the users alice, who is
// its default user, and bob; their web passwords are "<name>-web-password".
const referenceData = "../../testdata/reference/sqlite/data"

// imported runs a test against the interface over the imported reference
// installation.
func imported(t *testing.T, o Options, test func(t *testing.T, s *site)) {
	t.Helper()
	eachEngine(t, o, func(t *testing.T, s *site) {
		if _, err := importer.Run(context.Background(), s.db, importer.Options{DataDir: referenceData}); err != nil {
			t.Fatalf("import: %v", err)
		}
		test(t, s)
	})
}

func (s *site) login(name, password string, more url.Values) answer {
	s.t.Helper()
	form := url.Values{"username": {name}, "password": {password}}
	for k, v := range more {
		form[k] = v
	}
	return s.post("/login", form)
}

// setting changes one setting of a user.
func (s *site) setting(user, key string, value any) {
	s.t.Helper()
	ctx := context.Background()
	u, err := s.db.UserByName(ctx, user)
	if err != nil {
		s.t.Fatal(err)
	}
	err = s.db.UpdateUserSettings(ctx, u.ID, func(settings map[string]json.RawMessage) error {
		raw, err := json.Marshal(value)
		settings[key] = raw
		return err
	})
	if err != nil {
		s.t.Fatal(err)
	}
}

// system changes the settings of the installation.
func (s *site) system(change func(*store.System)) {
	s.t.Helper()
	ctx := context.Background()
	system, err := s.db.System(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	change(&system)
	if err := s.db.SetSystem(ctx, system); err != nil {
		s.t.Fatal(err)
	}
}

// clock makes the interface live at a time the test moves.
func (s *site) clock() *time.Time {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.h.now = func() time.Time { return now }
	return &now
}

// whoami adds a page that tells who the interface takes the visitor for,
// open to those the level lets in.
func (s *site) whoami(level int) {
	s.h.mux.HandleFunc("/test/whoami", s.h.protect(level, func(w http.ResponseWriter, r *http.Request) {
		who := state(r).who
		switch {
		case who == nil:
			_, _ = w.Write([]byte("nobody"))
		case who.anonymous:
			_, _ = w.Write([]byte("guest of " + who.user.Name))
		case who.admin:
			_, _ = w.Write([]byte(who.user.Name + ", administrator"))
		default:
			_, _ = w.Write([]byte(who.user.Name))
		}
	}))
}

// A user of the imported installation logs in with the password they had.
func TestLogin(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.whoami(members)
		if a := s.get("/"); a.status != http.StatusSeeOther || a.header.Get("Location") != "/login?next=%2F" {
			t.Fatalf("GET / without a login: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		form := s.get("/login?next=%2Ftest%2Fwhoami%3Fa%3D1")
		for _, want := range []string{
			`<form method="post" action="/login"`, `name="next" value="/test/whoami?a=1"`,
			`<label for="username">User name</label>`, `name="username" value="" required autocomplete="username"`,
			`<label for="password">Password</label>`, `type="password" id="password" name="password" required autocomplete="current-password"`,
			`<label for="remember">Keep me signed in on this device</label>`, `<a href="/login" aria-current="page">Sign in</a>`,
		} {
			if form.status != http.StatusOK || !strings.Contains(form.body, want) {
				t.Errorf("GET /login: status %d, no %q in\n%s", form.status, want, form.body)
			}
		}

		a := s.login("alice", "alice-web-password", url.Values{"next": {"/test/whoami?a=1"}})
		if a.status != http.StatusSeeOther || a.header.Get("Location") != "/test/whoami?a=1" {
			t.Fatalf("login: status %d, Location %q, body\n%s", a.status, a.header.Get("Location"), a.body)
		}
		cookie := a.header.Get("Set-Cookie")
		for _, want := range []string{sessionCookie + "=", "; Path=/", "; HttpOnly", "; SameSite=Lax"} {
			if !strings.Contains(cookie, want) {
				t.Errorf("Set-Cookie = %q, no %q", cookie, want)
			}
		}
		// Not to be remembered: the cookie goes with the browser. And over
		// plain HTTP it cannot be marked for HTTPS only.
		if strings.Contains(cookie, "Max-Age") || strings.Contains(cookie, "Expires") || strings.Contains(cookie, "Secure") {
			t.Errorf("Set-Cookie = %q, want a cookie for the browser session, usable over HTTP", cookie)
		}
		// The default user of the installation is an administrator.
		if a := s.get("/test/whoami"); a.body != "alice, administrator" {
			t.Errorf("after the login the visitor is %q", a.body)
		}
		page := s.get("/about")
		if !strings.Contains(page.body, `<span class="account-name">alice</span>`) || !strings.Contains(page.body, `action="/logout"`) ||
			strings.Contains(page.body, `href="/login"`) {
			t.Errorf("a page after the login does not show the user and the way out:\n%s", page.body)
		}
		// Somebody who is logged in has no use for the login page.
		if a := s.get("/login?next=/about"); a.status != http.StatusSeeOther || a.header.Get("Location") != "/about" {
			t.Errorf("GET /login when logged in: status %d, Location %q", a.status, a.header.Get("Location"))
		}

		// The database knows the login by a digest of what the browser holds.
		token := s.cookies[sessionCookie].Value
		if _, err := s.db.Session(context.Background(), token, 0); err == nil {
			t.Error("the secret of the login is stored as it is")
		}
		if _, err := s.db.Session(context.Background(), tokenHash(token), 0); err != nil {
			t.Errorf("the login is not stored under its digest: %v", err)
		}

		out := s.post("/logout", nil)
		if out.status != http.StatusSeeOther || !strings.Contains(out.header.Get("Set-Cookie"), "Max-Age=0") {
			t.Errorf("logout: status %d, Set-Cookie %q", out.status, out.header.Get("Set-Cookie"))
		}
		if _, err := s.db.Session(context.Background(), tokenHash(token), 0); err == nil {
			t.Error("the login outlived the logout")
		}
		// A copy of the cookie is of no use after the logout.
		r := httptest.NewRequest(http.MethodGet, "/test/whoami", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		if a := s.send(r, nil); a.status != http.StatusSeeOther {
			t.Errorf("a request with the cookie of an ended login: status %d, body %q", a.status, a.body)
		}

		// bob is a user like any other.
		if a := s.login("bob", "bob-web-password", nil); a.status != http.StatusSeeOther || a.header.Get("Location") != "/" {
			t.Errorf("login of bob: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if a := s.get("/test/whoami"); a.body != "bob" {
			t.Errorf("after his login bob is %q", a.body)
		}
		s.setting("bob", "is_admin", true)
		if a := s.get("/test/whoami"); a.body != "bob, administrator" {
			t.Errorf("bob made an administrator is %q", a.body)
		}
		// Kept out from now on, in the middle of his login.
		s.setting("bob", "enabled", false)
		if a := s.get("/test/whoami"); a.status != http.StatusSeeOther {
			t.Errorf("a disabled user is still let in: status %d, body %q", a.status, a.body)
		}
	})
}

// Whatever is wrong with a login, the answer is the same.
func TestLoginRefused(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("bob", "enabled", false)
		ctx := context.Background()
		if err := s.db.CreateUser(ctx, &store.User{Name: "carol"}); err != nil {
			t.Fatal(err)
		}
		var bodies []string
		for _, attempt := range [][2]string{
			{"alice", "bob-web-password"}, {"alice", ""}, {"nobody", "alice-web-password"},
			{"bob", "bob-web-password"}, {"carol", ""}, {"carol", "anything"}, {"not a name", "x"}, {"", ""},
		} {
			a := s.login(attempt[0], attempt[1], nil)
			if a.status != http.StatusUnauthorized || a.header.Get("Set-Cookie") != "" ||
				!strings.Contains(a.body, `<p class="alert" role="alert">Wrong user name or password.</p>`) {
				t.Errorf("login as %q with %q: status %d, cookie %q, body\n%s", attempt[0], attempt[1], a.status, a.header.Get("Set-Cookie"), a.body)
			}
			// The name typed is kept in the form, the password is not.
			if !strings.Contains(a.body, `name="username" value="`+attempt[0]+`"`) || strings.Contains(a.body, "web-password") {
				t.Errorf("login as %q: the form does not keep the name, or keeps the password:\n%s", attempt[0], a.body)
			}
			bodies = append(bodies, strings.ReplaceAll(a.body, `name="username" value="`+attempt[0]+`"`, ""))
		}
		for i, body := range bodies {
			// With no name typed the cursor starts in the name field,
			// otherwise in the password field; nothing else may differ.
			body = strings.ReplaceAll(body, " autofocus", "")
			if want := strings.ReplaceAll(bodies[0], " autofocus", ""); body != want {
				t.Errorf("refusal %d differs from the first:\n%s\nfirst:\n%s", i, body, want)
			}
		}
		if n := len(s.cookies); n != 0 {
			t.Errorf("%d cookies after refused logins", n)
		}
	})
}

func TestLoginTarget(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		for next, want := range map[string]string{
			"":                         "/",
			"/about":                   "/about",
			"/feeds/3?state=unread#e5": "/feeds/3?state=unread",
			"//evil.example/":          "/",
			"https://evil.example/":    "/",
			"/\\evil.example":          "/",
			"about":                    "/",
			"javascript:alert(1)":      "/",
		} {
			a := s.login("alice", "alice-web-password", url.Values{"next": {next}})
			if a.status != http.StatusSeeOther || a.header.Get("Location") != want {
				t.Errorf("login with next=%q: status %d, Location %q; want %q", next, a.status, a.header.Get("Location"), want)
			}
			s.post("/logout", nil)
		}
	})
}

// A login lasts a day without use, or as long as the installation
// remembers a user who asked for it; either is renewed by use and survives
// a restart of the server.
func TestSessionLife(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.whoami(members)
		now := s.clock()
		loggedIn := func() bool {
			s.t.Helper()
			return s.get("/test/whoami").body == "alice, administrator"
		}

		s.login("alice", "alice-web-password", nil)
		*now = now.Add(23 * time.Hour)
		if !loggedIn() {
			t.Fatal("a login was gone within a day")
		}
		// Used a moment ago, so the day starts anew.
		*now = now.Add(23 * time.Hour)
		if !loggedIn() {
			t.Error("a login used within the day was not renewed")
		}
		*now = now.Add(25 * time.Hour)
		if loggedIn() {
			t.Error("a login left alone for more than a day still works")
		}

		a := s.login("alice", "alice-web-password", url.Values{"remember": {"1"}})
		if cookie := a.header.Get("Set-Cookie"); !strings.Contains(cookie, "Max-Age=7776000") {
			t.Errorf("Set-Cookie of a login to be remembered = %q, want 90 days", cookie)
		}
		*now = now.Add(80 * 24 * time.Hour)
		if !loggedIn() {
			t.Fatal("a remembered login was gone within 90 days")
		}
		// Another server over the same database, as after a restart.
		restarted, err := New(Options{DB: s.db, Log: s.h.log})
		if err != nil {
			t.Fatal(err)
		}
		restarted.now = s.h.now
		old := s.h
		s.h = restarted
		s.whoami(members)
		*now = now.Add(80 * 24 * time.Hour)
		if !loggedIn() {
			t.Error("a remembered login did not survive a restart of the server, or was not renewed by use")
		}
		*now = now.Add(91 * 24 * time.Hour)
		if loggedIn() {
			t.Error("a remembered login left alone for 91 days still works")
		}
		s.h = old

		// The administrator decides how long users are remembered.
		s.system(func(system *store.System) { system.Limits.CookieDuration = 3600 })
		a = s.login("alice", "alice-web-password", url.Values{"remember": {"1"}})
		if cookie := a.header.Get("Set-Cookie"); !strings.Contains(cookie, "Max-Age=3600") {
			t.Errorf("Set-Cookie with a duration of an hour = %q", cookie)
		}
	})
}

// After a run of failures further attempts have to wait, the right password
// included.
func TestLoginGuard(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		now := s.clock()
		for i := range freeFailures {
			if a := s.login("alice", "wrong", nil); a.status != http.StatusUnauthorized {
				t.Fatalf("failure %d: status %d", i+1, a.status)
			}
		}
		a := s.login("alice", "alice-web-password", nil)
		if a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "31" ||
			!strings.Contains(a.body, "Too many failed attempts. Try again in 1 minute.") || a.header.Get("Set-Cookie") != "" {
			t.Errorf("login after %d failures: status %d, Retry-After %q, body\n%s", freeFailures, a.status, a.header.Get("Retry-After"), a.body)
		}
		// Another name, or the same from elsewhere, is not held back.
		if a := s.login("bob", "bob-web-password", nil); a.status != http.StatusSeeOther {
			t.Errorf("login of another user meanwhile: status %d", a.status)
		}
		s.post("/logout", nil)
		elsewhere := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=alice-web-password"))
		elsewhere.RemoteAddr = "198.51.100.7:4000"
		if a := s.send(elsewhere, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); a.status != http.StatusSeeOther {
			t.Errorf("login of the same user from another address meanwhile: status %d", a.status)
		}
		s.post("/logout", nil)

		*now = now.Add(31 * time.Second)
		// One more failure doubles the pause.
		s.login("alice", "wrong", nil)
		if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusTooManyRequests || a.header.Get("Retry-After") != "61" {
			t.Errorf("login after one more failure: status %d, Retry-After %q", a.status, a.header.Get("Retry-After"))
		}
		*now = now.Add(61 * time.Second)
		if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusSeeOther {
			t.Errorf("login after the pause: status %d", a.status)
		}
		s.post("/logout", nil)
		// A login that worked wipes the slate.
		for range freeFailures - 1 {
			s.login("alice", "wrong", nil)
		}
		if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusSeeOther {
			t.Errorf("login after fewer failures than the limit: status %d", a.status)
		}
	})
}

func TestGuardPauses(t *testing.T) {
	var g guard
	now := time.Unix(1_000_000, 0)
	for i := 1; i <= 20; i++ {
		if wait := g.attempt("k", now); wait != 0 {
			t.Fatalf("attempt %d, made after the pause, has to wait %s", i, wait)
		}
		want := time.Duration(0)
		if i >= freeFailures {
			want = min(firstPause<<(i-freeFailures), longestPause)
		}
		// While the pause lasts, asking again is no attempt.
		for range 3 {
			if want == 0 {
				break
			}
			if got := g.attempt("k", now); got != want {
				t.Errorf("after %d failures the pause is %s, want %s", i, got, want)
			}
		}
		if got := g.failures["k"].count; got != i {
			t.Errorf("after %d attempts the guard has counted %d", i, got)
		}
		now = now.Add(want)
	}
	// Left alone for longer than the longest pause, a name starts anew.
	now = now.Add(longestPause + time.Second)
	if g.attempt("k", now); g.failures["k"].count != 1 {
		t.Errorf("the first failure after a long quiet is counted as number %d", g.failures["k"].count)
	}
	// What the guard remembers is bounded.
	for i := range knownFailures + 10 {
		g.attempt(strconv.Itoa(i), now)
	}
	if len(g.failures) > knownFailures {
		t.Errorf("the guard remembers %d names", len(g.failures))
	}
}

// Attempts made at once are counted like attempts made in a row: no more
// than the free ones get a look at the password.
func TestGuardCountsParallelAttempts(t *testing.T) {
	var (
		g      guard
		passed atomic.Int32
		wg     sync.WaitGroup
	)
	now := time.Unix(1_000_000, 0)
	for range 200 {
		wg.Go(func() {
			if g.attempt("k", now) == 0 {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := passed.Load(); n != freeFailures {
		t.Errorf("%d of 200 attempts made at once were let through, want %d", n, freeFailures)
	}
}

// A name without a password to guess is refused as slowly as a wrong
// password, with a hash as costly as those of the installation.
func TestRefusalsTakeTheSameWork(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		alice, err := s.db.UserByName(ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		cost, err := bcrypt.Cost([]byte(readPreferences(alice).PasswordHash))
		if err != nil || cost == bcrypt.DefaultCost {
			t.Fatalf("the reference hash has cost %d, %v; the test needs one that is not Go's default", cost, err)
		}
		system, err := s.db.System(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"nobody", "not a name"} {
			if u, err := s.h.checkPassword(ctx, system, name, "x"); u != nil || err != nil {
				t.Fatalf("checkPassword(%q) = %v, %v", name, u, err)
			}
		}
		if len(s.h.decoys.byCost) != 1 || s.h.decoys.byCost[cost] == nil {
			t.Errorf("unknown names were checked against decoys of the costs %v, want only %d", keys(s.h.decoys.byCost), cost)
		}
		if got, _ := bcrypt.Cost(s.h.decoys.withCost(cost)); got != cost {
			t.Errorf("the decoy for cost %d has cost %d", cost, got)
		}
	})
}

func keys(m map[int][]byte) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	return out
}

// What a login request may carry is bounded, and so is what is kept of it.
func TestLoginInputIsBounded(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		long := strings.Repeat("a", 1<<20)
		for range freeFailures + 2 {
			if a := s.login(long, "x", nil); a.status != http.StatusUnauthorized {
				t.Fatalf("login with a name of 1 MiB: status %d", a.status)
			}
			if a := s.login("no such name!", "x", nil); a.status != http.StatusUnauthorized {
				t.Fatalf("login with a name no user can have: status %d", a.status)
			}
		}
		if n := len(s.h.guard.failures); n != 0 {
			t.Errorf("the guard remembers %d names nobody can have", n)
		}
		// A body beyond the limit is not read to its end: the fields
		// after the limit do not arrive.
		form := url.Values{"padding": {long}, "username": {"alice"}, "password": {"alice-web-password"}}
		if a := s.post("/login", form); a.status != http.StatusUnauthorized || len(s.cookies) != 0 {
			t.Errorf("login in an oversized form: status %d, %d cookies", a.status, len(s.cookies))
		}
	})
}

// Behind a reverse proxy attempts are told apart by the address the proxy
// took them from; without one, a header proves nothing.
func TestLoginGuardBehindProxy(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
	imported(t, Options{TrustedProxies: proxies}, func(t *testing.T, s *site) {
		s.clock()
		attempt := func(remote, forwarded, password string) int {
			s.t.Helper()
			form := url.Values{"username": {"alice"}, "password": {password}}
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			r.RemoteAddr = remote
			header := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
			if forwarded != "" {
				header["X-Forwarded-For"] = forwarded
			}
			status := s.send(r, header).status
			s.cookies = nil
			return status
		}
		// Somebody guesses through the proxy; the header starts with what
		// they sent themselves, the proxy added where they really are.
		for range freeFailures {
			attempt("10.1.0.1:4000", "203.0.113.9, 198.51.100.7", "wrong")
		}
		if got := attempt("10.1.0.1:4000", "198.51.100.7", "alice-web-password"); got != http.StatusTooManyRequests {
			t.Errorf("the guesser, after the failures: status %d, want 429", got)
		}
		if got := attempt("10.1.0.1:4000", "203.0.113.9", "alice-web-password"); got != http.StatusSeeOther {
			t.Errorf("alice from her own address, through the same proxy: status %d, want to be let in", got)
		}
		// Directly, the address is that of the connection whatever a
		// header claims.
		for i := range freeFailures {
			attempt("192.0.2.50:4000", "203.0.113."+strconv.Itoa(i), "wrong")
		}
		if got := attempt("192.0.2.50:4000", "203.0.113.200", "alice-web-password"); got != http.StatusTooManyRequests {
			t.Errorf("a guesser who claims another address with every attempt: status %d, want 429", got)
		}
	})
}

// Requests that change something are taken from the pages of the interface
// only.
func TestCrossSiteRequests(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.whoami(members)
		s.login("alice", "alice-web-password", nil)
		for name, header := range map[string]map[string]string{
			"marked cross-site by the browser": {"Sec-Fetch-Site": "cross-site"},
			"from a sibling site":              {"Sec-Fetch-Site": "same-site"},
			"with the origin of another site":  {"Origin": "https://evil.example"},
		} {
			a := s.do(http.MethodPost, "/logout", header)
			if a.status != http.StatusForbidden || !strings.Contains(a.body, "<h1>Not allowed</h1>") {
				t.Errorf("POST /logout %s: status %d, body\n%s", name, a.status, a.body)
			}
			if s.get("/test/whoami").body != "alice, administrator" {
				t.Fatalf("POST /logout %s logged the user out", name)
			}
		}
		// Following a link from another site is fine: it changes nothing.
		if a := s.do(http.MethodGet, "/about", map[string]string{"Sec-Fetch-Site": "cross-site"}); a.status != http.StatusOK {
			t.Errorf("GET /about from another site: status %d", a.status)
		}
		for name, header := range map[string]map[string]string{
			"from the same origin":           {"Sec-Fetch-Site": "same-origin"},
			"typed by the user":              {"Sec-Fetch-Site": "none"},
			"with the origin of the server":  {"Origin": "http://example.com"},
			"from a tool that is no browser": nil,
		} {
			s.login("alice", "alice-web-password", nil)
			if a := s.do(http.MethodPost, "/logout", header); a.status != http.StatusSeeOther {
				t.Errorf("POST /logout %s: status %d", name, a.status)
			}
		}
	})
}

// Behind a reverse proxy the browser sees the public address: cookies are
// for its path and for HTTPS, and requests from its origin are our own.
func TestBehindProxy(t *testing.T) {
	imported(t, Options{BaseURL: "https://rss.example.org/reader"}, func(t *testing.T, s *site) {
		form := url.Values{"username": {"alice"}, "password": {"alice-web-password"}, "next": {"/about"}}
		r := httptest.NewRequest(http.MethodPost, "http://10.0.0.5:8080/login", strings.NewReader(form.Encode()))
		a := s.send(r, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://rss.example.org"})
		if a.status != http.StatusSeeOther || a.header.Get("Location") != "/reader/about" {
			t.Fatalf("login through the proxy: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		cookie := a.header.Get("Set-Cookie")
		if !strings.Contains(cookie, "; Path=/reader/") || !strings.Contains(cookie, "; Secure") {
			t.Errorf("Set-Cookie = %q, want the public path and HTTPS only", cookie)
		}
		if a := s.do(http.MethodPost, "http://10.0.0.5:8080/logout", map[string]string{"Origin": "https://evil.example"}); a.status != http.StatusForbidden {
			t.Errorf("POST /logout from another origin through the proxy: status %d", a.status)
		}
		// The reading screen spells its links and forms with the public path.
		a = s.get("/")
		if a.status != http.StatusOK || !strings.Contains(a.body, `href="/reader/starred"`) ||
			!strings.Contains(a.body, `action="/reader/read-all"`) || strings.Contains(a.body, `href="/starred"`) {
			t.Errorf("GET / when logged in: status %d, body\n%s", a.status, a.body)
		}
	})
}

// The language and the colours of a page are the user's own.
func TestUserPreferences(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "language", "ru")
		s.setting("alice", "darkMode", "no")
		english := map[string]string{"Accept-Language": "en"}
		if a := s.do(http.MethodGet, "/about", english); !strings.Contains(a.body, `<html lang="en" data-theme="auto" data-look="classic">`) {
			t.Errorf("a page for a visitor:\n%.200s", a.body)
		}
		s.login("alice", "alice-web-password", nil)
		if a := s.do(http.MethodGet, "/about", english); !strings.Contains(a.body, `<html lang="ru" data-theme="light" data-look="classic">`) || !strings.Contains(a.body, ">Выйти</button>") {
			t.Errorf("a page for alice, who reads Russian on a light background:\n%s", a.body)
		}
		s.setting("alice", "darkMode", "dark")
		s.setting("alice", "look", "modern")
		// A language freshgo does not speak leaves the choice to the browser.
		s.setting("alice", "language", "zh-CN")
		if a := s.do(http.MethodGet, "/about", english); !strings.Contains(a.body, `<html lang="en" data-theme="dark" data-look="modern">`) {
			t.Errorf("a page for alice with a dark background:\n%.200s", a.body)
		}
		// A look there is not is the usual one.
		s.setting("alice", "look", "origine")
		if a := s.do(http.MethodGet, "/about", english); !strings.Contains(a.body, `data-theme="dark" data-look="classic">`) {
			t.Errorf("a page for alice with a look there is not:\n%.200s", a.body)
		}
	})
}

func (s *site) from(addr string, header map[string]string) answer {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/test/whoami", nil)
	r.RemoteAddr = addr
	return s.send(r, header)
}

// Users told apart by the reverse proxy: its word is taken, nobody else's.
func TestProxyAuthentication(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("10.1.0.0/16")}
	imported(t, Options{TrustedProxies: proxies}, func(t *testing.T, s *site) {
		s.whoami(members)
		s.system(func(system *store.System) { system.AuthType = store.AuthHTTP })

		for _, header := range []string{"Remote-User", "X-WebAuth-User"} {
			for _, proxy := range []string{"127.0.0.1:5000", "10.1.2.3:5000", "[::ffff:127.0.0.1]:5000"} {
				if a := s.from(proxy, map[string]string{header: "bob"}); a.body != "bob" {
					t.Errorf("%s: bob from the proxy at %s: status %d, body %q", header, proxy, a.status, a.body)
				}
			}
		}
		// Anybody can send the header; only the proxy is believed. And
		// there is no login page to send a visitor to.
		for _, addr := range []string{"192.0.2.1:5000", "10.2.0.1:5000", "[::1]:5000"} {
			if a := s.from(addr, map[string]string{"Remote-User": "bob"}); a.status != http.StatusForbidden {
				t.Errorf("Remote-User from %s: status %d, body %q", addr, a.status, a.body)
			}
		}
		if a := s.from("127.0.0.1:5000", nil); a.status != http.StatusForbidden {
			t.Errorf("the proxy naming nobody: status %d, body %q", a.status, a.body)
		}
		r := httptest.NewRequest(http.MethodGet, "/test/whoami", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Add("Remote-User", "bob")
		r.Header.Add("X-WebAuth-User", "alice")
		if a := s.send(r, nil); a.status != http.StatusForbidden {
			t.Errorf("the proxy naming two users: status %d, body %q", a.status, a.body)
		}
		// A password logs nobody in here, and the pages do not offer it.
		if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusNotFound || len(s.cookies) != 0 {
			t.Errorf("a login by password: status %d, %d cookies", a.status, len(s.cookies))
		}
		r = httptest.NewRequest(http.MethodGet, "/about", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		page := s.send(r, map[string]string{"Remote-User": "bob"})
		if !strings.Contains(page.body, `<span class="account-name">bob</span>`) || strings.Contains(page.body, "/logout") || strings.Contains(page.body, "/login") {
			t.Errorf("a page for a user the proxy vouches for offers to log in or out:\n%s", page.body)
		}
		s.setting("bob", "enabled", false)
		if a := s.from("127.0.0.1:5000", map[string]string{"Remote-User": "bob"}); a.status != http.StatusForbidden {
			t.Errorf("a disabled user the proxy vouches for: status %d", a.status)
		}

		// A name nobody has yet becomes a user, if it can be one.
		if a := s.from("127.0.0.1:5000", map[string]string{"Remote-User": "carol"}); a.body != "carol" {
			t.Errorf("a new user from the proxy: status %d, body %q", a.status, a.body)
		}
		if u, err := s.db.UserByName(context.Background(), "carol"); err != nil || readPreferences(u).PasswordHash != "" {
			t.Errorf("the user created on the word of the proxy: %+v, %v", u, err)
		}
		if a := s.from("127.0.0.1:5000", map[string]string{"Remote-User": "not/a name"}); a.status != http.StatusForbidden {
			t.Errorf("a name that cannot be a user name: status %d", a.status)
		}
		s.system(func(system *store.System) { system.HTTPAuthAutoRegister = false })
		if a := s.from("127.0.0.1:5000", map[string]string{"Remote-User": "dave"}); a.status != http.StatusForbidden {
			t.Errorf("a new user with registration by the proxy off: status %d", a.status)
		}
		if _, err := s.db.UserByName(context.Background(), "dave"); err == nil {
			t.Error("a user was created with registration by the proxy off")
		}
	})
}

// Without authentication everybody is the default user.
func TestNoAuthentication(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.whoami(members)
		s.system(func(system *store.System) { system.AuthType = store.AuthNone })
		if a := s.get("/test/whoami"); a.body != "alice, administrator" {
			t.Errorf("a visitor of an installation without authentication is %q (status %d)", a.body, a.status)
		}
		if a := s.get("/login"); a.status != http.StatusSeeOther || a.header.Get("Location") != "/" {
			t.Errorf("GET /login: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		s.system(func(system *store.System) { system.DefaultUser = "_" })
		if a := s.get("/test/whoami"); a.status != http.StatusForbidden {
			t.Errorf("without a default user: status %d, body %q", a.status, a.body)
		}
	})
}

// With anonymous reading a visitor reads what the default user reads and
// changes nothing.
func TestAnonymousReading(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "language", "ru")
		s.system(func(system *store.System) { system.AllowAnonymous = true })
		s.h.mux.HandleFunc("/test/read", s.h.protect(readers, func(w http.ResponseWriter, r *http.Request) {
			who := state(r).who
			_, _ = w.Write([]byte(who.user.Name))
			if who.anonymous {
				_, _ = w.Write([]byte(", read only"))
			}
			if who.admin {
				_, _ = w.Write([]byte(", administrator"))
			}
		}))
		s.whoami(members)

		if a := s.get("/test/read"); a.body != "alice, read only" {
			t.Errorf("a page for readers, asked for by a visitor: status %d, body %q", a.status, a.body)
		}
		// What takes a user sends the visitor to log in, or refuses.
		if a := s.get("/test/whoami"); a.status != http.StatusSeeOther || a.header.Get("Location") != "/login?next=%2Ftest%2Fwhoami" {
			t.Errorf("a page for users, asked for by a visitor: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if a := s.post("/test/whoami", nil); a.status != http.StatusForbidden {
			t.Errorf("a change asked for by a visitor: status %d, body %q", a.status, a.body)
		}
		// The visitor is not shown as alice, reads in a language of their
		// own, and is offered to log in.
		page := s.do(http.MethodGet, "/about", map[string]string{"Accept-Language": "en"})
		if strings.Contains(page.body, "account-name") || !strings.Contains(page.body, `<html lang="en"`) || !strings.Contains(page.body, `href="/login"`) {
			t.Errorf("a page for a visitor:\n%s", page.body)
		}

		s.login("bob", "bob-web-password", nil)
		if a := s.get("/test/read"); a.body != "bob" {
			t.Errorf("a page for readers, asked for by bob: %q", a.body)
		}
		s.post("/logout", nil)
		s.setting("alice", "enabled", false)
		if a := s.get("/test/read"); a.status != http.StatusSeeOther {
			t.Errorf("anonymous reading of a disabled user: status %d, body %q", a.status, a.body)
		}
	})
}
