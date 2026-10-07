package store

import (
	"context"
	"fmt"
)

// Session is a login to the web interface.
type Session struct {
	// TokenHash is the hexadecimal SHA-256 of the secret in the cookie of
	// the browser.
	TokenHash string
	UserID    int64
	Created   int64
	// Used is when the session last carried a request, Expires when it
	// ends unless it is used before.
	Used    int64
	Expires int64
	// Persistent is a session that outlives the browser: the user asked
	// to be remembered.
	Persistent bool
	// Authenticated is when the user last typed the password.
	Authenticated int64
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, session *Session) error {
	_, err := s.exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, created, used, expires, persistent, authenticated)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.TokenHash, session.UserID, session.Created, session.Used, session.Expires,
		session.Persistent, session.Authenticated)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// Session returns the session with the token hash if it has not expired at
// now, otherwise ErrNotFound.
func (s *Store) Session(ctx context.Context, tokenHash string, now int64) (*Session, error) {
	session := &Session{}
	err := s.queryRow(ctx, `
		SELECT token_hash, user_id, created, used, expires, persistent, authenticated
		FROM sessions WHERE token_hash = ? AND expires > ?`, tokenHash, now).
		Scan(&session.TokenHash, &session.UserID, &session.Created, &session.Used, &session.Expires,
			&session.Persistent, &session.Authenticated)
	if err != nil {
		return nil, fmt.Errorf("store: session: %w", err)
	}
	return session, nil
}

// TouchSession records that a session was used and moves its end.
func (s *Store) TouchSession(ctx context.Context, tokenHash string, used, expires int64) error {
	_, err := s.exec(ctx, `UPDATE sessions SET used = ?, expires = ? WHERE token_hash = ?`, used, expires, tokenHash)
	if err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// DeleteSession ends a session; one that does not exist is not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.exec(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteUserSessions ends the sessions of a user except the one with the
// token hash keep, which may be empty.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keep string) error {
	if _, err := s.exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keep); err != nil {
		return fmt.Errorf("store: delete sessions of user %d: %w", userID, err)
	}
	return nil
}

// DeleteExpiredSessions removes the sessions that have ended by now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now int64) error {
	if _, err := s.exec(ctx, `DELETE FROM sessions WHERE expires <= ?`, now); err != nil {
		return fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return nil
}

// ConfirmSession records that the user of a session typed the password at
// the given time.
func (s *Store) ConfirmSession(ctx context.Context, tokenHash string, at int64) error {
	if _, err := s.exec(ctx, `UPDATE sessions SET authenticated = ? WHERE token_hash = ?`, at, tokenHash); err != nil {
		return fmt.Errorf("store: confirm session: %w", err)
	}
	return nil
}
