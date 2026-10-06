package store

import (
	"context"
	"fmt"
)

const feedColumns = `id, url, kind, category_id, name, website, description, last_update,
	priority, path_entries, http_auth, error, ttl, attributes, http_etag, http_last_modified, websub_topic`

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
			VALUES (`+placeholders(18)+`)`,
			f.UserID, f.ID, f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate,
			f.Priority, f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes), f.HTTPETag, f.HTTPLastModified,
			f.WebSubTopic)
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

// LockFeed returns the feed as it is stored now. Inside InTx it also makes
// other transactions that lock or update the same feed wait until this one
// ends, which is how refreshes of one feed are kept from overlapping.
func (s *Store) LockFeed(ctx context.Context, userID, id int64) (*Feed, error) {
	// A write that changes nothing takes the row lock on PostgreSQL; SQLite
	// has a single writer anyway.
	res, err := s.exec(ctx, `UPDATE feeds SET url = url WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return nil, fmt.Errorf("store: lock feed %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("store: lock feed %d: %w", id, err)
	} else if n == 0 {
		return nil, fmt.Errorf("store: lock feed %d: %w", id, ErrNotFound)
	}
	return s.FeedByID(ctx, userID, id)
}

// UpdateFeed replaces every field of a feed except its identifier. A missing
// feed gives ErrNotFound.
func (s *Store) UpdateFeed(ctx context.Context, f *Feed) error {
	res, err := s.exec(ctx, `
		UPDATE feeds SET url = ?, kind = ?, category_id = ?, name = ?, website = ?, description = ?,
			last_update = ?, priority = ?, path_entries = ?, http_auth = ?, error = ?, ttl = ?,
			attributes = ?, http_etag = ?, http_last_modified = ?, websub_topic = ?
		WHERE user_id = ? AND id = ?`,
		f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate, f.Priority,
		f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes), f.HTTPETag, f.HTTPLastModified,
		f.WebSubTopic, f.UserID, f.ID)
	if err != nil {
		return fmt.Errorf("store: update feed %d: %w", f.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update feed %d: %w", f.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: update feed %d: %w", f.ID, ErrNotFound)
	}
	return nil
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
		&f.Priority, &f.PathEntries, &f.HTTPAuth, &f.Error, &f.TTL, &attributes, &f.HTTPETag, &f.HTTPLastModified,
		&f.WebSubTopic)
	if err != nil {
		return nil, err
	}
	f.Attributes = []byte(attributes)
	return f, nil
}

// DeleteFeed removes a feed with its entries and its custom icon.
func (s *Store) DeleteFeed(ctx context.Context, userID, id int64) error {
	n, err := s.affected(ctx, `DELETE FROM feeds WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return fmt.Errorf("store: delete feed %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: delete feed %d: %w", id, ErrNotFound)
	}
	return nil
}

// FeedsByTopic returns the feeds of all users that announced the WebSub
// topic, ordered by user and identifier.
func (s *Store) FeedsByTopic(ctx context.Context, topic string) ([]*Feed, error) {
	rows, err := s.query(ctx, `
		SELECT user_id, `+feedColumns+` FROM feeds WHERE websub_topic = ? ORDER BY user_id, id`, topic)
	if err != nil {
		return nil, fmt.Errorf("store: feeds of topic %q: %w", topic, err)
	}
	feeds, err := collect(rows, func(sc scanner) (*Feed, error) {
		var userID int64
		f, err := scanFeed(0, prefixed{sc, &userID})
		if err != nil {
			return nil, err
		}
		f.UserID = userID
		return f, nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: feeds of topic %q: %w", topic, err)
	}
	return feeds, nil
}

// prefixed scans one leading column into first and the rest as asked.
type prefixed struct {
	sc    scanner
	first any
}

func (p prefixed) Scan(dest ...any) error {
	return p.sc.Scan(append([]any{p.first}, dest...)...)
}
