package store

import (
	"context"
	"fmt"
	"slices"
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
