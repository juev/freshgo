package store

import (
	"context"
	"fmt"
)

const feedColumns = `id, url, kind, category_id, name, website, description, last_update,
	priority, path_entries, http_auth, error, ttl, attributes`

// CreateFeed adds a feed. A zero f.ID is replaced with a new identifier, a
// zero f.CategoryID with the default category.
func (s *Store) CreateFeed(ctx context.Context, f *Feed) error {
	if f.CategoryID == 0 {
		f.CategoryID = DefaultCategoryID
	}
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.assignID(ctx, f.UserID, seqFeed, &f.ID); err != nil {
			return err
		}
		_, err := tx.exec(ctx, `
			INSERT INTO feeds (user_id, `+feedColumns+`)
			VALUES (`+placeholders(15)+`)`,
			f.UserID, f.ID, f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate,
			f.Priority, f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes))
		if err != nil {
			return fmt.Errorf("store: create feed %q: %w", f.URL, err)
		}
		return nil
	})
}

// FeedByID returns a feed or ErrNotFound.
func (s *Store) FeedByID(ctx context.Context, userID, id int64) (*Feed, error) {
	f, err := scanFeed(userID, s.queryRow(ctx, `
		SELECT `+feedColumns+` FROM feeds WHERE user_id = ? AND id = ?`, userID, id))
	if err != nil {
		return nil, fmt.Errorf("store: feed %d: %w", id, err)
	}
	return f, nil
}

// Feeds returns the feeds of a user ordered by identifier.
func (s *Store) Feeds(ctx context.Context, userID int64) ([]*Feed, error) {
	rows, err := s.query(ctx, `
		SELECT `+feedColumns+` FROM feeds WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: feeds: %w", err)
	}
	feeds, err := collect(rows, func(sc scanner) (*Feed, error) { return scanFeed(userID, sc) })
	if err != nil {
		return nil, fmt.Errorf("store: feeds: %w", err)
	}
	return feeds, nil
}

func scanFeed(userID int64, sc scanner) (*Feed, error) {
	f := &Feed{UserID: userID}
	var attributes string
	err := sc.Scan(&f.ID, &f.URL, &f.Kind, &f.CategoryID, &f.Name, &f.Website, &f.Description, &f.LastUpdate,
		&f.Priority, &f.PathEntries, &f.HTTPAuth, &f.Error, &f.TTL, &attributes)
	if err != nil {
		return nil, err
	}
	f.Attributes = []byte(attributes)
	return f, nil
}
