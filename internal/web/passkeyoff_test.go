package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/juev/freshgo/internal/store"
)

// switchPasswordLogin posts the form of /admin/authentication with the
// switch set as asked, and returns the answer.
func (s *site) switchPasswordLogin(off bool) answer {
	s.t.Helper()
	form := s.formAt("/admin/authentication", "/admin/authentication")
	form.Del("password_login_off")
	if off {
		form.Set("password_login_off", "1")
	}
	return s.post("/admin/authentication", form)
}

func (s *site) passwordLoginOff() bool {
	s.t.Helper()
	system, err := s.db.System(s.t.Context())
	if err != nil {
		s.t.Fatal(err)
	}
	return system.PasswordLoginOff
}

// An administrator with a passkey switches logins by password off: the form
// is gone from the login page, a password is refused whoever types it,
// a passkey logs in, and the page that asks for the password again asks for
// a passkey.
func TestPasswordLoginOff(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		now := s.clock()
		s.asAlice()
		a := s.addPasskey("laptop")
		if form := s.shown("/admin/authentication"); !strings.Contains(form, `name="password_login_off"`) || strings.Contains(form, `name="password_login_off" value="1" checked`) {
			t.Fatalf("the form of the authentication does not offer the switch, off:\n%s", form)
		}
		location, body := s.follow("/admin/authentication", func() url.Values {
			form := s.formAt("/admin/authentication", "/admin/authentication")
			form.Set("password_login_off", "1")
			return form
		}())
		if location != "/admin/authentication" || notice(body) != "Saved." || !s.passwordLoginOff() {
			t.Fatalf("switching it off: at %q, notice %q, off %v", location, notice(body), s.passwordLoginOff())
		}
		if !strings.Contains(body, `name="password_login_off" value="1" checked`) {
			t.Error("the form does not show the switch on")
		}

		// The same browser, an hour on, asked for the password again.
		s.post("/logout", url.Values{})
		login := s.get("/login").body
		if strings.Contains(login, `name="password"`) || strings.Contains(login, `name="username"`) ||
			!strings.Contains(login, "Signing in by password is switched off") || !strings.Contains(login, `id="passkey-signin"`) || !strings.Contains(login, `id="remember"`) {
			t.Errorf("the login page: %s", login)
		}
		for _, user := range [][2]string{{"alice", "alice-web-password"}, {"bob", "bob-web-password"}, {"nobody", "x"}, {"", ""}} {
			if got := s.login(user[0], user[1], nil); got.status != http.StatusForbidden || s.cookies[sessionCookie] != nil || !strings.Contains(got.body, "switched off") {
				t.Errorf("a password of %q: status %d, cookie %v", user[0], got.status, s.cookies[sessionCookie])
			}
		}
		if s.loggedIn() {
			t.Fatal("logged in by a password that was refused")
		}

		if got := s.passkeyLogin(a, "", false); got.status != http.StatusOK || !s.loggedIn() {
			t.Fatalf("the passkey: status %d", got.status)
		}
		// An old login answers with a passkey; the password is refused.
		*now = now.Add(30 * time.Minute)
		page := s.get("/reauth?next=%2Fadmin%2Fusers").body
		if strings.Contains(page, `name="password"`) || !strings.Contains(page, "passkey-signin") {
			t.Errorf("the page that asks again: %s", page)
		}
		if got := s.post("/reauth", url.Values{"password": {"alice-web-password"}, "next": {"/admin/users"}}); got.status != http.StatusForbidden || s.get("/admin/users").status == http.StatusOK {
			t.Errorf("a password at /reauth: status %d, want 403 and a login that stays old", got.status)
		}
		if got := s.reauthByPasskey(a, "/admin/users"); got.status != http.StatusOK || s.get("/admin/users").status != http.StatusOK {
			t.Errorf("a passkey at /reauth: status %d", got.status)
		}

		// A password cannot be asked for by a letter or by registering.
		if page := s.get("/register"); strings.Contains(page.body, `name="new"`) {
			t.Errorf("registration is open: %.300s", page.body)
		}
		if got := s.post("/register", url.Values{"name": {"carol"}, "new": {"long enough"}, "again": {"long enough"}}); got.status == http.StatusSeeOther {
			t.Errorf("registered with logins by password off: status %d", got.status)
		}
		if _, err := s.db.UserByName(t.Context(), "carol"); err == nil {
			t.Error("a user was made while logins by password were off")
		}

		// And back on.
		if got := s.switchPasswordLogin(false); got.status != http.StatusSeeOther || s.passwordLoginOff() {
			t.Fatalf("switching it on: status %d", got.status)
		}
		s.post("/logout", url.Values{})
		if got := s.login("alice", "alice-web-password", nil); got.status != http.StatusSeeOther {
			t.Errorf("the password after switching it on: status %d", got.status)
		}
	})
}

// A password typed before logins by password were switched off, and a code
// asked after it, are not a login.
func TestPasswordLoginOffEndsTheCodeStep(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		s.addPasskey("laptop")
		const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
		s.setting("alice", "totp_secret", secret)
		s.post("/logout", url.Values{})
		first := s.login("alice", "alice-web-password", nil)
		token := regexpToken(t, first.body)
		s.system(func(sys *store.System) { sys.PasswordLoginOff = true })
		code, err := totp.GenerateCode(secret, s.h.now())
		if err != nil {
			t.Fatal(err)
		}
		if got := s.post("/login/code", url.Values{"token": {token}, "code": {code}}); got.status != http.StatusForbidden || s.loggedIn() {
			t.Errorf("a code after the switch: status %d", got.status)
		}
	})
}

func regexpToken(t *testing.T, page string) string {
	t.Helper()
	const mark = `name="token" value="`
	i := strings.Index(page, mark)
	if i < 0 {
		t.Fatalf("no token in the page:\n%s", page)
	}
	rest := page[i+len(mark):]
	return rest[:strings.Index(rest, `"`)]
}

// Logins by password are not switched off by an administrator who would
// lose the way in: no passkey of their own, no passkey that works here, and
// no provider.
func TestPasswordLoginOffLockout(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		got := s.switchPasswordLogin(true)
		if got.status != http.StatusBadRequest || s.passwordLoginOff() || !strings.Contains(got.body, "no passkey that works here") {
			t.Fatalf("without a passkey: status %d, off %v", got.status, s.passwordLoginOff())
		}
		// The passkey of another user is no way in for this one.
		bob := &site{t: t, db: s.db, h: s.h}
		bob.login("bob", "bob-web-password", nil)
		bob.addPasskey("bob's")
		if got := s.switchPasswordLogin(true); got.status != http.StatusBadRequest || s.passwordLoginOff() {
			t.Errorf("with a passkey of bob: status %d", got.status)
		}

		s.addPasskey("laptop")
		// A passkey that cannot be used at the address the server has now
		// is no way in.
		s.h.baseURL = "http://reader.example"
		if got := s.switchPasswordLogin(true); got.status != http.StatusBadRequest || s.passwordLoginOff() {
			t.Errorf("with a passkey and no address for it: status %d", got.status)
		}
		s.h.baseURL = passkeyBase
		if got := s.switchPasswordLogin(true); got.status != http.StatusSeeOther || !s.passwordLoginOff() {
			t.Errorf("with a passkey: status %d, off %v", got.status, s.passwordLoginOff())
		}
	})

	// An administrator with a passkey and no web password, as one who came
	// through a provider is, has a way in: the missing password is no reason
	// to refuse.
	passkeyAt(t, func(t *testing.T, s *site) {
		carol := &store.User{Name: "carol", Settings: []byte(`{"is_admin":true}`)}
		if err := s.db.CreateUser(t.Context(), carol); err != nil {
			t.Fatal(err)
		}
		s.startLogin(carol)
		s.addPasskey("phone")
		if got := s.switchPasswordLogin(true); got.status != http.StatusSeeOther || !s.passwordLoginOff() {
			t.Errorf("with a passkey and no password: status %d, off %v", got.status, s.passwordLoginOff())
		}
	})

	// A provider is a way in without a passkey.
	imported(t, Options{BaseURL: passkeyBase, OIDCClientSecret: oidcTestSecret}, func(t *testing.T, s *site) {
		s.asAlice()
		s.system(func(sys *store.System) { sys.OIDC = store.OIDC{Issuer: "https://id.example", ClientID: "freshgo"} })
		if got := s.switchPasswordLogin(true); got.status != http.StatusSeeOther || !s.passwordLoginOff() {
			t.Errorf("with a provider and no passkey: status %d, off %v", got.status, s.passwordLoginOff())
		}
	})
}

// The last passkey of a user stays while there is no other way in; with a
// provider, or a second passkey, it goes.
func TestLastPasskeyStaysWithoutPasswords(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		s.addPasskey("one")
		s.addPasskey("two")
		s.system(func(sys *store.System) { sys.PasswordLoginOff = true })
		first, second := s.passkeys("alice")[0], s.passkeys("alice")[1]
		remove := func(id int64) answer {
			return s.post("/settings/profile/passkeys/"+itoa(id)+"/delete", url.Values{})
		}
		if got := remove(first.ID); got.status != http.StatusSeeOther {
			t.Fatalf("removing one of two: status %d", got.status)
		}
		if got := remove(second.ID); got.status != http.StatusBadRequest || !strings.Contains(got.body, "last passkey") || len(s.passkeys("alice")) != 1 {
			t.Errorf("removing the last: status %d", got.status)
		}
		s.system(func(sys *store.System) { sys.PasswordLoginOff = false })
		if got := remove(second.ID); got.status != http.StatusSeeOther || len(s.passkeys("alice")) != 0 {
			t.Errorf("removing the last with passwords on: status %d", got.status)
		}
	})
}

// An administrator removes the passkeys of another user, who loses the
// logins as well; not their own, and not as a user who is not one.
func TestAdministratorRemovesPasskeys(t *testing.T) {
	passkeyAt(t, func(t *testing.T, s *site) {
		s.asAlice()
		bob := &site{t: t, db: s.db, h: s.h}
		bob.login("bob", "bob-web-password", nil)
		one, two := bob.addPasskey("one"), bob.addPasskey("two")
		s.addPasskey("alice's")
		page := s.get("/admin/users/bob").body
		for _, want := range []string{">one<", ">two<", `action="/admin/users/bob/passkeys/delete"`, `name="id" value="all"`,
			"<summary>Delete…</summary>", "The passkey is deleted. The device that uses it can no longer log in with it."} {
			if !strings.Contains(page, want) {
				t.Errorf("the page of bob lacks %q", want)
			}
		}
		if own := s.get("/admin/users/alice").body; strings.Contains(own, "/passkeys/delete") {
			t.Error("the page of the administrator offers to remove their own passkeys")
		}

		first := bob.passkeys("bob")[0]
		location, body := s.follow("/admin/users/bob/passkeys/delete", url.Values{"id": {itoa(first.ID)}})
		if location != "/admin/users/bob" || notice(body) != "Passkey removed." || len(bob.passkeys("bob")) != 1 {
			t.Errorf("removing one: at %q, notice %q", location, notice(body))
		}
		if bob.loggedIn() {
			t.Error("bob stays logged in after his passkey was removed")
		}
		// The passkey that was removed no longer logs in, the other does.
		if got := bob.passkeyLogin(one, "", false); got.status != http.StatusUnauthorized || bob.loggedIn() {
			t.Errorf("the removed passkey: status %d", got.status)
		}
		if got := bob.passkeyLogin(two, "", false); got.status != http.StatusOK || !bob.loggedIn() {
			t.Errorf("the passkey that stays: status %d", got.status)
		}

		location, body = s.follow("/admin/users/bob/passkeys/delete", url.Values{"id": {"all"}})
		if location != "/admin/users/bob" || notice(body) != "Passkeys removed." || len(bob.passkeys("bob")) != 0 {
			t.Errorf("removing all: at %q, notice %q", location, notice(body))
		}

		for name, c := range map[string]struct {
			target string
			form   url.Values
			status int
		}{
			"their own":     {"/admin/users/alice/passkeys/delete", url.Values{"id": {"all"}}, http.StatusBadRequest},
			"unknown user":  {"/admin/users/nobody/passkeys/delete", url.Values{"id": {"all"}}, http.StatusNotFound},
			"not a number":  {"/admin/users/bob/passkeys/delete", url.Values{"id": {"x"}}, http.StatusBadRequest},
			"another's one": {"/admin/users/bob/passkeys/delete", url.Values{"id": {itoa(s.passkeys("alice")[0].ID)}}, http.StatusNotFound},
		} {
			if got := s.post(c.target, c.form); got.status != c.status {
				t.Errorf("%s: status %d, want %d", name, got.status, c.status)
			}
		}
		if len(s.passkeys("alice")) != 1 {
			t.Error("alice lost her passkey")
		}
		// A user who is not an administrator cannot.
		bob.login("bob", "bob-web-password", nil)
		if got := bob.post("/admin/users/alice/passkeys/delete", url.Values{"id": {"all"}}); got.status != http.StatusForbidden || len(s.passkeys("alice")) != 1 {
			t.Errorf("bob removes alice's passkeys: status %d", got.status)
		}
	})
}
