package store

import (
	"context"
	"fmt"
)

// PushSubscription is a browser or an app of a user that asked to be told of
// new entries: where the push service takes a message for it, and the keys
// the message is encrypted with.
type PushSubscription struct {
	UserID int64
	// Endpoint is the address at the push service; it names one browser.
	Endpoint string
	// P256DH and Auth are the public key of the browser and its secret for
	// the encryption, as the browser gives them: base64url.
	P256DH  string
	Auth    string
	Created int64
	// App says an app subscribed through the API: it takes messages that
	// show nothing, a browser does not.
	App bool
}

// PutPush stores a subscription. The endpoint names a browser, so a browser
// that another user subscribes from becomes that user's, and its keys are
// replaced, as is whether it is an app.
func (s *Store) PutPush(ctx context.Context, p *PushSubscription) error {
	_, err := s.exec(ctx, `
		INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, created, app) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = excluded.user_id, p256dh = excluded.p256dh,
			auth = excluded.auth, created = excluded.created, app = excluded.app`,
		p.UserID, p.Endpoint, p.P256DH, p.Auth, p.Created, p.App)
	if err != nil {
		return fmt.Errorf("store: push subscription of user %d: %w", p.UserID, err)
	}
	return nil
}

// PushSubscriptions returns the subscriptions of a user, the oldest first.
func (s *Store) PushSubscriptions(ctx context.Context, userID int64) ([]*PushSubscription, error) {
	rows, err := s.query(ctx, `
		SELECT user_id, endpoint, p256dh, auth, created, app FROM push_subscriptions WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: push subscriptions of user %d: %w", userID, err)
	}
	subs, err := collect(rows, func(sc scanner) (*PushSubscription, error) {
		var p PushSubscription
		return &p, sc.Scan(&p.UserID, &p.Endpoint, &p.P256DH, &p.Auth, &p.Created, &p.App)
	})
	if err != nil {
		return nil, fmt.Errorf("store: push subscriptions of user %d: %w", userID, err)
	}
	return subs, nil
}

// DeletePush removes the subscription of the user at the endpoint; one that
// is not there, or belongs to another user, is left alone.
func (s *Store) DeletePush(ctx context.Context, userID int64, endpoint string) error {
	if _, err := s.exec(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint); err != nil {
		return fmt.Errorf("store: delete push subscription of user %d: %w", userID, err)
	}
	return nil
}
