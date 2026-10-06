package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// Names of installation-wide settings.
const (
	// SettingSalt is the secret mixed into Google Reader API tokens. Import
	// carries it over from FreshRSS so that issued tokens stay valid.
	SettingSalt = "salt"
	// SettingForceHTTPS lists, one per line, the domains whose http:// links
	// are rewritten to https:// in addition to the built-in list.
	SettingForceHTTPS = "force_https"
)

// Setting returns an installation-wide value or ErrNotFound.
func (s *Store) Setting(ctx context.Context, name string) (string, error) {
	var value string
	if err := s.queryRow(ctx, `SELECT value FROM settings WHERE name = ?`, name).Scan(&value); err != nil {
		return "", fmt.Errorf("store: setting %q: %w", name, err)
	}
	return value, nil
}

// SetSetting stores an installation-wide value, replacing the previous one.
func (s *Store) SetSetting(ctx context.Context, name, value string) error {
	_, err := s.exec(ctx, `
		INSERT INTO settings (name, value) VALUES (?, ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value)
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", name, err)
	}
	return nil
}

// Salt returns the secret of the installation, making one up on first use.
func (s *Store) Salt(ctx context.Context) (string, error) {
	salt, err := s.Setting(ctx, SettingSalt)
	if !errors.Is(err, ErrNotFound) {
		return salt, err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("store: salt: %w", err)
	}
	// Whoever stores a salt first wins; everybody reads that one.
	_, err = s.exec(ctx, `
		INSERT INTO settings (name, value) VALUES (?, ?)
		ON CONFLICT (name) DO NOTHING`, SettingSalt, hex.EncodeToString(random))
	if err != nil {
		return "", fmt.Errorf("store: salt: %w", err)
	}
	return s.Setting(ctx, SettingSalt)
}
