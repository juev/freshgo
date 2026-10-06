package store

import (
	"context"
	"fmt"
)

// SetCustomIcon stores the icon a user chose for a feed, replacing the previous one.
func (s *Store) SetCustomIcon(ctx context.Context, userID, feedID int64, content []byte) error {
	_, err := s.exec(ctx, `
		INSERT INTO custom_icons (user_id, feed_id, content) VALUES (?, ?, ?)
		ON CONFLICT (user_id, feed_id) DO UPDATE SET content = excluded.content`, userID, feedID, content)
	if err != nil {
		return fmt.Errorf("store: custom icon of feed %d: %w", feedID, err)
	}
	return nil
}

// CustomIcon returns the icon a user chose for a feed or ErrNotFound.
func (s *Store) CustomIcon(ctx context.Context, userID, feedID int64) ([]byte, error) {
	var content []byte
	err := s.queryRow(ctx, `
		SELECT content FROM custom_icons WHERE user_id = ? AND feed_id = ?`, userID, feedID).Scan(&content)
	if err != nil {
		return nil, fmt.Errorf("store: custom icon of feed %d: %w", feedID, err)
	}
	return content, nil
}
