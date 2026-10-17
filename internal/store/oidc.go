package store

import (
	"context"
	"fmt"
)

// OIDCUser returns the ID of the user the subject of the issuer belongs to,
// or ErrNotFound.
func (s *Store) OIDCUser(ctx context.Context, issuer, subject string) (int64, error) {
	var id int64
	err := s.queryRow(ctx, `SELECT user_id FROM oidc_identities WHERE issuer = ? AND subject = ?`, issuer, subject).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: user of subject %q at %q: %w", subject, issuer, err)
	}
	return id, nil
}

// BindOIDC gives the subject of the issuer to the user. A subject that
// belongs to somebody, or a user who has another subject there, gives
// ErrConflict.
func (s *Store) BindOIDC(ctx context.Context, issuer, subject string, userID int64) error {
	_, err := s.exec(ctx, `INSERT INTO oidc_identities (issuer, subject, user_id) VALUES (?, ?, ?)`, issuer, subject, userID)
	if err != nil {
		return fmt.Errorf("store: give subject %q at %q to user %d: %w", subject, issuer, userID, err)
	}
	return nil
}
