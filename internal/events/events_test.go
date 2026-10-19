package events

import (
	"context"
	"testing"
	"time"
)

// waitReady fails unless s is signalled within a second.
func waitReady(t *testing.T, s *Subscription) {
	t.Helper()
	select {
	case <-s.Ready():
	case <-time.After(time.Second):
		t.Fatal("the subscription was not signalled")
	}
}

func notReady(t *testing.T, s *Subscription) {
	t.Helper()
	select {
	case <-s.Ready():
		t.Fatal("the subscription was signalled")
	default:
	}
}

func ended(s *Subscription) bool {
	select {
	case <-s.Done():
		return true
	default:
		return false
	}
}

func TestPublishReachesTheSubscriptionsOfTheUser(t *testing.T) {
	var b Broker
	ctx := t.Context()
	first, second, other := b.Subscribe(ctx, 1), b.Subscribe(ctx, 1), b.Subscribe(ctx, 2)
	b.Publish(1, Tree)
	for _, s := range []*Subscription{first, second} {
		waitReady(t, s)
		if got := s.Take(); got != Tree {
			t.Errorf("Take = %b, want %b", got, Tree)
		}
	}
	notReady(t, other)
	if got := other.Take(); got != 0 {
		t.Errorf("the other user took %b", got)
	}
}

func TestPublishDoesNotWaitForASlowSubscriber(t *testing.T) {
	var b Broker
	s := b.Subscribe(t.Context(), 1)
	published := make(chan struct{})
	go func() {
		// Nobody reads s meanwhile.
		for range 1000 {
			b.Publish(1, Tree)
		}
		b.Publish(1, Entries)
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish waited for the subscriber")
	}
	// What it missed comes as one signal with every kind.
	waitReady(t, s)
	if got := s.Take(); got != Tree|Entries {
		t.Errorf("Take = %b, want %b", got, Tree|Entries)
	}
	notReady(t, s)
}

func TestSubscriptionEndsWithItsContext(t *testing.T) {
	var b Broker
	ctx, cancel := context.WithCancel(t.Context())
	s := b.Subscribe(ctx, 1)
	cancel()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("the subscription did not end with its context")
	}
	b.mu.Lock()
	left := len(b.subs[1])
	b.mu.Unlock()
	if left != 0 {
		t.Errorf("%d subscriptions are left", left)
	}
	b.Publish(1, Tree)
	notReady(t, s)
}

func TestTooManySubscriptionsEndTheOldest(t *testing.T) {
	var b Broker
	var subs []*Subscription
	for range MaxPerUser + 2 {
		subs = append(subs, b.Subscribe(t.Context(), 1))
	}
	for i, s := range subs {
		if want := i < 2; ended(s) != want || s.Replaced() != want {
			t.Errorf("subscription %d ended: %v, replaced: %v, want %v", i, ended(s), s.Replaced(), want)
		}
	}
	other := b.Subscribe(t.Context(), 2)
	if ended(other) {
		t.Error("the subscriptions of another user count towards the limit")
	}
	b.Publish(1, Tree)
	notReady(t, subs[0])
	waitReady(t, subs[len(subs)-1])
}

func TestCloseEndsEverySubscription(t *testing.T) {
	var b Broker
	first, second := b.Subscribe(t.Context(), 1), b.Subscribe(t.Context(), 2)
	b.Close()
	if !ended(first) || !ended(second) {
		t.Error("Close left a subscription open")
	}
	if first.Replaced() {
		t.Error("Close is taken for newer subscriptions")
	}
	if late := b.Subscribe(t.Context(), 1); !ended(late) {
		t.Error("a subscription after Close is open")
	}
	// Ending one that has ended does nothing.
	first.Cancel()
}
