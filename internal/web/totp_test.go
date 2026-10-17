package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/juev/freshgo/internal/authguard"
)

var totpSecretField = regexp.MustCompile(`name="totp_secret" value="([A-Z2-7]+)"`)

// code is what the app of the secret shows at the time.
func code(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(secret, at, totpOptions)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A user turns the code of an app on with the password and a code; a login
// by password then asks for a code, takes each code once, and forgets a
// login that waits too long or gets too many wrong codes. The code is turned
// off with the password and a code.
func TestTOTP(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		now := s.clock()
		s.whoami(members)
		s.asAlice()
		page := s.shown("/settings/profile")
		m := totpSecretField.FindStringSubmatch(page)
		if m == nil || !strings.Contains(page, `<img src="data:image/png;base64,`) {
			t.Fatalf("the profile does not offer the code of an app:\n%s", page)
		}
		secret := m[1]

		on := url.Values{"action": {"on"}, "totp_secret": {secret}, "password": {"alice-web-password"}, "code": {"000000"}}
		if code(t, secret, *now) == "000000" {
			on.Set("code", "111111")
		}
		a := s.post("/settings/profile/totp", on)
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, `name="totp_secret" value="`+secret+`"`) {
			t.Errorf("a wrong code: status %d, want 400 and the same secret\n%s", a.status, a.body)
		}
		on.Set("code", code(t, secret, *now))
		on.Set("password", "wrong")
		if a := s.post("/settings/profile/totp", on); a.status != http.StatusForbidden {
			t.Errorf("a wrong password: status %d", a.status)
		}
		on.Set("password", "alice-web-password")
		if location, body := s.follow("/settings/profile/totp", on); location != "/settings/profile" || notice(body) != "Signing in now asks for the code of your app." {
			t.Fatalf("turning the code on: Location %q, notice %q", location, notice(body))
		}
		if strings.Contains(s.shown("/settings/profile"), "totp_secret") {
			t.Error("the profile still offers to turn the code on")
		}
		_ = s.post("/logout", nil)

		// The password alone is no login.
		a = s.login("alice", "alice-web-password", url.Values{"next": {"/subscriptions"}})
		token := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(a.body)
		if a.status != http.StatusOK || token == nil || !strings.Contains(a.body, `action="/login/code"`) || s.signedInAs() != "" {
			t.Fatalf("the password of a user with a code: status %d, signed in as %q\n%s", a.status, s.signedInAs(), a.body)
		}
		send := func(token, c, addr string) answer {
			t.Helper()
			r := newPost("/login/code", url.Values{"token": {token}, "code": {c}})
			r.RemoteAddr = addr + ":4000"
			return s.send(r, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin"})
		}
		// The code that turned it on was taken.
		if a := send(token[1], code(t, secret, *now), "192.0.2.1"); a.status != http.StatusUnauthorized || !strings.Contains(a.body, "This code is not the one.") {
			t.Errorf("a code taken before: status %d\n%s", a.status, a.body)
		}
		*now = now.Add(totpPeriod)
		if a := send(token[1], code(t, secret, *now), "192.0.2.1"); a.status != http.StatusSeeOther || a.header.Get("Location") != "/subscriptions" {
			t.Fatalf("the code of the app: status %d, Location %q\n%s", a.status, a.header.Get("Location"), a.body)
		}
		if who := s.signedInAs(); who != "alice, administrator" {
			t.Errorf("signed in as %q after the code", who)
		}
		_ = s.post("/logout", nil)

		// A code is taken once, and a login that waits is forgotten after
		// a few wrong codes, from wherever they come.
		token = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(s.login("alice", "alice-web-password", nil).body)
		if a := send(token[1], code(t, secret, *now), "192.0.2.1"); a.status != http.StatusUnauthorized {
			t.Errorf("a code used twice: status %d", a.status)
		}
		for i := 2; i <= codeTries; i++ {
			a = send(token[1], "123456", "198.51.100."+string(rune('0'+i)))
		}
		if a.status != http.StatusUnauthorized || !strings.Contains(a.body, "had too many wrong codes") || !strings.Contains(a.body, `action="/login"`) {
			t.Errorf("the last wrong code: status %d\n%s", a.status, a.body)
		}
		*now = now.Add(totpPeriod)
		if a := send(token[1], code(t, secret, *now), "192.0.2.2"); a.status != http.StatusUnauthorized || s.signedInAs() != "" {
			t.Errorf("a right code for a forgotten login: status %d", a.status)
		}

		// A login waits for its code five minutes.
		token = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(s.login("alice", "alice-web-password", nil).body)
		*now = now.Add(codeLife)
		if a := send(token[1], code(t, secret, *now), "192.0.2.2"); a.status != http.StatusUnauthorized || s.signedInAs() != "" {
			t.Errorf("a code after five minutes: status %d", a.status)
		}

		// Off with the password and a code.
		token = regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(s.login("alice", "alice-web-password", nil).body)
		if a := send(token[1], code(t, secret, *now), "192.0.2.2"); a.status != http.StatusSeeOther {
			t.Fatalf("signing in: status %d", a.status)
		}
		off := url.Values{"action": {"off"}, "password": {"alice-web-password"}, "code": {code(t, secret, *now)}}
		if a := s.post("/settings/profile/totp", off); a.status != http.StatusBadRequest {
			t.Errorf("turning off with a code taken before: status %d", a.status)
		}
		// Wrong codes are counted even with the right password.
		for range authguard.FreeFailures - 1 {
			_ = s.post("/settings/profile/totp", off)
		}
		if a := s.post("/settings/profile/totp", off); a.status != http.StatusTooManyRequests {
			t.Errorf("guessing the code to turn it off: status %d, want 429", a.status)
		}
		*now = now.Add(authguard.LongestPause)
		*now = now.Add(totpPeriod)
		off.Set("code", code(t, secret, *now))
		if _, body := s.follow("/settings/profile/totp", off); notice(body) != "Signing in takes the password alone again." {
			t.Errorf("turning the code off: notice %q", notice(body))
		}
		_ = s.post("/logout", nil)
		if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusSeeOther {
			t.Errorf("the password once the code is off: status %d", a.status)
		}
	})
}

// An administrator turns the code off for a user who lost the app.
func TestTOTPOffByAdministrator(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("bob", "totp_secret", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
		s.asAlice()
		if page := s.shown("/admin/users/bob"); !strings.Contains(page, `value="totp-off"`) {
			t.Fatalf("the page of bob does not offer to turn the code off:\n%s", page)
		}
		s.follow("/admin/users/bob", url.Values{"action": {"totp-off"}})
		if _, on := s.settings("bob")["totp_secret"]; on {
			t.Error("bob still has the code of an app")
		}
		if strings.Contains(s.shown("/admin/users/bob"), `value="totp-off"`) {
			t.Error("the page of bob offers to turn off a code he no longer has")
		}
	})
}
