package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/mail/mailtest"
	"github.com/juev/freshgo/internal/store"
)

var mailedLink = regexp.MustCompile(`https?://\S+/validate-email\?\S+`)

// R16: a visitor makes an account while registration is open, and reads
// what the administrator has to say when it is not.
func TestRegistration(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		ctx := context.Background()
		form := url.Values{"username": {"carol"}, "password": {"carol-password"}, "again": {"carol-password"}}
		// FreshRSS closes registration by default: one user is the limit.
		if a := s.get("/register"); a.status != http.StatusForbidden || !strings.Contains(a.body, "Nobody can register here at the moment") || strings.Contains(a.body, "<form method=\"post\" action=\"/register\"") {
			t.Errorf("GET /register while closed: status %d\n%s", a.status, a.body)
		}
		if strings.Contains(s.page("/login"), `href="/register"`) {
			t.Error("the login page offers to register while nobody can")
		}
		s.system(func(system *store.System) { system.ClosedRegistrationMessage = "Write to admin@example.org." })
		if a := s.post("/register", form); a.status != http.StatusForbidden || !strings.Contains(a.body, "Write to admin@example.org.") {
			t.Errorf("POST /register while closed: status %d", a.status)
		}
		if _, err := s.db.UserByName(ctx, "carol"); err == nil {
			t.Fatal("a user registered while registration was closed")
		}

		// Three users may be here: one more than there are.
		s.system(func(system *store.System) { system.Limits.MaxRegistrations, system.TOS = 3, "<p>Be <b>kind</b>.</p><script>x()</script>" })
		page := s.shown("/register")
		if !strings.Contains(page, `name="accept_tos"`) || !strings.Contains(page, `<a href="/tos">Terms of use</a>`) || !strings.Contains(s.page("/login"), `href="/register"`) {
			t.Errorf("GET /register while open:\n%s", page)
		}
		if terms := s.shown("/tos"); !strings.Contains(terms, "<p>Be <b>kind</b>.</p>") || strings.Contains(terms, "x()") {
			t.Errorf("GET /tos:\n%s", terms)
		}
		for name, c := range map[string]struct {
			change url.Values
			want   string
		}{
			"terms not accepted": {nil, "The terms have to be accepted."},
			"name taken":         {url.Values{"username": {"alice"}, "accept_tos": {"1"}}, "A user has this name already."},
			"bad name":           {url.Values{"username": {"no spaces"}, "accept_tos": {"1"}}, "This cannot be a user name."},
			"short password":     {url.Values{"password": {"short"}, "again": {"short"}, "accept_tos": {"1"}}, "at least 7 characters"},
			"passwords differ":   {url.Values{"again": {"carol-passwore"}, "accept_tos": {"1"}}, "The two passwords differ."},
			"bad address":        {url.Values{"email": {"nobody"}, "accept_tos": {"1"}}, "This is not an e-mail address."},
		} {
			sent := url.Values{}
			for key, values := range form {
				sent[key] = values
			}
			for key, values := range c.change {
				sent[key] = values
			}
			a := s.post("/register", sent)
			if a.status != http.StatusBadRequest || !strings.Contains(a.body, c.want) || !strings.Contains(a.body, `value="`+sent.Get("username")+`"`) {
				t.Errorf("%s: status %d, want 400 with %q", name, a.status, c.want)
			}
		}
		form.Set("accept_tos", "1")
		form.Set("email", "carol@example.org")
		r := s.post("/register", form)
		carol, err := s.db.UserByName(ctx, "carol")
		if r.status != http.StatusSeeOther || r.header.Get("Location") != "/" || err != nil {
			t.Fatalf("registration: status %d, %v", r.status, err)
		}
		settings := decoded(t, carol.Settings)
		hash, _ := settings["passwordHash"].(string)
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte("carol-password")) != nil || settings["mail_login"] != "carol@example.org" || settings["language"] != "en" ||
			settings["is_admin"] != nil || carol.APIPasswordHash != "" {
			t.Errorf("the registered user = %s", carol.Settings)
		}
		// The visitor is logged in, as a plain user with a library of their own.
		if body := s.page("/"); !strings.Contains(body, `<span class="account-name">carol</span>`) || strings.Contains(body, "/admin/users") {
			t.Errorf("the page after registering:\n%.600s", body)
		}
		if a := s.get("/register"); a.status != http.StatusSeeOther {
			t.Errorf("GET /register while logged in: status %d", a.status)
		}
		// Now the installation is full again.
		s.cookies = nil
		form.Set("username", "dave")
		if a := s.post("/register", form); a.status != http.StatusForbidden {
			t.Errorf("registering past the limit: status %d", a.status)
		}
		// Without a login by password nobody registers.
		s.system(func(system *store.System) { system.Limits.MaxRegistrations, system.AuthType = 0, store.AuthHTTP })
		if a := s.post("/register", form); a.status != http.StatusForbidden {
			t.Errorf("registering where users are told apart by the proxy: status %d", a.status)
		}
	})
}

// R16: where addresses have to be confirmed, a new user gets a letter with
// a link and sees nothing but the page that waits for it, the profile and
// the way out until the link is followed.
func TestEmailValidation(t *testing.T) {
	smtp := mailtest.New(t)
	sender, err := mail.New(smtp.URL)
	if err != nil {
		t.Fatal(err)
	}
	imported(t, Options{Mailer: sender, BaseURL: "https://reader.example"}, func(t *testing.T, s *site) {
		before := len(smtp.Letters())
		s.system(func(system *store.System) { system.Limits.MaxRegistrations, system.ForceEmailValidation = 0, true })
		form := url.Values{"username": {"carol"}, "password": {"carol-password"}, "again": {"carol-password"}}
		if a := s.post("/register", form); a.status != http.StatusBadRequest || !strings.Contains(a.body, "This is not an e-mail address.") {
			t.Errorf("registering without an address where one is needed: status %d", a.status)
		}
		form.Set("email", "carol@example.org")
		r := s.send(newPost("/register", form), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin", "Accept-Language": "ru"})
		if r.status != http.StatusSeeOther {
			t.Fatalf("registration: status %d\n%s", r.status, r.body)
		}
		letters := smtp.Letters()[before:]
		if len(letters) != 1 || letters[0].To != "carol@example.org" || letters[0].Subject != "Подтвердите адрес — FreshRSS" || !strings.Contains(letters[0].Body, "Здравствуйте, carol!") {
			t.Fatalf("letters after registering = %+v", letters)
		}
		link := mailedLink.FindString(letters[0].Body)
		if !strings.HasPrefix(link, "https://reader.example/validate-email?token=") {
			t.Fatalf("the link of the letter = %q", link)
		}

		// Until the link is followed the account leads to one page only.
		for _, target := range []string{"/", "/subscriptions", "/settings/display", "/stats"} {
			if a := s.get(target); a.status != http.StatusSeeOther || a.header.Get("Location") != "/validate-email" {
				t.Errorf("GET %s before confirming: status %d, Location %q", target, a.status, a.header.Get("Location"))
			}
		}
		if a := s.post("/subscriptions/categories", url.Values{"name": {"Mine"}}); a.status != http.StatusForbidden {
			t.Errorf("a form before confirming: status %d", a.status)
		}
		waiting := s.shown("/validate-email")
		if !strings.Contains(waiting, "carol@example.org") || !strings.Contains(waiting, `action="/validate-email/resend"`) {
			t.Errorf("the page that waits for the confirmation:\n%s", waiting)
		}
		s.shown("/settings/profile")
		if _, body := s.follow("/validate-email/resend", nil); !strings.Contains(notice(body), "отправлено ещё раз") || len(smtp.Letters())-before != 2 {
			t.Errorf("asking for the letter again: notice %q, %d letters", notice(body), len(smtp.Letters())-before)
		}
		// Another address gets a letter and a link of its own; the old link is dead.
		s.follow("/settings/profile", url.Values{"email": {"carol@example.net"}})
		letters = smtp.Letters()[before:]
		fresh := mailedLink.FindString(letters[len(letters)-1].Body)
		if len(letters) != 3 || letters[2].To != "carol@example.net" || fresh == link {
			t.Fatalf("letters after a change of the address = %+v", letters)
		}
		path := func(link string) string { return strings.TrimPrefix(link, "https://reader.example") }
		for _, target := range []string{path(link), "/validate-email?user=carol&token=wrong", "/validate-email?user=alice&token=" + url.QueryEscape(strings.SplitN(fresh, "token=", 2)[1]), "/validate-email?user=no%20body&token=x"} {
			if a := s.get(target); a.status != http.StatusNotFound {
				t.Errorf("GET %s: status %d, want 404", target, a.status)
			}
		}
		// The link works without the login it was asked for with.
		other := &site{t: t, db: s.db, h: s.h}
		if a := other.get(path(fresh)); a.status != http.StatusSeeOther || a.header.Get("Location") != "/" {
			t.Fatalf("following the link: status %d", a.status)
		}
		if a := s.get("/subscriptions"); a.status != http.StatusOK {
			t.Errorf("a page after confirming: status %d", a.status)
		}
		if a := s.get(path(fresh)); a.status != http.StatusNotFound {
			t.Errorf("the link followed twice: status %d", a.status)
		}
		// Administrators are not held up, and switch the rule on only with a
		// server to send letters through.
		s.cookies = nil
		s.asAlice()
		s.follow("/settings/profile", url.Values{"email": {"alice@example.org"}})
		if a := s.get("/"); a.status != http.StatusOK {
			t.Errorf("an administrator after a change of address: status %d", a.status)
		}
		s.h.mailer = nil
		a := s.post("/admin/authentication", url.Values{"auth_type": {"form"}, "force_email_validation": {"1"}, "api_enabled": {"1"}})
		if a.status != http.StatusBadRequest || !strings.Contains(a.body, "without an SMTP server") {
			t.Errorf("switching the confirmation on without a mailer: status %d", a.status)
		}
		s.cookies = nil
		if a := s.get("/register"); a.status != http.StatusForbidden || !strings.Contains(a.body, "cannot send the letter") {
			t.Errorf("GET /register where letters cannot be sent: status %d", a.status)
		}
	})
}

// newPost is a form as a browser sends it, for a test that adds headers.
func newPost(target string, form url.Values) *http.Request {
	return httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
}
