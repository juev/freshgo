package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/mail/mailtest"
)

var resetLink = regexp.MustCompile(`https://reader\.example/reset\?\S+`)

// A user who forgot the password asks for a letter, follows its link once
// within the hour and chooses another; every login of the user ends. The
// page says the same whoever was asked for, and only a confirmed address
// gets a letter.
func TestPasswordReset(t *testing.T) {
	smtp := mailtest.New(t)
	sender, err := mail.New(smtp.URL)
	if err != nil {
		t.Fatal(err)
	}
	imported(t, Options{Mailer: sender, BaseURL: "https://reader.example"}, func(t *testing.T, s *site) {
		now := s.clock()
		s.setting("alice", "mail_login", "alice@example.org")
		before := len(smtp.Letters())
		if page := s.shown("/login"); !strings.Contains(page, `<a href="/reset">Forgot the password?</a>`) {
			t.Errorf("the login page does not offer to reset the password:\n%s", page)
		}
		if page := s.shown("/reset"); !strings.Contains(page, `action="/reset"`) {
			t.Errorf("GET /reset:\n%s", page)
		}
		ask := func(name string) answer {
			t.Helper()
			a := s.post("/reset", url.Values{"username": {name}})
			if a.status != http.StatusOK || !strings.Contains(a.body, "If the account has a confirmed e-mail address, a letter with a link is on its way.") {
				t.Fatalf("asking for a letter for %q: status %d\n%s", name, a.status, a.body)
			}
			return a
		}

		// Nobody, a user without an address, one whose address is not
		// confirmed: the same page, and no letter.
		s.setting("bob", "enabled", true)
		ask("nobody")
		ask("bob")
		s.setting("alice", "email_validation_token", "unconfirmed")
		ask("alice")
		if letters := smtp.Letters()[before:]; len(letters) != 0 {
			t.Fatalf("letters without a confirmed address: %+v", letters)
		}
		s.setting("alice", "email_validation_token", "")

		// Alice is logged in elsewhere until the password is reset.
		elsewhere := &site{t: t, db: s.db, h: s.h}
		elsewhere.whoami(members)
		elsewhere.asAlice()
		if who := elsewhere.signedInAs(); who != "alice, administrator" {
			t.Fatalf("alice elsewhere is signed in as %q", who)
		}

		ask("alice")
		letters := smtp.Letters()[before:]
		if len(letters) != 1 || letters[0].To != "alice@example.org" || letters[0].Subject != "A new password — freshgo" {
			t.Fatalf("letters for alice = %+v", letters)
		}
		link := strings.TrimPrefix(resetLink.FindString(letters[0].Body), "https://reader.example")
		at, _ := url.Parse(link)
		token := at.Query().Get("token")
		if at.Path != "/reset" || at.Query().Get("user") != "alice" || len(token) < 32 {
			t.Fatalf("the link of the letter = %q", link)
		}
		if strings.Contains(s.settings("alice")["password_reset"].(string), token) {
			t.Error("the token of the link is stored as it is")
		}

		for _, target := range []string{"/reset?user=alice&token=wrong", "/reset?user=bob&token=" + url.QueryEscape(token)} {
			if a := s.get(target); a.status != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", target, a.status)
			}
		}
		if page := s.shown(link); !strings.Contains(page, `action="/reset/password"`) || !strings.Contains(page, `name="token" value="`+token+`"`) {
			t.Fatalf("the page of the link:\n%s", page)
		}
		choose := url.Values{"user": {"alice"}, "token": {token}, "new": {"a new password"}, "again": {"another one"}}
		if a := s.post("/reset/password", choose); a.status != http.StatusBadRequest || !strings.Contains(a.body, `name="token" value="`+token+`"`) {
			t.Errorf("two passwords that differ: status %d\n%s", a.status, a.body)
		}
		choose.Set("again", "a new password")
		if location, body := s.follow("/reset/password", choose); location != "/login" || notice(body) != "The password is changed. Sign in with it." {
			t.Errorf("choosing the password: Location %q, notice %q", location, notice(body))
		}
		if who := elsewhere.signedInAs(); who != "" {
			t.Errorf("alice is still logged in elsewhere as %q", who)
		}
		if a := s.login("alice", "alice-web-password", nil); a.status != http.StatusUnauthorized {
			t.Errorf("the old password: status %d", a.status)
		}
		if a := s.login("alice", "a new password", nil); a.status != http.StatusSeeOther {
			t.Errorf("the new password: status %d", a.status)
		}
		// The link works once.
		if a := s.post("/reset/password", choose); a.status != http.StatusNotFound {
			t.Errorf("the link used again: status %d", a.status)
		}

		// A link works for an hour.
		*now = now.Add(time.Hour)
		ask("alice")
		letters = smtp.Letters()[before:]
		link = strings.TrimPrefix(resetLink.FindString(letters[len(letters)-1].Body), "https://reader.example")
		*now = now.Add(resetLife)
		if a := s.get(link); a.status != http.StatusNotFound {
			t.Errorf("a link an hour old: status %d", a.status)
		}
	})
}

// Without a server to send letters through, nobody resets a password.
func TestPasswordResetWithoutMail(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		if strings.Contains(s.shown("/login"), `href="/reset"`) {
			t.Error("the login page offers to reset the password without a way to send letters")
		}
		if a := s.get("/reset"); a.status != http.StatusNotFound {
			t.Errorf("GET /reset: status %d", a.status)
		}
		if a := s.post("/reset", url.Values{"username": {"alice"}}); a.status != http.StatusNotFound {
			t.Errorf("POST /reset: status %d", a.status)
		}
	})
}
