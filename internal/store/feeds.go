package store

import (
	"context"
	"fmt"
)

const feedColumns = `id, url, kind, category_id, name, website, description, last_update,
	priority, path_entries, http_auth, error, ttl, attributes, http_etag, http_last_modified, websub_topic, websub_hub`

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
			VALUES (`+placeholders(19)+`)`,
			f.UserID, f.ID, f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate,
			f.Priority, f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes), f.HTTPETag, f.HTTPLastModified,
			f.WebSubTopic, f.WebSubHub)
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
			attributes = ?, http_etag = ?, http_last_modified = ?, websub_topic = ?, websub_hub = ?
		WHERE user_id = ? AND id = ?`,
		f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate, f.Priority,
		f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes), f.HTTPETag, f.HTTPLastModified,
		f.WebSubTopic, f.WebSubHub, f.UserID, f.ID)
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
		&f.WebSubTopic, &f.WebSubHub)
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

// DeleteFeedEntries removes every entry of a feed, the feed stays. It
// returns how many entries there were.
func (s *Store) DeleteFeedEntries(ctx context.Context, userID, feedID int64) (int, error) {
	n, err := s.affected(ctx, `DELETE FROM entries WHERE user_id = ? AND feed_id = ?`, userID, feedID)
	if err != nil {
		return 0, fmt.Errorf("store: delete entries of feed %d: %w", feedID, err)
	}
	return n, nil
}

// NewestEntryDates returns, by feed, the date of the newest entry of the
// feeds that have entries.
func (s *Store) NewestEntryDates(ctx context.Context, userID int64) (map[int64]int64, error) {
	rows, err := s.query(ctx, `
		SELECT feed_id, MAX(published) FROM entries WHERE user_id = ? GROUP BY feed_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: newest entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	dates := map[int64]int64{}
	for rows.Next() {
		var feedID, date int64
		if err := rows.Scan(&feedID, &date); err != nil {
			return nil, fmt.Errorf("store: newest entries: %w", err)
		}
		dates[feedID] = date
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: newest entries: %w", err)
	}
	return dates, nil
}
