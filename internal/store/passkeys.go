package store

import (
	"context"
	"fmt"
)

// Passkey is a credential of WebAuthn that logs a user in.
type Passkey struct {
	ID     int64
	UserID int64
	// CredentialID is what the authenticator names the credential by; it is
	// unique among all users.
	CredentialID []byte
	// Name is what the user calls the passkey by.
	Name string
	// Credential is the credential as the library that verifies it writes
	// it, with the count of signatures it last saw.
	Credential []byte
	Created    int64
	// Used is when the passkey last logged in, zero when it never has.
	Used int64
}

// AddPasskey stores a passkey and sets its ID. A credential ID somebody
// already has gives ErrConflict.
func (s *Store) AddPasskey(ctx context.Context, p *Passkey) error {
	err := s.queryRow(ctx, `
		INSERT INTO passkeys (user_id, credential_id, name, credential, created, used) VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id`, p.UserID, p.CredentialID, p.Name, p.Credential, p.Created, p.Used).Scan(&p.ID)
	if err != nil {
		return fmt.Errorf("store: add passkey %q of user %d: %w", p.Name, p.UserID, err)
	}
	return nil
}

// Passkeys returns the passkeys of a user, the oldest first.
func (s *Store) Passkeys(ctx context.Context, userID int64) ([]*Passkey, error) {
	rows, err := s.query(ctx, `
		SELECT id, user_id, credential_id, name, credential, created, used
		FROM passkeys WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: passkeys of user %d: %w", userID, err)
	}
	passkeys, err := collect(rows, scanPasskey)
	if err != nil {
		return nil, fmt.Errorf("store: passkeys of user %d: %w", userID, err)
	}
	return passkeys, nil
}

// PasskeyByCredential returns the passkey with the credential ID, or
// ErrNotFound.
func (s *Store) PasskeyByCredential(ctx context.Context, credentialID []byte) (*Passkey, error) {
	p, err := scanPasskey(s.queryRow(ctx, `
		SELECT id, user_id, credential_id, name, credential, created, used
		FROM passkeys WHERE credential_id = ?`, credentialID))
	if err != nil {
		return nil, fmt.Errorf("store: passkey by credential: %w", err)
	}
	return p, nil
}

func scanPasskey(sc scanner) (*Passkey, error) {
	var p Passkey
	err := sc.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Name, &p.Credential, &p.Created, &p.Used)
	return &p, err
}

// UsePasskey records a login with the passkey: the credential as the
// library updated it, and when. A passkey that is gone is ErrNotFound.
func (s *Store) UsePasskey(ctx context.Context, id int64, credential []byte, used int64) error {
	n, err := s.affected(ctx, `UPDATE passkeys SET credential = ?, used = ? WHERE id = ?`, credential, used, id)
	if err != nil {
		return fmt.Errorf("store: use passkey %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: use passkey %d: %w", id, ErrNotFound)
	}
	return nil
}

// RenamePasskey gives the passkey of the user another name. A passkey that
// is not the user's, or is not there, is ErrNotFound.
func (s *Store) RenamePasskey(ctx context.Context, userID, id int64, name string) error {
	n, err := s.affected(ctx, `UPDATE passkeys SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return fmt.Errorf("store: rename passkey %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: rename passkey %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeletePasskey removes the passkey of the user. A passkey that is not the
// user's, or is not there, is ErrNotFound.
func (s *Store) DeletePasskey(ctx context.Context, userID, id int64) error {
	n, err := s.affected(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete passkey %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: delete passkey %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteUserPasskeys removes all the passkeys of a user.
func (s *Store) DeleteUserPasskeys(ctx context.Context, userID int64) error {
	if _, err := s.exec(ctx, `DELETE FROM passkeys WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete passkeys of user %d: %w", userID, err)
	}
	return nil
}
