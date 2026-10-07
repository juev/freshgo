package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

const entryColumns = `id, feed_id, guid, title, authors, content, link, published, last_seen,
	last_modified, last_user_modified, hash, is_read, is_favorite, tags, attributes`

// InsertEntries adds entries of one user in a single transaction. Entries
// with a zero ID get new identifiers, increasing in slice order and greater
// than any identifier the user already has; a non-zero ID (import) is kept.
// A guid already present in the feed gives ErrConflict and nothing is added.
func (s *Store) InsertEntries(ctx context.Context, userID int64, entries []*Entry) error {
	if len(entries) == 0 {
		return nil
	}
	return s.InTx(ctx, func(tx *Store) error {
		var maxGiven int64
		unassigned := 0
		for _, e := range entries {
			if e.ID == 0 {
				unassigned++
			} else if e.ID > maxGiven {
				maxGiven = e.ID
			}
		}
		if maxGiven != 0 {
			if err := tx.reserveID(ctx, userID, seqEntry, maxGiven); err != nil {
				return err
			}
		}
		var next int64
		if unassigned > 0 {
			first, err := tx.allocEntryIDs(ctx, userID, unassigned)
			if err != nil {
				return err
			}
			next = first
		}
		for _, e := range entries {
			e.UserID = userID
			if e.ID == 0 {
				e.ID = next
				next++
			}
			if err := tx.insertEntry(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) insertEntry(ctx context.Context, e *Entry) error {
	authors, err := jsonStrings(e.Authors)
	if err != nil {
		return fmt.Errorf("store: entry %q: authors: %w", e.GUID, err)
	}
	tags, err := jsonStrings(e.Tags)
	if err != nil {
		return fmt.Errorf("store: entry %q: tags: %w", e.GUID, err)
	}
	// A typed nil slice is not NULL for every driver; an untyped nil is.
	var hash any
	if len(e.Hash) > 0 {
		hash = e.Hash
	}
	_, err = s.exec(ctx, `
		INSERT INTO entries (user_id, `+entryColumns+`)
		VALUES (`+placeholders(17)+`)`,
		e.UserID, e.ID, e.FeedID, e.GUID, e.Title, authors, e.Content, e.Link, e.Published, e.LastSeen,
		e.LastModified, e.LastUserModified, hash, e.IsRead, e.IsFavorite, tags, jsonObject(e.Attributes))
	if err != nil {
		return fmt.Errorf("store: insert entry %q of feed %d: %w", e.GUID, e.FeedID, err)
	}
	return nil
}

// UpdateEntry replaces every field of an entry except its identifier, feed
// and guid. A missing entry gives ErrNotFound.
func (s *Store) UpdateEntry(ctx context.Context, e *Entry) error {
	authors, err := jsonStrings(e.Authors)
	if err != nil {
		return fmt.Errorf("store: entry %d: authors: %w", e.ID, err)
	}
	tags, err := jsonStrings(e.Tags)
	if err != nil {
		return fmt.Errorf("store: entry %d: tags: %w", e.ID, err)
	}
	var hash any
	if len(e.Hash) > 0 {
		hash = e.Hash
	}
	res, err := s.exec(ctx, `
		UPDATE entries SET title = ?, authors = ?, content = ?, link = ?, published = ?, last_seen = ?,
			last_modified = ?, last_user_modified = ?, hash = ?, is_read = ?, is_favorite = ?, tags = ?,
			attributes = ?
		WHERE user_id = ? AND id = ?`,
		e.Title, authors, e.Content, e.Link, e.Published, e.LastSeen, e.LastModified, e.LastUserModified,
		hash, e.IsRead, e.IsFavorite, tags, jsonObject(e.Attributes), e.UserID, e.ID)
	if err != nil {
		return fmt.Errorf("store: update entry %d: %w", e.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update entry %d: %w", e.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: update entry %d: %w", e.ID, ErrNotFound)
	}
	return nil
}

// SetEntryHash stores the change hash of an entry and nothing else.
func (s *Store) SetEntryHash(ctx context.Context, userID, id int64, hash []byte) error {
	if _, err := s.exec(ctx, `UPDATE entries SET hash = ? WHERE user_id = ? AND id = ?`, hash, userID, id); err != nil {
		return fmt.Errorf("store: hash of entry %d: %w", id, err)
	}
	return nil
}

// guidChunk bounds the number of guids in one query: both engines limit the
// number of parameters of a statement.
const guidChunk = 500

// EntryStates returns the state of the entries of a feed that have one of
// the given guids, by guid. Guids the feed does not have are absent.
func (s *Store) EntryStates(ctx context.Context, userID, feedID int64, guids []string) (map[string]EntryState, error) {
	states := make(map[string]EntryState, len(guids))
	for chunk := range slices.Chunk(guids, guidChunk) {
		rows, err := s.query(ctx, `
			SELECT guid, id, hash, is_read, is_favorite, last_user_modified FROM entries
			WHERE user_id = ? AND feed_id = ? AND guid IN (`+placeholders(len(chunk))+`)`,
			guidArgs(chunk, userID, feedID)...)
		if err != nil {
			return nil, fmt.Errorf("store: entry states of feed %d: %w", feedID, err)
		}
		for rows.Next() {
			var (
				guid string
				st   EntryState
			)
			if err := rows.Scan(&guid, &st.ID, &st.Hash, &st.IsRead, &st.IsFavorite, &st.LastUserModified); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("store: entry states of feed %d: %w", feedID, err)
			}
			states[guid] = st
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("store: entry states of feed %d: %w", feedID, err)
		}
	}
	return states, nil
}

// MarkEntriesSeen records that the feed still listed the entries with the
// given guids at the time at.
func (s *Store) MarkEntriesSeen(ctx context.Context, userID, feedID int64, guids []string, at int64) error {
	for chunk := range slices.Chunk(guids, guidChunk) {
		_, err := s.exec(ctx, `
			UPDATE entries SET last_seen = ?
			WHERE user_id = ? AND feed_id = ? AND guid IN (`+placeholders(len(chunk))+`)`,
			guidArgs(chunk, at, userID, feedID)...)
		if err != nil {
			return fmt.Errorf("store: last seen of feed %d: %w", feedID, err)
		}
	}
	return nil
}

// MarkEntriesSeenSince records at as the time the feed last listed the
// entries it listed at since or later: what an unchanged feed means for the
// entries seen at its previous refresh.
func (s *Store) MarkEntriesSeenSince(ctx context.Context, userID, feedID, since, at int64) error {
	_, err := s.exec(ctx, `
		UPDATE entries SET last_seen = ? WHERE user_id = ? AND feed_id = ? AND last_seen >= ?`,
		at, userID, feedID, since)
	if err != nil {
		return fmt.Errorf("store: last seen of feed %d: %w", feedID, err)
	}
	return nil
}

// guidArgs returns the query arguments: the leading ones, then the guids.
func guidArgs(guids []string, leading ...any) []any {
	args := make([]any, 0, len(leading)+len(guids))
	args = append(args, leading...)
	for _, g := range guids {
		args = append(args, g)
	}
	return args
}

// EntryByID returns an entry or ErrNotFound.
func (s *Store) EntryByID(ctx context.Context, userID, id int64) (*Entry, error) {
	e, err := scanEntry(userID, s.queryRow(ctx, `
		SELECT `+entryColumns+` FROM entries WHERE user_id = ? AND id = ?`, userID, id))
	if err != nil {
		return nil, fmt.Errorf("store: entry %d: %w", id, err)
	}
	return e, nil
}

// EntriesByFeed returns the entries of a feed ordered by identifier.
func (s *Store) EntriesByFeed(ctx context.Context, userID, feedID int64) ([]*Entry, error) {
	rows, err := s.query(ctx, `
		SELECT `+entryColumns+` FROM entries WHERE user_id = ? AND feed_id = ? ORDER BY id`, userID, feedID)
	if err != nil {
		return nil, fmt.Errorf("store: entries of feed %d: %w", feedID, err)
	}
	entries, err := collect(rows, func(sc scanner) (*Entry, error) { return scanEntry(userID, sc) })
	if err != nil {
		return nil, fmt.Errorf("store: entries of feed %d: %w", feedID, err)
	}
	return entries, nil
}

func scanEntry(userID int64, sc scanner) (*Entry, error) {
	e := &Entry{UserID: userID}
	var authors, tags, attributes string
	err := sc.Scan(&e.ID, &e.FeedID, &e.GUID, &e.Title, &authors, &e.Content, &e.Link, &e.Published, &e.LastSeen,
		&e.LastModified, &e.LastUserModified, &e.Hash, &e.IsRead, &e.IsFavorite, &tags, &attributes)
	if err != nil {
		return nil, err
	}
	if e.Authors, err = parseStrings(authors); err != nil {
		return nil, fmt.Errorf("authors: %w", err)
	}
	if e.Tags, err = parseStrings(tags); err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}
	e.Attributes = []byte(attributes)
	return e, nil
}

// CountEntries returns the number of entries of a user.
func (s *Store) CountEntries(ctx context.Context, userID int64) (int, error) {
	var n int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM entries WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count entries: %w", err)
	}
	return n, nil
}

// Retention says which entries of a feed DeleteOldEntries removes. An entry
// goes when one of the two rules, SeenBefore and KeepMax, asks for it and
// none of the Keep fields protects it. Entries the feed listed at its last
// refresh, those with the greatest last-seen time, always stay.
type Retention struct {
	// SeenBefore removes the entries the feed last listed before this time;
	// zero turns the rule off.
	SeenBefore int64
	// KeepMax removes the entries beyond the KeepMax listed most recently;
	// zero turns the rule off. Entries listed at the same time as the first
	// one beyond go with it.
	KeepMax int
	// KeepMin keeps at least this many of the entries listed most recently.
	KeepMin       int
	KeepFavorites bool
	// KeepLabeled keeps the entries that carry a user label.
	KeepLabeled bool
	KeepUnread  bool
}

// DeleteOldEntries removes the entries of a feed the retention rules give up
// and returns their number. The rules are those of
// FreshRSS_EntryDAO::cleanOldEntries.
func (s *Store) DeleteOldEntries(ctx context.Context, userID, feedID int64, keep Retention) (int, error) {
	if keep.SeenBefore == 0 && keep.KeepMax <= 0 {
		return 0, nil
	}
	// The last-seen time of the entry that n others were listed after.
	const nth = `(SELECT e.last_seen FROM entries e WHERE e.user_id = ? AND e.feed_id = ?
		ORDER BY e.last_seen DESC LIMIT 1 OFFSET ?)`
	query := `DELETE FROM entries WHERE user_id = ? AND feed_id = ?`
	args := []any{userID, feedID}
	if keep.KeepFavorites {
		query += ` AND NOT is_favorite`
	}
	if keep.KeepUnread {
		query += ` AND is_read`
	}
	if keep.KeepLabeled {
		query += ` AND NOT EXISTS (
			SELECT 1 FROM entry_tags t WHERE t.user_id = entries.user_id AND t.entry_id = entries.id)`
	}
	if keep.KeepMin > 0 {
		query += ` AND last_seen < ` + nth
		args = append(args, userID, feedID, keep.KeepMin)
	}
	query += ` AND last_seen < (SELECT MAX(e.last_seen) FROM entries e WHERE e.user_id = ? AND e.feed_id = ?)`
	args = append(args, userID, feedID)
	var rules []string
	if keep.SeenBefore != 0 {
		rules = append(rules, `last_seen < ?`)
		args = append(args, keep.SeenBefore)
	}
	if keep.KeepMax > 0 {
		rules = append(rules, `last_seen <= `+nth)
		args = append(args, userID, feedID, keep.KeepMax)
	}
	query += ` AND (` + strings.Join(rules, ` OR `) + `)`

	n, err := s.affected(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("store: delete old entries of feed %d: %w", feedID, err)
	}
	return n, nil
}

// MarkUnseenEntriesRead marks read the unread entries of a feed that it last
// listed before the given time, and returns their number.
func (s *Store) MarkUnseenEntriesRead(ctx context.Context, userID, feedID, before int64) (int, error) {
	n, err := s.affected(ctx, `
		UPDATE entries SET is_read = ? WHERE user_id = ? AND feed_id = ? AND NOT is_read AND last_seen < ?`,
		true, userID, feedID, before)
	if err != nil {
		return 0, fmt.Errorf("store: unseen entries of feed %d: %w", feedID, err)
	}
	return n, nil
}

// KeepNewestUnread marks read every unread entry of a feed except the keep
// newest, by identifier, and returns the number of entries it marked.
func (s *Store) KeepNewestUnread(ctx context.Context, userID, feedID int64, keep int) (int, error) {
	n, err := s.affected(ctx, `
		UPDATE entries SET is_read = ? WHERE user_id = ? AND feed_id = ? AND NOT is_read AND id <= (
			SELECT e.id FROM entries e WHERE e.user_id = ? AND e.feed_id = ? AND NOT e.is_read
			ORDER BY e.id DESC LIMIT 1 OFFSET ?)`,
		true, userID, feedID, userID, feedID, keep)
	if err != nil {
		return 0, fmt.Errorf("store: unread entries of feed %d: %w", feedID, err)
	}
	return n, nil
}

// affected runs a statement and returns the number of rows it changed.
func (s *Store) affected(ctx context.Context, query string, args ...any) (int, error) {
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// LatestFeedEntries returns the guid and title of the newest entries of a
// feed, by identifier, at most limit of them; a limit of zero means all.
func (s *Store) LatestFeedEntries(ctx context.Context, userID, feedID int64, limit int) ([]EntryKey, error) {
	keys, err := s.entryKeys(ctx, `
		SELECT guid, title FROM entries WHERE user_id = ? AND feed_id = ? ORDER BY id DESC`, limit, userID, feedID)
	if err != nil {
		return nil, fmt.Errorf("store: latest entries of feed %d: %w", feedID, err)
	}
	return keys, nil
}

// LatestCategoryEntries is LatestFeedEntries over all feeds of a category.
func (s *Store) LatestCategoryEntries(ctx context.Context, userID, categoryID int64, limit int) ([]EntryKey, error) {
	keys, err := s.entryKeys(ctx, `
		SELECT e.guid, e.title FROM entries e
		JOIN feeds f ON f.user_id = e.user_id AND f.id = e.feed_id
		WHERE e.user_id = ? AND f.category_id = ? ORDER BY e.id DESC`, limit, userID, categoryID)
	if err != nil {
		return nil, fmt.Errorf("store: latest entries of category %d: %w", categoryID, err)
	}
	return keys, nil
}

func (s *Store) entryKeys(ctx context.Context, query string, limit int, args ...any) ([]EntryKey, error) {
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (EntryKey, error) {
		var k EntryKey
		return k, sc.Scan(&k.GUID, &k.Title)
	})
}

// EntryFact is what statistics need to know of an entry.
type EntryFact struct {
	ID        int64
	FeedID    int64
	Published int64
	Read      bool
	Favorite  bool
}

// EntryFacts hands every entry of a user to each, in the order of their
// identifiers.
func (s *Store) EntryFacts(ctx context.Context, userID int64, each func(EntryFact)) error {
	rows, err := s.query(ctx, `
		SELECT id, feed_id, published, is_read, is_favorite FROM entries WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return fmt.Errorf("store: entry facts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f EntryFact
		if err := rows.Scan(&f.ID, &f.FeedID, &f.Published, &f.Read, &f.Favorite); err != nil {
			return fmt.Errorf("store: entry facts: %w", err)
		}
		each(f)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: entry facts: %w", err)
	}
	return nil
}
