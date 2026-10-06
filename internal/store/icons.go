package store

import (
	"context"
	"fmt"
)

// Icon is the icon of a site, looked up by a hash of where it is looked for.
type Icon struct {
	Hash string
	// Source is the address the search starts at: a site or an image.
	Source string
	// Content is the image, nil while none has been found.
	Content     []byte
	ContentType string
	// Modified is when Content last changed, Checked when Source was last
	// asked; both are zero until the first attempt.
	Modified int64
	Checked  int64
}

// PutIcon stores an icon, replacing the one with the same hash.
func (s *Store) PutIcon(ctx context.Context, icon *Icon) error {
	// A typed nil slice is not NULL for every driver; an untyped nil is.
	var content any
	if len(icon.Content) > 0 {
		content = icon.Content
	}
	_, err := s.exec(ctx, `
		INSERT INTO icons (hash, source, content, content_type, modified, checked) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (hash) DO UPDATE SET source = excluded.source, content = excluded.content,
			content_type = excluded.content_type, modified = excluded.modified, checked = excluded.checked`,
		icon.Hash, icon.Source, content, icon.ContentType, icon.Modified, icon.Checked)
	if err != nil {
		return fmt.Errorf("store: icon %s: %w", icon.Hash, err)
	}
	return nil
}

// Icon returns the icon with the given hash or ErrNotFound.
func (s *Store) Icon(ctx context.Context, hash string) (*Icon, error) {
	icon := &Icon{Hash: hash}
	err := s.queryRow(ctx, `
		SELECT source, content, content_type, modified, checked FROM icons WHERE hash = ?`, hash).
		Scan(&icon.Source, &icon.Content, &icon.ContentType, &icon.Modified, &icon.Checked)
	if err != nil {
		return nil, fmt.Errorf("store: icon %s: %w", hash, err)
	}
	return icon, nil
}

// CustomIcon is the icon a user chose for a feed.
type CustomIcon struct {
	// Hash is what the icon is served by.
	Hash    string
	Content []byte
	// Modified is when the user set the icon.
	Modified int64
}

// SetCustomIcon stores the icon a user chose for a feed, replacing the previous one.
func (s *Store) SetCustomIcon(ctx context.Context, userID, feedID int64, icon CustomIcon) error {
	_, err := s.exec(ctx, `
		INSERT INTO custom_icons (user_id, feed_id, hash, content, modified) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, feed_id) DO UPDATE SET hash = excluded.hash, content = excluded.content,
			modified = excluded.modified`, userID, feedID, icon.Hash, icon.Content, icon.Modified)
	if err != nil {
		return fmt.Errorf("store: custom icon of feed %d: %w", feedID, err)
	}
	return nil
}

// CustomIconByHash returns the custom icon served by the given hash or
// ErrNotFound.
func (s *Store) CustomIconByHash(ctx context.Context, hash string) (*CustomIcon, error) {
	icon := &CustomIcon{Hash: hash}
	// An empty hash is what rows written before icons were served have.
	err := s.queryRow(ctx, `
		SELECT content, modified FROM custom_icons WHERE hash = ? AND hash <> ''`, hash).
		Scan(&icon.Content, &icon.Modified)
	if err != nil {
		return nil, fmt.Errorf("store: custom icon %s: %w", hash, err)
	}
	return icon, nil
}
