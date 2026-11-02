package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

// passkeyLogin runs the two requests of the login page for an authenticator
// and returns the answer of the second.
func (s *site) passkeyLogin(a *authenticator, next string, remember bool) answer {
	s.t.Helper()
	begin := s.postJSON(passkeyLoginBeginPath, `{"next":`+jsonString(next)+`,"remember":`+map[bool]string{true: "true", false: "false"}[remember]+`}`)
	if begin.status != http.StatusOK {
		s.t.Fatalf("begin: status %d: %s", begin.status, begin.body)
	}
	return s.postJSON(passkeyLoginFinishPath, a.asserted(begin.body))
}

// loggedIn reports whether the browser of the test is logged in.
func (s *site) loggedIn() bool {
	s.t.Helper()
	return s.get("/settings/profile").status == http.StatusOK
}

// withPasskey is a site where alice has a passkey and is logged out.
func withPasskey(t *testing.T, test func(t *testing.T, s *site, a *authenticator)) {
	t.Helper()
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		a := s.addPasskey("laptop")
		if a := s.post("/logout", url.Values{}); a.status != http.StatusSeeOther {
			t.Fatalf("logout: %d", a.status)
		}
		if s.loggedIn() {
			t.Fatal("still logged in after logging out")
		}
		test(t, s, a)
	})
}

// A user who registered a passkey and logged out logs in with it, without
// a password or a code, and lands where they were going.
func TestPasskeyLogin(t *testing.T) {
	withPasskey(t, func(t *testing.T, s *site, a *authenticator) {
		page := s.get("/login").body
		for _, want := range []string{`id="passkey-signin"`, `data-begin="/passkeys/login/begin"`, `data-finish="/passkeys/login/finish"`, "Sign in with a passkey"} {
			if !strings.Contains(page, want) {
				t.Errorf("the login page lacks %q", want)
			}
		}

		begin := s.postJSON(passkeyLoginBeginPath, `{"next":"/feeds/1"}`)
		var options struct {
			PublicKey struct {
				RPID             string `json:"rpId"`
				Challenge        string
				UserVerification string
				AllowCredentials []any
			}
		}
		if err := json.Unmarshal([]byte(begin.body), &options); err != nil {
			t.Fatal(err)
		}
		// Nobody is named: the authenticator offers what it has.
		if pk := options.PublicKey; pk.RPID != "freshgo.example" || pk.UserVerification != "required" || len(pk.AllowCredentials) != 0 || len(unb64(t, pk.Challenge)) < 16 {
			t.Errorf("options = %+v", pk)
		}
		finish := s.postJSON(passkeyLoginFinishPath, a.asserted(begin.body))
		if finish.status != http.StatusOK || finish.body != `{"next":"/feeds/1"}` {
			t.Fatalf("finish: status %d: %s", finish.status, finish.body)
		}
		session := s.cookies[sessionCookie]
		if session == nil || !session.HttpOnly || session.MaxAge != 0 {
			t.Errorf("session cookie = %+v, want one for the browser alone", session)
		}
		if !s.loggedIn() {
			t.Fatal("not logged in after the passkey")
		}
		if got := s.passkeys("alice")[0]; got.Used == 0 || !strings.Contains(string(got.Credential), `"signCount":1`) {
			t.Errorf("after the login: used %d, credential %s; want the time and the counter kept", got.Used, got.Credential)
		}
		// Fresh: the administration needs no password after a passkey.
		if a := s.get("/admin/users"); a.status != http.StatusOK {
			t.Errorf("the administration after a passkey login: status %d", a.status)
		}

		// The counter goes on from the last login, and a login is
		// remembered when it is asked to be.
		s.post("/logout", url.Values{})
		finish = s.passkeyLogin(a, "//evil.example", true)
		if finish.status != http.StatusOK || finish.body != `{"next":"/"}` {
			t.Errorf("second login: status %d: %s; want the start page, not another site", finish.status, finish.body)
		}
		if c := s.cookies[sessionCookie]; c == nil || c.MaxAge <= 0 {
			t.Errorf("session cookie of a login that is remembered = %+v", c)
		}
	})
}

// A passkey logs in a user who has the code of an app turned on, without
// the code: it is the whole login.
func TestPasskeyReplacesTheCode(t *testing.T) {
	withPasskey(t, func(t *testing.T, s *site, a *authenticator) {
		s.setting("alice", "totp_secret", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
		if finish := s.passkeyLogin(a, "", false); finish.status != http.StatusOK || !s.loggedIn() {
			t.Errorf("status %d, logged in %v; want a login without a code", finish.status, s.loggedIn())
		}
	})
}

// What the server will not log in on: a removed passkey, a signature that
// does not check, one over a challenge that was used, one from a user who
// was not verified, a counter that did not move, a user handle that is
// another user's, a user who is kept out.
func TestPasskeyLoginRefusals(t *testing.T) {
	withPasskey(t, func(t *testing.T, s *site, a *authenticator) {
		refused := func(what string, status int, finish answer) {
			t.Helper()
			if finish.status != status || s.loggedIn() || s.cookies[sessionCookie] != nil {
				t.Errorf("%s: status %d, logged in %v; want %d and nobody logged in", what, finish.status, s.loggedIn(), status)
			}
		}
		stored := func() string { return string(s.passkeys("alice")[0].Credential) }

		// A passkey that has been used before.
		a.count = 4
		// A challenge answers once.
		begin := s.postJSON(passkeyLoginBeginPath, `{}`)
		answer := a.asserted(begin.body)
		if ok := s.postJSON(passkeyLoginFinishPath, answer); ok.status != http.StatusOK {
			t.Fatalf("the first answer: %d", ok.status)
		}
		s.post("/logout", url.Values{})
		refused("the same answer again", http.StatusBadRequest, s.postJSON(passkeyLoginFinishPath, answer))
		// A browser that did not get the challenge cannot answer it.
		begin = s.postJSON(passkeyLoginBeginPath, `{}`)
		other := &site{t: t, db: s.db, h: s.h}
		if got := other.postJSON(passkeyLoginFinishPath, a.asserted(begin.body)); got.status != http.StatusBadRequest || other.loggedIn() {
			t.Errorf("a browser without the secret of the ceremony: status %d", got.status)
		}
		if s.loggedIn() {
			t.Fatal("the browser that began the ceremony is logged in by an answer it did not send")
		}
		// The ceremony of this browser is still its own to answer; spend it.
		s.postJSON(passkeyLoginFinishPath, `{}`)

		wrongOrigin := *a
		wrongOrigin.origin = "https://evil.example"
		refused("another origin", http.StatusUnauthorized, s.passkeyLogin(&wrongOrigin, "", false))

		wrongRP := *a
		wrongRP.rpID = "evil.example"
		refused("another relying party", http.StatusUnauthorized, s.passkeyLogin(&wrongRP, "", false))

		unverified := *a
		unverified.flags = flagPresent
		refused("a user who was not verified", http.StatusUnauthorized, s.passkeyLogin(&unverified, "", false))

		absent := *a
		absent.flags = flagVerified
		refused("a user who was not present", http.StatusUnauthorized, s.passkeyLogin(&absent, "", false))

		forged := *a
		forged.key = newAuthenticator(t).key
		refused("a signature of another key", http.StatusUnauthorized, s.passkeyLogin(&forged, "", false))

		unknown := *a
		unknown.id = []byte("a credential nobody registered")
		refused("an unknown credential", http.StatusUnauthorized, s.passkeyLogin(&unknown, "", false))

		// The handle of the passkey is its user's.
		strange := *a
		strange.handle = []byte("some other handle")
		refused("a user handle that is not the user's", http.StatusUnauthorized, s.passkeyLogin(&strange, "", false))
		none := *a
		none.handle = nil
		refused("no user handle", http.StatusUnauthorized, s.passkeyLogin(&none, "", false))
		bobsHandle := make([]byte, passkeyHandleSize)
		s.setting("bob", "passkey_handle", b64(bobsHandle))
		stolen := *a
		stolen.handle = bobsHandle
		refused("the handle of another user", http.StatusUnauthorized, s.passkeyLogin(&stolen, "", false))

		// A counter that stands still or goes back: a copy of the passkey.
		copied := *a
		copied.count = 2
		refused("a counter that went back", http.StatusUnauthorized, s.passkeyLogin(&copied, "", false))
		copied.count = 4
		refused("a counter that stood still", http.StatusUnauthorized, s.passkeyLogin(&copied, "", false))

		// Nothing the refusals did changed the passkey: the one login that
		// worked left its counter and its time.
		if got := s.passkeys("alice")[0]; got.Used == 0 || !strings.Contains(string(got.Credential), `"signCount":5`) {
			t.Errorf("after the refusals: used %d, credential %s", got.Used, got.Credential)
		}

		// The user kept out.
		s.setting("alice", "enabled", false)
		refused("a user who is kept out", http.StatusUnauthorized, s.passkeyLogin(a, "", false))
		s.setting("alice", "enabled", true)
		if ok := s.passkeyLogin(a, "", false); ok.status != http.StatusOK {
			t.Errorf("alice back in: status %d, %v", ok.status, stored())
		}
	})
}

// A removed passkey no longer logs in.
func TestPasskeyRemovedDoesNotLogIn(t *testing.T) {
	withPasskey(t, func(t *testing.T, s *site, a *authenticator) {
		if ok := s.passkeyLogin(a, "", false); ok.status != http.StatusOK || !s.loggedIn() {
			t.Fatalf("login: %d", ok.status)
		}
		id := s.passkeys("alice")[0].ID
		if a := s.post("/settings/profile/passkeys/"+itoa(id)+"/delete", url.Values{}); a.status != http.StatusSeeOther {
			t.Fatalf("removal: %d", a.status)
		}
		s.post("/logout", url.Values{})
		if got := s.passkeyLogin(a, "", false); got.status != http.StatusUnauthorized || s.loggedIn() {
			t.Errorf("a removed passkey: status %d, logged in %v", got.status, s.loggedIn())
		}
	})
}

// Logging in with a passkey ends the login the browser had.
func TestPasskeyLoginEndsTheLoginBefore(t *testing.T) {
	withPasskey(t, func(t *testing.T, s *site, a *authenticator) {
		bob := s.user("bob")
		s.login("bob", "bob-web-password", nil)
		before := s.cookies[sessionCookie].Value
		if ok := s.passkeyLogin(a, "", false); ok.status != http.StatusOK {
			t.Fatalf("login: %d", ok.status)
		}
		if _, err := s.db.Session(context.Background(), tokenHash(before), 0); err == nil {
			t.Errorf("the login of %s stays after another user logged in", bob.Name)
		}
		if page := s.get("/settings/profile"); !strings.Contains(page.body, ">alice<") {
			t.Errorf("the browser is not alice's: %.200s", page.body)
		}
	})
}

// A request from another site changes nothing, and a server that does not
// log users in by a form has no passkeys to log in with.
func TestPasskeyLoginRequests(t *testing.T) {
	withPasskey(t, func(t *testing.T, s *site, a *authenticator) {
		if got := s.postJSON(passkeyLoginBeginPath, `{}`, "Sec-Fetch-Site", "cross-site"); got.status != http.StatusForbidden {
			t.Errorf("begin from another site: status %d, want 403", got.status)
		}
		begin := s.postJSON(passkeyLoginBeginPath, `{}`)
		if got := s.postJSON(passkeyLoginFinishPath, a.asserted(begin.body), "Sec-Fetch-Site", "cross-site"); got.status != http.StatusForbidden || s.loggedIn() {
			t.Errorf("finish from another site: status %d", got.status)
		}
		if got := s.do(http.MethodGet, passkeyLoginBeginPath, nil); got.status != http.StatusMethodNotAllowed {
			t.Errorf("GET of begin: status %d, want 405", got.status)
		}
		if got := s.postJSON(passkeyLoginBeginPath, `{`); got.status != http.StatusBadRequest {
			t.Errorf("begin with garbage: status %d", got.status)
		}
		s.system(func(sys *store.System) { sys.AuthType = store.AuthNone })
		if got := s.postJSON(passkeyLoginBeginPath, `{}`); got.status != http.StatusNotFound {
			t.Errorf("begin without a login by form: status %d, want 404", got.status)
		}
	})
}
