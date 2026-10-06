package store

import (
	"context"
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
