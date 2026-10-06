package store

import (
	"context"
	"fmt"
)

// WebSub is a subscription to the hub of a topic. It is shared by every
// feed, of any user, that announces the topic.
type WebSub struct {
	// Topic is the address the feed gives as its own.
	Topic string
	Hub   string
	// Key is the end of the address the hub calls back; Secret signs what
	// the hub sends there.
	Key    string
	Secret string
	// LeaseStart is when the hub was last asked or last confirmed, LeaseEnd
	// when the subscription runs out; zero means not confirmed yet, or
	// confirmed without an end.
	LeaseStart int64
	LeaseEnd   int64
	// Error says pushes cannot be relied on: none has arrived yet, the hub
	// refused, or a poll found an entry the hub did not push.
	Error bool
}

const webSubColumns = `topic, hub, key, secret, lease_start, lease_end, error`

// PutWebSub stores a subscription, replacing the one of the same topic.
func (s *Store) PutWebSub(ctx context.Context, w *WebSub) error {
	_, err := s.exec(ctx, `
		INSERT INTO websub_subscriptions (`+webSubColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (topic) DO UPDATE SET hub = excluded.hub, key = excluded.key, secret = excluded.secret,
			lease_start = excluded.lease_start, lease_end = excluded.lease_end, error = excluded.error`,
		w.Topic, w.Hub, w.Key, w.Secret, w.LeaseStart, w.LeaseEnd, w.Error)
	if err != nil {
		return fmt.Errorf("store: WebSub subscription %q: %w", w.Topic, err)
	}
	return nil
}

// WebSubByTopic returns the subscription of a topic or ErrNotFound.
func (s *Store) WebSubByTopic(ctx context.Context, topic string) (*WebSub, error) {
	w, err := scanWebSub(s.queryRow(ctx, `SELECT `+webSubColumns+` FROM websub_subscriptions WHERE topic = ?`, topic))
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscription %q: %w", topic, err)
	}
	return w, nil
}

// WebSubByKey returns the subscription a callback address belongs to or
// ErrNotFound.
func (s *Store) WebSubByKey(ctx context.Context, key string) (*WebSub, error) {
	w, err := scanWebSub(s.queryRow(ctx, `SELECT `+webSubColumns+` FROM websub_subscriptions WHERE key = ?`, key))
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscription by key: %w", err)
	}
	return w, nil
}

// WebSubs returns every subscription, ordered by topic.
func (s *Store) WebSubs(ctx context.Context) ([]*WebSub, error) {
	rows, err := s.query(ctx, `SELECT `+webSubColumns+` FROM websub_subscriptions ORDER BY topic`)
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscriptions: %w", err)
	}
	subs, err := collect(rows, scanWebSub)
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscriptions: %w", err)
	}
	return subs, nil
}

// DeleteWebSub removes the subscription of a topic.
func (s *Store) DeleteWebSub(ctx context.Context, topic string) error {
	if _, err := s.exec(ctx, `DELETE FROM websub_subscriptions WHERE topic = ?`, topic); err != nil {
		return fmt.Errorf("store: delete WebSub subscription %q: %w", topic, err)
	}
	return nil
}

func scanWebSub(sc scanner) (*WebSub, error) {
	w := &WebSub{}
	if err := sc.Scan(&w.Topic, &w.Hub, &w.Key, &w.Secret, &w.LeaseStart, &w.LeaseEnd, &w.Error); err != nil {
		return nil, err
	}
	return w, nil
}
