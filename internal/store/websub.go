package store

import (
	"context"
	"fmt"
)

// WebSub is a subscription to the hub of the topic one feed announces. Its
// key and secret belong to that feed alone: a document pushed for it is
// stored into the feed and no other.
type WebSub struct {
	UserID int64
	FeedID int64
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

const webSubColumns = `user_id, feed_id, topic, hub, key, secret, lease_start, lease_end, error`

// PutWebSub stores a subscription, replacing the one of the same feed.
func (s *Store) PutWebSub(ctx context.Context, w *WebSub) error {
	_, err := s.exec(ctx, `
		INSERT INTO websub_subscriptions (`+webSubColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, feed_id) DO UPDATE SET topic = excluded.topic, hub = excluded.hub,
			key = excluded.key, secret = excluded.secret, lease_start = excluded.lease_start,
			lease_end = excluded.lease_end, error = excluded.error`,
		w.UserID, w.FeedID, w.Topic, w.Hub, w.Key, w.Secret, w.LeaseStart, w.LeaseEnd, w.Error)
	if err != nil {
		return fmt.Errorf("store: WebSub subscription of feed %d: %w", w.FeedID, err)
	}
	return nil
}

// WebSubByFeed returns the subscription of a feed or ErrNotFound.
func (s *Store) WebSubByFeed(ctx context.Context, userID, feedID int64) (*WebSub, error) {
	w, err := scanWebSub(s.queryRow(ctx, `
		SELECT `+webSubColumns+` FROM websub_subscriptions WHERE user_id = ? AND feed_id = ?`, userID, feedID))
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscription of feed %d: %w", feedID, err)
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

// WebSubsOfUser returns the subscriptions of a user's feeds, ordered by feed.
func (s *Store) WebSubsOfUser(ctx context.Context, userID int64) ([]*WebSub, error) {
	rows, err := s.query(ctx, `
		SELECT `+webSubColumns+` FROM websub_subscriptions WHERE user_id = ? ORDER BY feed_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscriptions of user %d: %w", userID, err)
	}
	subs, err := collect(rows, scanWebSub)
	if err != nil {
		return nil, fmt.Errorf("store: WebSub subscriptions of user %d: %w", userID, err)
	}
	return subs, nil
}

// DeleteWebSub removes the subscription of a feed.
func (s *Store) DeleteWebSub(ctx context.Context, userID, feedID int64) error {
	if _, err := s.exec(ctx, `DELETE FROM websub_subscriptions WHERE user_id = ? AND feed_id = ?`, userID, feedID); err != nil {
		return fmt.Errorf("store: delete WebSub subscription of feed %d: %w", feedID, err)
	}
	return nil
}

func scanWebSub(sc scanner) (*WebSub, error) {
	w := &WebSub{}
	if err := sc.Scan(&w.UserID, &w.FeedID, &w.Topic, &w.Hub, &w.Key, &w.Secret,
		&w.LeaseStart, &w.LeaseEnd, &w.Error); err != nil {
		return nil, err
	}
	return w, nil
}
