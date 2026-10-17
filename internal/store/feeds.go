package store

import (
	"context"
	"encoding/json"
	"fmt"
)

const feedColumns = `id, url, kind, category_id, name, website, description, last_update,
	priority, path_entries, http_auth, error, ttl, attributes, http_etag, http_last_modified, websub_topic, websub_hub, failures, not_before, error_message`

// CreateFeed adds a feed. A zero f.ID is replaced with a new identifier, a
// zero f.CategoryID with the default category, and attributes that are not
// there with the empty object that is stored for them: the feed is then what
// reading it back gives.
func (s *Store) CreateFeed(ctx context.Context, f *Feed) error {
	if f.CategoryID == 0 {
		f.CategoryID = DefaultCategoryID
	}
	f.Attributes = json.RawMessage(jsonObject(f.Attributes))
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.assignID(ctx, f.UserID, seqFeed, &f.ID); err != nil {
			return err
		}
		_, err := tx.exec(ctx, `
			INSERT INTO feeds (user_id, `+feedColumns+`)
			VALUES (`+placeholders(22)+`)`,
			f.UserID, f.ID, f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate,
			f.Priority, f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes), f.HTTPETag, f.HTTPLastModified,
			f.WebSubTopic, f.WebSubHub, f.Failures, f.NotBefore, f.ErrorMessage)
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
			attributes = ?, http_etag = ?, http_last_modified = ?, websub_topic = ?, websub_hub = ?,
			failures = ?, not_before = ?, error_message = ?
		WHERE user_id = ? AND id = ?`,
		f.URL, f.Kind, f.CategoryID, f.Name, f.Website, f.Description, f.LastUpdate, f.Priority,
		f.PathEntries, f.HTTPAuth, f.Error, f.TTL, jsonObject(f.Attributes), f.HTTPETag, f.HTTPLastModified,
		f.WebSubTopic, f.WebSubHub, f.Failures, f.NotBefore, f.ErrorMessage, f.UserID, f.ID)
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
		&f.WebSubTopic, &f.WebSubHub, &f.Failures, &f.NotBefore, &f.ErrorMessage)
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

// DeleteFeedEntries removes every entry of a feed, the feed stays. It
// returns how many entries there were.
func (s *Store) DeleteFeedEntries(ctx context.Context, userID, feedID int64) (int, error) {
	n, err := s.affected(ctx, `DELETE FROM entries WHERE user_id = ? AND feed_id = ?`, userID, feedID)
	if err != nil {
		return 0, fmt.Errorf("store: delete entries of feed %d: %w", feedID, err)
	}
	return n, nil
}

// Published says how much a feed has published within a stretch of time.
type Published struct {
	// Entries is the number of entries dated within the stretch.
	Entries int
	// Oldest is the date of the oldest of them.
	Oldest int64
}

// PublishedBetween returns, by feed, how much the feeds have published after
// since and not after until, for the feeds that have such entries.
func (s *Store) PublishedBetween(ctx context.Context, userID, since, until int64) (map[int64]Published, error) {
	rows, err := s.query(ctx, `
		SELECT feed_id, COUNT(*), MIN(published) FROM entries WHERE user_id = ? AND published > ? AND published <= ?
		GROUP BY feed_id`, userID, since, until)
	if err != nil {
		return nil, fmt.Errorf("store: published entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	published := map[int64]Published{}
	for rows.Next() {
		var (
			feedID int64
			p      Published
		)
		if err := rows.Scan(&feedID, &p.Entries, &p.Oldest); err != nil {
			return nil, fmt.Errorf("store: published entries: %w", err)
		}
		published[feedID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: published entries: %w", err)
	}
	return published, nil
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

// FailingFeed is a feed whose last refresh failed, with its user.
type FailingFeed struct {
	User string
	ID   int64
	URL  string
	// Since is when the last refresh failed, in Unix seconds.
	Since int64
}

// FailingFeeds returns the feeds of every user whose last refresh failed,
// by user and feed.
func (s *Store) FailingFeeds(ctx context.Context) ([]FailingFeed, error) {
	rows, err := s.query(ctx, `
		SELECT u.name, f.id, f.url, f.error FROM feeds f JOIN users u ON u.id = f.user_id
		WHERE f.error <> 0 ORDER BY u.name, f.id`)
	if err != nil {
		return nil, fmt.Errorf("store: failing feeds: %w", err)
	}
	return collect(rows, func(r scanner) (FailingFeed, error) {
		var f FailingFeed
		err := r.Scan(&f.User, &f.ID, &f.URL, &f.Since)
		return f, err
	})
}

// CountFeeds returns how many feeds all users have together.
func (s *Store) CountFeeds(ctx context.Context) (int, error) {
	var n int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM feeds`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count feeds: %w", err)
	}
	return n, nil
}
