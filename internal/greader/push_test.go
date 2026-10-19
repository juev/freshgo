package greader

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/juev/freshgo/internal/push"
	"github.com/juev/freshgo/internal/store"
)

const pushKey = "BPublicKeyOfTheInstallation"

// appSubscriptionJSON is what an app posts for the endpoint, with keys of
// its own.
func appSubscriptionJSON(t *testing.T, endpoint string) string {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}}`, endpoint,
		base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(secret))
}

// pushing makes the API of the world one of an installation that pushes.
func (w *world) pushing() {
	w.t.Helper()
	w.handler = New(Options{DB: w.db, Hooks: w.registry, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PushKey: pushKey})
	w.server.Config.Handler = w.handler
}

func (w *world) pushSubscriptions(u *store.User) []*store.PushSubscription {
	w.t.Helper()
	subs, err := w.db.PushSubscriptions(context.Background(), u.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return subs
}

// Without VAPID keys there is no push to subscribe to.
func TestPushIsNotThereWithoutKeys(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		if status, _ := w.get(w.auth, PushPath); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", PushPath, status)
		}
		if status, _ := w.post(w.auth, PushPath+"/subscribe", appSubscriptionJSON(t, "https://push.example/a")); status != http.StatusNotFound {
			t.Errorf("subscribe = %d, want 404", status)
		}
		if subs := w.pushSubscriptions(w.alice); len(subs) != 0 {
			t.Errorf("subscriptions = %+v, want none", subs)
		}
	})
}

// An app with the token of the API learns the public key, subscribes as an
// app, subscribes again and takes its subscription back; another user's
// subscription is left alone.
func TestPushSubscribeWithToken(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.pushing()
		var key struct {
			Key string `json:"vapidPublicKey"`
		}
		w.json(PushPath, &key)
		if key.Key != pushKey {
			t.Errorf("public key = %q, want %q", key.Key, pushKey)
		}

		endpoint := "https://ntfy.example/up123"
		for range 2 {
			if status, body := w.post(w.auth, PushPath+"/subscribe", appSubscriptionJSON(t, endpoint)); status != http.StatusNoContent {
				t.Fatalf("subscribe = %d %q, want 204", status, body)
			}
		}
		subs := w.pushSubscriptions(w.alice)
		if len(subs) != 1 || subs[0].Endpoint != endpoint || !subs[0].App {
			t.Fatalf("subscriptions = %+v, want one app at %s", subs, endpoint)
		}

		// Under the path FreshRSS serves the API at as well.
		other := "https://ntfy.example/up456"
		if status, _ := w.post(w.auth, Alias+PushPath+"/subscribe", appSubscriptionJSON(t, other)); status != http.StatusNoContent {
			t.Errorf("subscribe under %s = %d, want 204", Alias, status)
		}

		bob := w.user("bob", "")
		bobs := w.login("bob")
		if status, _ := w.post(bobs, PushPath+"/unsubscribe", fmt.Sprintf(`{"endpoint":%q}`, endpoint)); status != http.StatusNoContent {
			t.Errorf("unsubscribe by bob = %d, want 204", status)
		}
		if n := len(w.pushSubscriptions(w.alice)); n != 2 {
			t.Errorf("bob took back a subscription of alice: %d left, want 2", n)
		}
		if status, _ := w.post(w.auth, PushPath+"/unsubscribe", fmt.Sprintf(`{"endpoint":%q}`, endpoint)); status != http.StatusNoContent {
			t.Errorf("unsubscribe = %d, want 204", status)
		}
		if subs := w.pushSubscriptions(w.alice); len(subs) != 1 || subs[0].Endpoint != other {
			t.Errorf("subscriptions after unsubscribing = %+v, want the other one", subs)
		}
		if n := len(w.pushSubscriptions(bob)); n != 0 {
			t.Errorf("bob has %d subscriptions, want none", n)
		}
	})
}

// Without the token, or with a wrong one, nothing is told or stored.
func TestPushWantsToken(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.pushing()
		for _, auth := range []string{"", "GoogleLogin auth=alice/wrong"} {
			status, _ := w.get(auth, PushPath)
			if status != http.StatusUnauthorized {
				t.Errorf("GET with %q = %d, want 401", auth, status)
			}
			status, _ = w.post(auth, PushPath+"/subscribe", appSubscriptionJSON(t, "https://push.example/a"))
			if status != http.StatusUnauthorized {
				t.Errorf("subscribe with %q = %d, want 401", auth, status)
			}
		}
		if subs := w.pushSubscriptions(w.alice); len(subs) != 0 {
			t.Errorf("subscriptions = %+v, want none", subs)
		}
	})
}

// What is not a subscription is refused, and apps share the bound of the
// browsers.
func TestPushRefusesWhatIsNotASubscriptionOfAnApp(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.pushing()
		for name, body := range map[string]string{
			"not JSON":   "endpoint=https://push.example/a",
			"http":       appSubscriptionJSON(t, "http://push.example/a"),
			"no keys":    `{"endpoint":"https://push.example/a"}`,
			"short auth": `{"endpoint":"https://push.example/a","keys":{"p256dh":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 65)) + `","auth":"AAAA"}}`,
		} {
			if status, _ := w.post(w.auth, PushPath+"/subscribe", body); status != http.StatusBadRequest {
				t.Errorf("%s: subscribe = %d, want 400", name, status)
			}
		}
		if status, _ := w.get(w.auth, PushPath+"/subscribe"); status != http.StatusBadRequest {
			t.Errorf("GET of subscribe = %d, want 400", status)
		}
		if status, _ := w.post(w.auth, PushPath+"/elsewhere", "{}"); status != http.StatusBadRequest {
			t.Errorf("POST elsewhere = %d, want 400", status)
		}

		for i := range push.MaxSubscriptions {
			if err := w.db.PutPush(context.Background(), &store.PushSubscription{
				UserID: w.alice.ID, Endpoint: fmt.Sprintf("https://push.example/%d", i), P256DH: "k", Auth: "s",
			}); err != nil {
				t.Fatal(err)
			}
		}
		if status, _ := w.post(w.auth, PushPath+"/subscribe", appSubscriptionJSON(t, "https://ntfy.example/up")); status != http.StatusConflict {
			t.Errorf("subscribe beyond the bound = %d, want 409", status)
		}
		// One of the ten subscribing again is no new one.
		if status, _ := w.post(w.auth, PushPath+"/subscribe", appSubscriptionJSON(t, "https://push.example/3")); status != http.StatusNoContent {
			t.Errorf("subscribe again at the bound = %d, want 204", status)
		}
	})
}
