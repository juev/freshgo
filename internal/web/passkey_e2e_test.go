//go:build e2e

package web

import (
	"context"
	"net"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/cdproto/webauthn"
	"github.com/chromedp/chromedp"

	"github.com/juev/freshgo/internal/store"
)

// TestE2EPasskeys drives a real Chrome with a virtual authenticator through
// what the script does and the tests of the handlers only imitate: the
// options the server gives are taken by navigator.credentials, the answer
// is what the browser makes of them, and the server accepts it.
func TestE2EPasskeys(t *testing.T) {
	// The server has to know the address the browser reaches it at before it
	// answers anything, so the listener comes first. Chrome treats
	// localhost as a secure origin, so passkeys work over plain HTTP there.
	server := httptest.NewUnstartedServer(nil)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	base := "http://localhost:" + strconv.Itoa(port)
	imported(t, Options{BaseURL: base}, func(t *testing.T, s *site) {
		server.Config.Handler = s.h
		server.Start()
		t.Cleanup(server.Close)

		options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(1280, 900))
		allocator, cancelAllocator := chromedp.NewExecAllocator(context.Background(), options...)
		t.Cleanup(cancelAllocator)
		chrome, cancel := chromedp.NewContext(allocator)
		t.Cleanup(cancel)
		t.Cleanup(func() {
			if err := chromedp.Cancel(chrome); err != nil {
				t.Logf("Chrome does not close: %v", err)
			}
		})
		if err := chromedp.Do(chrome); err != nil {
			t.Fatalf("Chrome does not start: %v", err)
		}
		ctx, cancelTimeout := context.WithTimeout(chrome, 2*time.Minute)
		t.Cleanup(cancelTimeout)
		b := &browser{t: t, s: s, ctx: ctx, base: base}

		enabled := true
		b.run(
			command(webauthn.Enable, webauthn.EnableParams{EnableUI: new(bool)}),
			command(webauthn.AddVirtualAuthenticator, webauthn.AddVirtualAuthenticatorParams{Options: &webauthn.VirtualAuthenticatorOptions{
				Protocol: webauthn.AuthenticatorProtocolCtap2, Transport: webauthn.AuthenticatorTransportInternal,
				HasResidentKey: enabled, HasUserVerification: enabled, IsUserVerified: enabled, AutomaticPresenceSimulation: enabled,
			}}),
		)

		// Adding a passkey in the profile.
		b.login("alice")
		b.open("/settings/profile")
		b.until("the form of a passkey", `document.getElementById('passkey-add') && !document.getElementById('passkey-add').hidden && document.getElementById('passkey-needs-browser').hidden`)
		b.run(chromedp.SendKeys("#passkey-new", "virtual key"), chromedp.Click("#passkey-register"))
		b.until("the passkey listed", `document.querySelector('table.data') && document.querySelector('input[name=name][value="virtual key"]')`)
		if got := s.passkeys("alice"); len(got) != 1 || got[0].Name != "virtual key" {
			t.Fatalf("stored = %+v", got)
		}
		if b.text(`document.getElementById('passkey-failed').hidden`) != "true" {
			t.Error("the page says the passkey failed")
		}

		// Logging in with it, from the login page.
		b.run(chromedp.Click(".account button"))
		b.until("the login page", `location.pathname === '/login'`)
		b.until("the button of the passkey", `document.getElementById('passkey-signin') && !document.getElementById('passkey-signin').hidden`)
		b.run(chromedp.Click("#passkey-signin button"))
		b.until("the reading screen after the passkey", `location.pathname === '/' && document.querySelector('.entries')`)
		if got := b.text(`document.querySelector('.account-name').textContent`); got != "alice" {
			t.Errorf("logged in as %q", got)
		}
		if got := s.passkeys("alice")[0]; got.Used == 0 {
			t.Error("the passkey has no time of use")
		}

		// With logins by password off, only the passkey is left on the page.
		s.system(func(sys *store.System) { sys.PasswordLoginOff = true })
		b.run(chromedp.Click(".account button"))
		b.until("the login page without a password", `location.pathname === '/login' && !document.getElementById('password') && document.getElementById('passkey-signin') && !document.getElementById('passkey-signin').hidden && document.getElementById('passkey-signin-needs-browser').hidden`)
		b.run(chromedp.Click("#remember"), chromedp.Click("#passkey-signin button"))
		b.until("the reading screen after the passkey", `location.pathname === '/' && document.querySelector('.entries')`)

		// An old login answers /reauth with the passkey.
		later := time.Now().Add(time.Hour)
		s.h.now = func() time.Time { return later }
		b.open("/admin/users")
		b.until("the page that asks again", `location.pathname === '/reauth' && !document.getElementById('password') && document.getElementById('passkey-signin') && !document.getElementById('passkey-signin').hidden`)
		b.run(chromedp.Click("#passkey-signin button"))
		b.until("the administration", `location.pathname === '/admin/users'`)
	})
}
