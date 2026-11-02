package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// reauthByPasskey runs the two requests of the page that asks for the
// password again, and returns the answer of the second.
func (s *site) reauthByPasskey(a *authenticator, next string) answer {
	s.t.Helper()
	begin := s.postJSON(passkeyReauthBeginPath, `{"next":`+jsonString(next)+`}`)
	if begin.status != http.StatusOK {
		s.t.Fatalf("begin: status %d: %s", begin.status, begin.body)
	}
	return s.postJSON(passkeyReauthFinishPath, a.asserted(begin.body))
}

// An administrator whose login is old answers /reauth with a passkey, and
// the administration opens again, as it does after the password.
func TestPasskeyAnswersReauth(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		now := s.clock()
		s.asAlice()
		a := s.addPasskey("laptop")
		*now = now.Add(30 * time.Minute)
		if got := s.get("/admin/users"); got.status != http.StatusSeeOther || got.header.Get("Location") != "/reauth?next=%2Fadmin%2Fusers" {
			t.Fatalf("the administration with an old login: status %d, %q", got.status, got.header.Get("Location"))
		}
		page := s.get("/reauth?next=%2Fadmin%2Fusers").body
		for _, want := range []string{`id="passkey-signin"`, `data-begin="/passkeys/reauth/begin"`, `data-finish="/passkeys/reauth/finish"`, `data-next="/admin/users"`, "Use a passkey"} {
			if !strings.Contains(page, want) {
				t.Errorf("the page lacks %q", want)
			}
		}

		begin := s.postJSON(passkeyReauthBeginPath, `{"next":"/admin/users"}`)
		var options struct {
			PublicKey struct {
				UserVerification string
				AllowCredentials []struct{ ID string }
			}
		}
		if err := json.Unmarshal([]byte(begin.body), &options); err != nil {
			t.Fatal(err)
		}
		// Only the passkeys of the user who asks are offered.
		if pk := options.PublicKey; pk.UserVerification != "required" || len(pk.AllowCredentials) != 1 || string(unb64(t, pk.AllowCredentials[0].ID)) != string(a.id) {
			t.Errorf("options = %+v", pk)
		}
		finish := s.postJSON(passkeyReauthFinishPath, a.asserted(begin.body))
		if finish.status != http.StatusOK || finish.body != `{"next":"/admin/users"}` {
			t.Fatalf("finish: status %d: %s", finish.status, finish.body)
		}
		if got := s.get("/admin/users"); got.status != http.StatusOK {
			t.Errorf("the administration after the passkey: status %d", got.status)
		}
		if got := s.passkeys("alice")[0]; got.Used != now.Unix() || !strings.Contains(string(got.Credential), `"signCount":1`) {
			t.Errorf("the passkey after the answer: used %d, %s", got.Used, got.Credential)
		}
		// The login itself is the one the browser had.
		if !s.loggedIn() {
			t.Error("the browser is not logged in")
		}

		// A page for a user with no passkey offers none.
		s.post("/logout", url.Values{})
		s.login("bob", "bob-web-password", nil)
		*now = now.Add(30 * time.Minute)
		if strings.Contains(s.get("/reauth").body, "passkey-signin") {
			t.Error("bob is offered a passkey he does not have")
		}
		if got := s.postJSON(passkeyReauthBeginPath, `{}`); got.status != http.StatusNotFound {
			t.Errorf("begin for a user without passkeys: status %d, want 404", got.status)
		}
	})
}

// The answer is the answer of this user's passkey to the challenge this
// login was given, once; a passkey of another user, a counter that stood
// still and the ceremony of another kind do not make a login fresh.
func TestPasskeyReauthRefusals(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		now := s.clock()
		s.asAlice()
		a := s.addPasskey("laptop")
		bob := &site{t: t, db: s.db, h: s.h}
		bob.login("bob", "bob-web-password", nil)
		theirs := bob.addPasskey("bob's")
		*now = now.Add(30 * time.Minute)
		stale := func(what string, status int, got answer) {
			t.Helper()
			if got.status != status || s.get("/admin/users").status == http.StatusOK {
				t.Errorf("%s: status %d (want %d), administration open %v", what, got.status, status, s.get("/admin/users").status == http.StatusOK)
			}
		}

		// Bob's passkey does not make alice's login fresh.
		stale("another user's passkey", http.StatusUnauthorized, s.reauthByPasskey(theirs, "/"))

		// Once.
		begin := s.postJSON(passkeyReauthBeginPath, `{}`)
		answer := a.asserted(begin.body)
		if ok := s.postJSON(passkeyReauthFinishPath, answer); ok.status != http.StatusOK {
			t.Fatalf("the first answer: %d", ok.status)
		}
		*now = now.Add(30 * time.Minute)
		stale("the same answer again", http.StatusBadRequest, s.postJSON(passkeyReauthFinishPath, answer))

		// A counter that stood still.
		copied := *a
		copied.count = 0
		stale("a counter that did not move", http.StatusUnauthorized, s.reauthByPasskey(&copied, "/"))

		// A ceremony of another kind: the challenge of a login does not
		// answer this.
		visitor := &site{t: t, db: s.db, h: s.h}
		login := visitor.postJSON(passkeyLoginBeginPath, `{}`)
		s.cookies[passkeyCookie] = visitor.cookies[passkeyCookie]
		stale("the ceremony of a login", http.StatusBadRequest, s.postJSON(passkeyReauthFinishPath, a.asserted(login.body)))
		// And the other way round.
		begin = s.postJSON(passkeyReauthBeginPath, `{}`)
		visitor.cookies[passkeyCookie] = s.cookies[passkeyCookie]
		if got := visitor.postJSON(passkeyLoginFinishPath, a.asserted(begin.body)); got.status != http.StatusBadRequest || visitor.loggedIn() {
			t.Errorf("the ceremony of a reauthentication logs in: status %d", got.status)
		}

		// Another login of alice's own.
		second := &site{t: t, db: s.db, h: s.h}
		second.asAlice()
		*now = now.Add(30 * time.Minute)
		begin = s.postJSON(passkeyReauthBeginPath, `{}`)
		second.cookies[passkeyCookie] = s.cookies[passkeyCookie]
		if got := second.postJSON(passkeyReauthFinishPath, a.asserted(begin.body)); got.status != http.StatusBadRequest || second.get("/admin/users").status == http.StatusOK {
			t.Errorf("another login finishes the ceremony: status %d", got.status)
		}

		// A visitor has nobody to confirm.
		if got := (&site{t: t, db: s.db, h: s.h}).postJSON(passkeyReauthBeginPath, `{}`); got.status != http.StatusForbidden {
			t.Errorf("begin by a visitor: status %d, want 403", got.status)
		}
	})
}
