// Package events tells the open pages of a user that something of theirs
// changed, so that they fetch it again: entries came, or entries were read
// or starred. An event carries no data that cannot be fetched again, so
// that one a page had no time for is merged into the next.
package events

import (
	"context"
	"slices"
	"sync"
)

// MaxPerUser is how many subscriptions a user may have at a time; a new
// one beyond it ends the oldest.
const MaxPerUser = 8

// Kind is what changed, as a set of bits.
type Kind uint8

const (
	// Tree means the counts of the tree may have changed.
	Tree Kind = 1 << iota
	// Entries means entries were added: the tree changed too.
	Entries
	// Session means a login of the user ended, and the subscriber is to
	// look whether it was its own.
	Session
)

// Broker hands what is published for a user to the subscriptions of that
// user. It is safe for concurrent use; the zero value is ready.
type Broker struct {
	mu     sync.Mutex
	subs   map[int64][]*Subscription
	closed bool
}

// Subscription receives what is published for one user.
type Subscription struct {
	b    *Broker
	user int64
	// ready has room for one signal: a publish that finds it full has
	// nothing to add but its kind.
	ready chan struct{}
	done  chan struct{}
	// pending is what was published since the last Take; mu of the broker
	// guards it.
	pending Kind
	ended   bool
	// replaced is set when newer subscriptions of the user ended this one.
	replaced bool
}

// Subscribe returns a subscription to what is published for user, which
// ends with ctx, with Close, or when the user has subscribed MaxPerUser
// times since. After Close it is ended from the start.
func (b *Broker) Subscribe(ctx context.Context, user int64) *Subscription {
	s := &Subscription{b: b, user: user, ready: make(chan struct{}, 1), done: make(chan struct{})}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		s.end()
		return s
	}
	if b.subs == nil {
		b.subs = map[int64][]*Subscription{}
	}
	subs := append(b.subs[user], s)
	if len(subs) > MaxPerUser {
		for _, old := range subs[:len(subs)-MaxPerUser] {
			old.replaced = true
			old.end()
		}
		subs = slices.Clone(subs[len(subs)-MaxPerUser:])
	}
	b.subs[user] = subs
	context.AfterFunc(ctx, s.Cancel)
	return s
}

// Publish tells the subscriptions of user that kind changed. It never waits.
func (b *Broker) Publish(user int64, kind Kind) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs[user] {
		s.pending |= kind
		select {
		case s.ready <- struct{}{}:
		default:
		}
	}
}

// Close ends every subscription, and those made after it.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, subs := range b.subs {
		for _, s := range subs {
			s.end()
		}
	}
	b.subs = nil
}

// Ready is signalled when something was published since the last Take.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Done is closed when the subscription ends.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Replaced reports whether the subscription ended because the user made
// MaxPerUser newer ones: one made again would end another.
func (s *Subscription) Replaced() bool {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.replaced
}

// Take returns what was published since it was last called.
func (s *Subscription) Take() Kind {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	kind := s.pending
	s.pending = 0
	return kind
}

// Cancel ends the subscription.
func (s *Subscription) Cancel() {
	b := s.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.ended {
		return
	}
	s.end()
	subs := slices.DeleteFunc(b.subs[s.user], func(one *Subscription) bool { return one == s })
	if len(subs) == 0 {
		delete(b.subs, s.user)
	} else {
		b.subs[s.user] = subs
	}
}

// end closes done once; mu of the broker is held.
func (s *Subscription) end() {
	if !s.ended {
		s.ended = true
		close(s.done)
	}
}
