package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/push"
)

const pushKey = "BPublicKeyOfTheInstallation"

// browserKeys returns the keys of a PushSubscription as a browser writes them.
func browserKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(secret)
}

// subscription is the JSON a browser posts for its PushSubscription.
func subscriptionJSON(t *testing.T, endpoint string) string {
	t.Helper()
	p256dh, auth := browserKeys(t)
	data, err := json.Marshal(map[string]any{"endpoint": endpoint, "expirationTime": nil, "keys": map[string]string{"p256dh": p256dh, "auth": auth}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// postJSON sends JSON the way the script does from a page of the interface.
func (s *site) postJSON(target, body string, header ...string) answer {
	s.t.Helper()
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	h := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	for i := 0; i+1 < len(header); i += 2 {
		h[header[i]] = header[i+1]
	}
	return s.send(r, h)
}

func (s *site) browsers(name string) []string {
	s.t.Helper()
	subs, err := s.db.PushSubscriptions(context.Background(), s.user(name).ID)
	if err != nil {
		s.t.Fatal(err)
	}
	var endpoints []string
	for _, p := range subs {
		endpoints = append(endpoints, p.Endpoint)
	}
	return endpoints
}

// Notifications are offered where the installation has the keys of Web Push:
// the profile has the button, with the key to subscribe with, the script
// knows where to post, and the service worker is served. Elsewhere none of it
// is there.
func TestPushIsOfferedWithKeys(t *testing.T) {
	imported(t, Options{PushKey: pushKey, BaseURL: "https://rss.example/reader"}, func(t *testing.T, s *site) {
		s.asAlice()
		page := s.shown("/settings/profile")
		if !strings.Contains(page, `id="push-toggle" data-key="`+pushKey+`"`) {
			t.Errorf("the profile does not offer notifications:\n%s", page)
		}
		for _, want := range []string{"/reader/settings/push", "/reader/settings/push/delete", "/reader/sw.js"} {
			if !strings.Contains(page, want) {
				t.Errorf("the script is not told %s:\n%s", want, page)
			}
		}
		a := s.get("/sw.js")
		if a.status != http.StatusOK || !strings.HasPrefix(a.header.Get("Content-Type"), "text/javascript") || a.header.Get("Cache-Control") != "no-cache" {
			t.Errorf("GET /sw.js: status %d, type %q, cache %q", a.status, a.header.Get("Content-Type"), a.header.Get("Cache-Control"))
		}
		for _, want := range []string{"'push'", "showNotification", "'notificationclick'", "openWindow"} {
			if !strings.Contains(a.body, want) {
				t.Errorf("the service worker has no %s:\n%s", want, a.body)
			}
		}
		// The page registers the worker only when the reader asks.
		if strings.Contains(page, "serviceWorker.register") {
			t.Error("the page registers the worker on its own")
		}
	})
}

func TestPushIsNotOfferedWithoutKeys(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.asAlice()
		page := s.shown("/settings/profile")
		for _, absent := range []string{"push-toggle", "/settings/push", "sw.js"} {
			if strings.Contains(page, absent) {
				t.Errorf("the profile of an installation without keys has %q:\n%s", absent, page)
			}
		}
		if a := s.get("/sw.js"); a.status != http.StatusNotFound {
			t.Errorf("GET /sw.js: status %d, want 404", a.status)
		}
		body := subscriptionJSON(t, "https://push.example/a")
		for _, target := range []string{"/settings/push", "/settings/push/delete"} {
			if a := s.postJSON(target, body); a.status != http.StatusNotFound {
				t.Errorf("POST %s: status %d, want 404", target, a.status)
			}
		}
		if got := s.browsers("alice"); len(got) != 0 {
			t.Errorf("a browser was stored: %v", got)
		}
	})
}

// A user subscribes the browser they sit at and takes it back; what one user
// does reaches the browsers of no other.
func TestPushSubscribeAndUnsubscribe(t *testing.T) {
	imported(t, Options{PushKey: pushKey}, func(t *testing.T, s *site) {
		clock := s.clock()
		alice, bob := "https://push.example/alice", "https://push.example/bob"
		s.asAlice()
		if a := s.postJSON("/settings/push", subscriptionJSON(t, alice)); a.status != http.StatusNoContent {
			t.Fatalf("subscribe: status %d, %s", a.status, a.body)
		}
		subs, err := s.db.PushSubscriptions(context.Background(), s.user("alice").ID)
		if err != nil || len(subs) != 1 || subs[0].Endpoint != alice || subs[0].Created != clock.Unix() {
			t.Fatalf("stored for alice: %+v, %v; want her browser, subscribed now", subs, err)
		}
		// The same browser again is the same row.
		if a := s.postJSON("/settings/push", subscriptionJSON(t, alice)); a.status != http.StatusNoContent || len(s.browsers("alice")) != 1 {
			t.Errorf("subscribe again: status %d, browsers %v", a.status, s.browsers("alice"))
		}

		s.login("bob", "bob-web-password", nil)
		if a := s.postJSON("/settings/push", subscriptionJSON(t, bob)); a.status != http.StatusNoContent {
			t.Fatalf("subscribe as bob: status %d", a.status)
		}
		// Bob cannot take back the browser of alice.
		if a := s.postJSON("/settings/push/delete", `{"endpoint":"`+alice+`"}`); a.status != http.StatusNoContent {
			t.Errorf("unsubscribe alice's browser as bob: status %d", a.status)
		}
		if got := s.browsers("alice"); len(got) != 1 {
			t.Errorf("bob took back alice's browser: %v", got)
		}
		if got := s.browsers("bob"); len(got) != 1 || got[0] != bob {
			t.Errorf("bob's browsers = %v", got)
		}

		s.asAlice()
		if a := s.postJSON("/settings/push/delete", `{"endpoint":"`+alice+`"}`); a.status != http.StatusNoContent {
			t.Errorf("unsubscribe: status %d", a.status)
		}
		if got := s.browsers("alice"); len(got) != 0 {
			t.Errorf("alice's browser stays after she took it back: %v", got)
		}
		if got := s.browsers("bob"); len(got) != 1 {
			t.Errorf("alice's unsubscribing touched bob's browsers: %v", got)
		}
	})
}

// What is not a subscription of a browser is refused, and nothing is stored.
func TestPushRefusesWhatIsNotASubscription(t *testing.T) {
	imported(t, Options{PushKey: pushKey}, func(t *testing.T, s *site) {
		s.asAlice()
		p256dh, auth := browserKeys(t)
		with := func(endpoint, p256dh, auth string) string {
			data, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": p256dh, "auth": auth}})
			return string(data)
		}
		for name, body := range map[string]string{
			"not JSON":                   "endpoint=https://push.example/a",
			"empty":                      "{}",
			"an address over http":       with("http://push.example/a", p256dh, auth),
			"an address without a host":  with("https:///a", p256dh, auth),
			"an address with a password": with("https://user:pass@push.example/a", p256dh, auth),
			"an address too long":        with("https://push.example/"+strings.Repeat("a", 2000), p256dh, auth),
			"no keys":                    `{"endpoint":"https://push.example/a"}`,
			"a key that is text":         with("https://push.example/a", "not a key!", auth),
			"a key of the wrong length":  with("https://push.example/a", auth, auth),
			"a secret of the wrong size": with("https://push.example/a", p256dh, p256dh),
			"more than a form may be":    strings.Repeat(" ", 5000) + with("https://push.example/a", p256dh, auth),
		} {
			if a := s.postJSON("/settings/push", body); a.status != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400", name, a.status)
			}
		}
		if got := s.browsers("alice"); len(got) != 0 {
			t.Errorf("a refused subscription was stored: %v", got)
		}
		if a := s.postJSON("/settings/push/delete", "nonsense"); a.status != http.StatusBadRequest {
			t.Errorf("unsubscribe with nonsense: status %d, want 400", a.status)
		}

		// A message goes to every browser, so their number is bounded.
		for i := range push.MaxSubscriptions {
			if a := s.postJSON("/settings/push", subscriptionJSON(t, "https://push.example/"+string(rune('a'+i)))); a.status != http.StatusNoContent {
				t.Fatalf("browser %d: status %d", i+1, a.status)
			}
		}
		if a := s.postJSON("/settings/push", subscriptionJSON(t, "https://push.example/one-more")); a.status != http.StatusConflict {
			t.Errorf("one browser over the bound: status %d, want 409", a.status)
		}
		// One that is subscribed already may renew its keys.
		if a := s.postJSON("/settings/push", subscriptionJSON(t, "https://push.example/a")); a.status != http.StatusNoContent {
			t.Errorf("a browser renewing its keys at the bound: status %d, want 204", a.status)
		}
	})
}

// Subscribing needs a login and a request from the pages of the interface.
func TestPushRoutesAreProtected(t *testing.T) {
	imported(t, Options{PushKey: pushKey}, func(t *testing.T, s *site) {
		body := subscriptionJSON(t, "https://push.example/a")
		if a := s.postJSON("/settings/push", body); a.status != http.StatusForbidden {
			t.Errorf("subscribe without a login: status %d, want 403", a.status)
		}
		s.asAlice()
		for name, header := range map[string][]string{
			"marked cross-site by the browser": {"Sec-Fetch-Site", "cross-site"},
			"with the origin of another site":  {"Sec-Fetch-Site", "", "Origin", "https://evil.example"},
		} {
			if a := s.postJSON("/settings/push", body, header...); a.status != http.StatusForbidden {
				t.Errorf("subscribe %s: status %d, want 403", name, a.status)
			}
			if a := s.postJSON("/settings/push/delete", body, header...); a.status != http.StatusForbidden {
				t.Errorf("unsubscribe %s: status %d, want 403", name, a.status)
			}
		}
		if got := s.browsers("alice"); len(got) != 0 {
			t.Errorf("a request from another site stored a browser: %v", got)
		}
	})
}
