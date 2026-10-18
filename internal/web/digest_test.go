package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/mail/mailtest"
)

const digestBox = `name="digest"`

// The profile offers the daily digest only where letters can go out and
// the user has an address; a form that asks for it without either leaves it
// off. Switching it on starts its period.
func TestDigestSwitch(t *testing.T) {
	smtp := mailtest.New(t)
	sender, err := mail.New(smtp.URL)
	if err != nil {
		t.Fatal(err)
	}
	imported(t, Options{Mailer: sender}, func(t *testing.T, s *site) {
		now := s.clock()
		s.asAlice()
		s.setting("alice", "mail_login", "")
		if page := s.shown("/settings/profile"); strings.Contains(page, digestBox) {
			t.Errorf("the profile of a user without an address offers the digest:\n%s", page)
		}
		s.follow("/settings/profile", url.Values{"email": {""}, "token": {""}, "digest": {"1"}})
		if got := s.settings("alice"); got["digest"] != false || got["digest_sent"] != nil {
			t.Errorf("the digest without an address: digest = %v, digest_sent = %v", got["digest"], got["digest_sent"])
		}

		s.setting("alice", "mail_login", "alice@example.org")
		if page := s.shown("/settings/profile"); !strings.Contains(page, digestBox) || strings.Contains(page, digestBox+` value="1" checked`) {
			t.Errorf("the profile with an address does not offer the digest, off:\n%s", page)
		}
		s.follow("/settings/profile", url.Values{"email": {"alice@example.org"}, "token": {""}, "digest": {"1"}})
		got := s.settings("alice")
		if got["digest"] != true || got["digest_sent"] != float64(now.Unix()) {
			t.Errorf("the digest switched on: digest = %v, digest_sent = %v, want true and %d", got["digest"], got["digest_sent"], now.Unix())
		}
		if page := s.shown("/settings/profile"); !strings.Contains(page, `id="digest" name="digest" value="1" checked`) {
			t.Errorf("the profile does not show the digest on:\n%s", page)
		}

		// Saving again does not move the start of the period.
		*now = now.Add(3 * time.Hour)
		s.follow("/settings/profile", url.Values{"email": {"alice@example.org"}, "token": {""}, "digest": {"1"}})
		if got := s.settings("alice")["digest_sent"]; got == float64(now.Unix()) {
			t.Errorf("saving the profile again moved the period to %v", got)
		}

		// A form without the box, or without the address, turns it off.
		s.follow("/settings/profile", url.Values{"email": {"alice@example.org"}, "token": {""}})
		if got := s.settings("alice")["digest"]; got != false {
			t.Errorf("the digest after a form without it = %v", got)
		}
	})
}

// Without an SMTP server there is nothing to offer and nothing to switch on.
func TestDigestNeedsLetters(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		s.setting("alice", "mail_login", "alice@example.org")
		if page := s.shown("/settings/profile"); strings.Contains(page, digestBox) {
			t.Errorf("the profile offers the digest without an SMTP server:\n%s", page)
		}
		s.follow("/settings/profile", url.Values{"email": {"alice@example.org"}, "token": {""}, "digest": {"1"}})
		if got := s.settings("alice")["digest"]; got != false {
			t.Errorf("the digest without an SMTP server = %v", got)
		}
	})
}
