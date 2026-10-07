package web

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"

	"github.com/juev/freshgo/internal/store"
)

const (
	oidcTestClient = "freshgo-client"
	oidcTestSecret = "the secret of the client"
)

// provider is an OpenID Connect provider of a test. It signs in whoever the
// test says is sitting at the browser.
type provider struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	// user is the preferred_username of the next sign-in.
	user string
	// spoil changes the claims of the next ID token before it is signed;
	// signWith signs it with another key than the provider publishes.
	spoil    func(claims map[string]any)
	signWith *rsa.PrivateKey
	// codes are the sign-ins the provider has sent the browser back with.
	codes map[string]providerCode
}

type providerCode struct {
	nonce, challenge, user string
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{t: t, key: key, codes: map[string]providerCode{}}
	discovery := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{PublicKey: key.Public(), KeyID: "key", Algorithm: oidc.RS256}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", p.token)
	mux.Handle("/", discovery)
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	discovery.SetIssuer(p.server.URL)
	return p
}

// token trades a code for an ID token, as the provider does when the
// client proves it is the one that started the sign-in.
func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	// In the header the two are form-encoded (RFC 6749, 2.3.1).
	id, secret, _ := r.BasicAuth()
	id, _ = url.QueryUnescape(id)
	secret, _ = url.QueryUnescape(secret)
	if id == "" {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	code, known := p.codes[r.PostFormValue("code")]
	delete(p.codes, r.PostFormValue("code"))
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if id != oidcTestClient || secret != oidcTestSecret || !known || r.PostFormValue("grant_type") != "authorization_code" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
		p.t.Errorf("the token request is not that of the client: client %q, code known %v, form %v", id, known, r.PostForm)
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	claims := map[string]any{
		"iss": p.server.URL, "aud": oidcTestClient, "sub": "subject of " + code.user,
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": code.nonce, "preferred_username": code.user,
	}
	if p.spoil != nil {
		p.spoil(claims)
	}
	raw, _ := json.Marshal(claims)
	key := p.key
	if p.signWith != nil {
		key = p.signWith
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access", "token_type": "Bearer",
		"id_token": oidctest.SignIDToken(key, "key", oidc.RS256, string(raw)),
	})
}

// signIn takes the browser of the site to the provider and back, the way
// the pages lead, and returns what the site says to the browser that comes
// back. change, when given, alters the address the provider sends back to.
func (p *provider) signIn(s *site, next string, change func(back url.Values)) answer {
	s.t.Helper()
	start := s.get("/oidc/login?" + url.Values{"next": {next}}.Encode())
	if start.status != http.StatusSeeOther {
		s.t.Fatalf("GET /oidc/login: status %d\n%s", start.status, start.body)
	}
	at, err := url.Parse(start.header.Get("Location"))
	if err != nil || !strings.HasPrefix(at.String(), p.server.URL+"/") {
		s.t.Fatalf("the sign-in starts at %q, want the provider", start.header.Get("Location"))
	}
	q := at.Query()
	if q.Get("client_id") != oidcTestClient || q.Get("response_type") != "code" || q.Get("scope") != "openid profile" ||
		q.Get("redirect_uri") != "https://reader.example/oidc/callback" || q.Get("code_challenge_method") != "S256" ||
		len(q.Get("state")) < 32 || len(q.Get("nonce")) < 32 {
		s.t.Fatalf("what the provider is asked: %v", q)
	}
	code := fmt.Sprintf("code-%d", len(p.codes)+1)
	p.codes[code] = providerCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), user: p.user}
	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	if change != nil {
		change(back)
	}
	return s.get("/oidc/callback?" + back.Encode())
}

// withProvider runs a test on the reference installation with a provider
// to sign in through.
func withProvider(t *testing.T, test func(t *testing.T, s *site, p *provider)) {
	t.Helper()
	p := newProvider(t)
	imported(t, Options{BaseURL: "https://reader.example", OIDCClientSecret: oidcTestSecret}, func(t *testing.T, s *site) {
		s.system(func(system *store.System) {
			system.OIDC = store.OIDC{Issuer: p.server.URL, ClientID: oidcTestClient}
			system.HTTPAuthAutoRegister = false
		})
		p.user, p.spoil, p.signWith = "alice", nil, nil
		s.whoami(members)
		test(t, s, p)
	})
}

func (s *site) signedInAs() string {
	s.t.Helper()
	a := s.get("/test/whoami")
	if a.status != http.StatusOK {
		return ""
	}
	return a.body
}

// A visitor signs in through the provider as the user it names.
func TestOIDCSignIn(t *testing.T) {
	withProvider(t, func(t *testing.T, s *site, p *provider) {
		host := strings.TrimPrefix(p.server.URL, "http://")
		page := s.shown("/login?next=%2Fsubscriptions")
		if !strings.Contains(page, `<a href="/oidc/login?next=%2Fsubscriptions">Sign in with `+host+`</a>`) || !strings.Contains(page, `name="password"`) {
			t.Errorf("the login page does not offer the provider next to the password:\n%s", page)
		}

		a := p.signIn(s, "/subscriptions", nil)
		if a.status != http.StatusSeeOther || a.header.Get("Location") != "/subscriptions" {
			t.Fatalf("back from the provider: status %d, Location %q\n%s", a.status, a.header.Get("Location"), a.body)
		}
		if who := s.signedInAs(); who != "alice, administrator" {
			t.Errorf("signed in as %q, want alice", who)
		}
		if _, kept := s.cookies[oidcCookie]; kept {
			t.Error("the browser still holds the sign-in it brought back")
		}
		if c := s.cookies[sessionCookie]; c == nil || !c.HttpOnly || !c.Secure || c.MaxAge != 0 {
			t.Errorf("the cookie of the login = %+v", c)
		}

		// Another user at the browser takes over.
		p.user = "bob"
		if a := p.signIn(s, "https://elsewhere.example/", nil); a.status != http.StatusSeeOther || a.header.Get("Location") != "/" {
			t.Errorf("a sign-in that asks to land on another site: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if who := s.signedInAs(); who != "bob" {
			t.Errorf("signed in as %q, want bob", who)
		}
	})
}

// What does not come from the sign-in this browser started, or names
// nobody who may log in, logs nobody in.
func TestOIDCRefusals(t *testing.T) {
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	withProvider(t, func(t *testing.T, s *site, p *provider) {
		refused := func(what string, status int, a answer) {
			t.Helper()
			if a.status != status || !strings.Contains(a.body, "Signing in through the provider did not work.") || !strings.Contains(a.body, `name="password"`) {
				t.Errorf("%s: status %d, want %d and the login page with the message\n%s", what, a.status, status, a.body)
			}
			if who := s.signedInAs(); who != "" {
				t.Errorf("%s: signed in as %q", what, who)
			}
			if _, kept := s.cookies[oidcCookie]; kept {
				t.Errorf("%s: the browser still holds the sign-in", what)
			}
			p.user, p.spoil, p.signWith = "alice", nil, nil
			clear(p.codes)
		}

		refused("another state", http.StatusBadRequest, p.signIn(s, "/", func(back url.Values) { back.Set("state", "x") }))
		refused("no state", http.StatusBadRequest, p.signIn(s, "/", func(back url.Values) { back.Del("state") }))
		refused("an answer without a sign-in", http.StatusBadRequest, s.get("/oidc/callback?code=code-1&state="))
		refused("the provider says no", http.StatusForbidden, p.signIn(s, "/", func(back url.Values) { back.Set("error", "access_denied") }))

		p.spoil = func(claims map[string]any) { claims["nonce"] = "of another sign-in" }
		refused("another nonce", http.StatusBadGateway, p.signIn(s, "/", nil))
		p.spoil = func(claims map[string]any) { claims["aud"] = "another-client" }
		refused("a token for another client", http.StatusBadGateway, p.signIn(s, "/", nil))
		p.spoil = func(claims map[string]any) { claims["iss"] = "https://elsewhere.example" }
		refused("a token of another issuer", http.StatusBadGateway, p.signIn(s, "/", nil))
		p.spoil = func(claims map[string]any) { claims["exp"] = time.Now().Add(-time.Hour).Unix() }
		refused("a token that has expired", http.StatusBadGateway, p.signIn(s, "/", nil))
		p.signWith = other
		refused("a token signed by somebody else", http.StatusBadGateway, p.signIn(s, "/", nil))

		// An answer is taken once.
		first := p.signIn(s, "/", nil)
		if first.status != http.StatusSeeOther {
			t.Fatalf("a sign-in: status %d", first.status)
		}
		_ = s.post("/logout", nil)
		refused("an answer brought twice", http.StatusBadRequest, s.get("/oidc/callback?code=code-1&state=whatever"))

		p.user = "carol"
		refused("a name nobody has", http.StatusForbidden, p.signIn(s, "/", nil))
		p.user = "not a name"
		refused("what cannot be a name", http.StatusForbidden, p.signIn(s, "/", nil))
		p.spoil = func(claims map[string]any) { delete(claims, "preferred_username") }
		refused("no name", http.StatusForbidden, p.signIn(s, "/", nil))
		s.setting("bob", "enabled", false)
		p.user = "bob"
		refused("a disabled user", http.StatusForbidden, p.signIn(s, "/", nil))
	})
}

// A name nobody has gets a user where the installation makes users for
// names somebody vouches for.
func TestOIDCCreatesUsers(t *testing.T) {
	withProvider(t, func(t *testing.T, s *site, p *provider) {
		s.system(func(system *store.System) { system.HTTPAuthAutoRegister = true })
		p.user = "carol"
		if a := p.signIn(s, "/", nil); a.status != http.StatusSeeOther {
			t.Fatalf("the first sign-in of carol: status %d\n%s", a.status, a.body)
		}
		if who := s.signedInAs(); who != "carol" {
			t.Errorf("signed in as %q, want carol", who)
		}
		carol, err := s.db.UserByName(t.Context(), "carol")
		if err != nil || readPreferences(carol).PasswordHash != "" {
			t.Errorf("carol after the sign-in = %+v, err %v; want a user without a password", carol, err)
		}
	})
}

// Without the secret, a provider or the login by password nobody signs in
// through a provider.
func TestOIDCOff(t *testing.T) {
	p := newProvider(t)
	for name, o := range map[string]Options{
		"no secret":   {BaseURL: "https://reader.example"},
		"no provider": {BaseURL: "https://reader.example", OIDCClientSecret: oidcTestSecret},
		"no login":    {BaseURL: "https://reader.example", OIDCClientSecret: oidcTestSecret},
	} {
		t.Run(name, func(t *testing.T) {
			imported(t, o, func(t *testing.T, s *site) {
				s.system(func(system *store.System) {
					if name != "no provider" {
						system.OIDC = store.OIDC{Issuer: p.server.URL, ClientID: oidcTestClient}
					}
					if name == "no login" {
						system.AuthType = store.AuthNone
					}
				})
				for _, target := range []string{"/oidc/login", "/oidc/callback?code=x&state=y"} {
					if a := s.get(target); a.status != http.StatusNotFound {
						t.Errorf("GET %s: status %d, want 404", target, a.status)
					}
				}
				if name != "no login" && strings.Contains(s.shown("/login"), "/oidc/login") {
					t.Error("the login page offers a provider")
				}
			})
		})
	}
}

// A provider that cannot be reached leaves the login by password.
func TestOIDCProviderDown(t *testing.T) {
	withProvider(t, func(t *testing.T, s *site, p *provider) {
		s.system(func(system *store.System) { system.OIDC.Issuer = "http://127.0.0.1:1" })
		a := s.get("/oidc/login")
		if a.status != http.StatusBadGateway || !strings.Contains(a.body, "Signing in through the provider did not work.") {
			t.Errorf("GET /oidc/login with the provider down: status %d\n%s", a.status, a.body)
		}
		s.asAlice()
	})
}

// An administrator whose login has aged confirms it at the provider, and
// sets the provider up on the page of authentication.
func TestOIDCAdministration(t *testing.T) {
	withProvider(t, func(t *testing.T, s *site, p *provider) {
		now := s.clock()
		if a := p.signIn(s, "/", nil); a.status != http.StatusSeeOther {
			t.Fatalf("the sign-in: status %d", a.status)
		}
		page := s.shown("/admin/authentication")
		for _, want := range []string{`name="oidc_issuer" value="` + p.server.URL + `"`, `name="oidc_client_id" value="` + oidcTestClient + `"`, "https://reader.example/oidc/callback"} {
			if !strings.Contains(page, want) {
				t.Errorf("the page of authentication lacks %s", want)
			}
		}
		if strings.Contains(page, "-oidc-client-secret") {
			t.Error("the page of authentication misses a secret the server has")
		}

		*now = now.Add(21 * time.Minute)
		if a := s.get("/admin/authentication"); a.header.Get("Location") != "/reauth?next=%2Fadmin%2Fauthentication" {
			t.Fatalf("an aged login at administration: status %d, Location %q", a.status, a.header.Get("Location"))
		}
		if page := s.shown("/reauth?next=%2Fadmin%2Fauthentication"); !strings.Contains(page, `href="/oidc/login?next=%2Fadmin%2Fauthentication"`) {
			t.Errorf("the page that asks again does not offer the provider:\n%s", page)
		}
		if a := p.signIn(s, "/admin/authentication", nil); a.header.Get("Location") != "/admin/authentication" {
			t.Fatalf("signing in again: status %d, Location %q", a.status, a.header.Get("Location"))
		}

		form := s.formAt("/admin/authentication", "/admin/authentication")
		for issuer, client := range map[string]string{
			p.server.URL: "", "": oidcTestClient, "provider.example": oidcTestClient, "ftp://provider.example": oidcTestClient,
		} {
			form.Set("oidc_issuer", issuer)
			form.Set("oidc_client_id", client)
			a := s.post("/admin/authentication", form)
			if system, _ := s.db.System(t.Context()); a.status != http.StatusBadRequest || !strings.Contains(a.body, "The provider takes both") || system.OIDC.Issuer != p.server.URL {
				t.Errorf("issuer %q with client %q: status %d, stored %+v", issuer, client, a.status, system.OIDC)
			}
		}
		form.Set("oidc_issuer", " https://id.example.org ")
		form.Set("oidc_client_id", " reader ")
		s.follow("/admin/authentication", form)
		if system, _ := s.db.System(t.Context()); system.OIDC != (store.OIDC{Issuer: "https://id.example.org", ClientID: "reader"}) {
			t.Errorf("the provider after the form = %+v", system.OIDC)
		}
		form.Set("oidc_issuer", "")
		form.Set("oidc_client_id", "")
		s.follow("/admin/authentication", form)
		if system, _ := s.db.System(t.Context()); system.OIDC != (store.OIDC{}) {
			t.Errorf("the provider after it was taken away = %+v", system.OIDC)
		}
	})
}

// A user who has no password and came through the provider can keep the
// login by password switched on.
func TestOIDCAdministratorWithoutPassword(t *testing.T) {
	withProvider(t, func(t *testing.T, s *site, p *provider) {
		s.setting("alice", "passwordHash", "")
		if a := p.signIn(s, "/", nil); a.status != http.StatusSeeOther {
			t.Fatalf("the sign-in: status %d", a.status)
		}
		form := s.formAt("/admin/authentication", "/admin/authentication")
		if a := s.post("/admin/authentication", form); a.status != http.StatusSeeOther {
			t.Errorf("saving the page with a provider and no password: status %d\n%s", a.status, a.body)
		}
		form.Set("oidc_issuer", "")
		form.Set("oidc_client_id", "")
		if a := s.post("/admin/authentication", form); a.status != http.StatusBadRequest || !strings.Contains(a.body, "You have no password") {
			t.Errorf("taking the provider away with no password: status %d", a.status)
		}
	})
}
