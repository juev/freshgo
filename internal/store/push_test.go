package store

import (
	"context"
	"reflect"
	"testing"
)

// A browser is named by its endpoint: it is stored once, belongs to the user
// who subscribed from it last, and only that user removes it.
func TestPushSubscriptions(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		alice, bob := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		first := &PushSubscription{UserID: alice.ID, Endpoint: "https://push.example/a", P256DH: "k1", Auth: "s1", Created: 10}
		second := &PushSubscription{UserID: alice.ID, Endpoint: "https://push.example/b", P256DH: "k2", Auth: "s2", Created: 20, App: true}
		for _, p := range []*PushSubscription{first, second} {
			if err := s.PutPush(ctx, p); err != nil {
				t.Fatalf("PutPush: %v", err)
			}
		}
		got, err := s.PushSubscriptions(ctx, alice.ID)
		if err != nil || !reflect.DeepEqual(got, []*PushSubscription{first, second}) {
			t.Fatalf("PushSubscriptions(alice) = %+v, %v; want her two browsers, the oldest first", got, err)
		}
		if got, err := s.PushSubscriptions(ctx, bob.ID); err != nil || len(got) != 0 {
			t.Errorf("PushSubscriptions(bob) = %+v, %v; want none", got, err)
		}

		// The same browser subscribed again replaces its keys.
		again := &PushSubscription{UserID: alice.ID, Endpoint: first.Endpoint, P256DH: "k3", Auth: "s3", Created: 30}
		if err := s.PutPush(ctx, again); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.PushSubscriptions(ctx, alice.ID); len(got) != 2 || got[0].P256DH != "k3" || got[0].App || !got[1].App {
			t.Errorf("after subscribing again: %+v; want two, the first a browser with the new key, the second an app", got)
		}

		// Bob cannot remove alice's browser, but can take it over by
		// subscribing from it.
		if err := s.DeletePush(ctx, bob.ID, second.Endpoint); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.PushSubscriptions(ctx, alice.ID); len(got) != 2 {
			t.Errorf("bob's delete removed alice's browser: %+v", got)
		}
		// Subscribed from the interface, the app's endpoint is a browser's.
		moved := &PushSubscription{UserID: bob.ID, Endpoint: second.Endpoint, P256DH: "k4", Auth: "s4", Created: 40}
		if err := s.PutPush(ctx, moved); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.PushSubscriptions(ctx, alice.ID); len(got) != 1 {
			t.Errorf("alice still has the browser bob subscribed from: %+v", got)
		}
		if got, _ := s.PushSubscriptions(ctx, bob.ID); !reflect.DeepEqual(got, []*PushSubscription{moved}) {
			t.Errorf("bob's browsers = %+v, want %+v", got, moved)
		}

		if err := s.DeletePush(ctx, alice.ID, first.Endpoint); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.PushSubscriptions(ctx, alice.ID); len(got) != 0 {
			t.Errorf("alice's browser stays after she removed it: %+v", got)
		}
		// Gone with the user.
		if err := s.DeleteUser(ctx, bob.ID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.queryRow(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&n); err != nil || n != 0 {
			t.Errorf("rows left after the user was deleted: %d, %v", n, err)
		}
	})
}
