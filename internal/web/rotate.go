package web

import (
	"time"

	"github.com/juev/freshgo/internal/store"
)

// successor is the login that takes the place of old when the password
// changes: a secret of its own, so that a copy of the old cookie is worth
// nothing, with the same choice of being remembered and the same time of the
// last password typed, so that U61 counts from when it did.
func successor(old *store.Session, now time.Time, life time.Duration) (*store.Session, string, error) {
	token, err := newToken()
	if err != nil {
		return nil, "", err
	}
	return &store.Session{
		TokenHash: tokenHash(token), UserID: old.UserID,
		Created: now.Unix(), Used: now.Unix(), Expires: now.Add(life).Unix(),
		Persistent: old.Persistent, Authenticated: old.Authenticated,
	}, token, nil
}
