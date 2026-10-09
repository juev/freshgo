package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
)

// EntrySet selects entries by where they come from; the conditions add up.
// The zero value is every entry of the user.
type EntrySet struct {
	// FeedID, CategoryID and LabelID, when not zero, keep the entries of the
	// feed, of the feeds of the category, of the label.
	FeedID     int64
	CategoryID int64
	LabelID    int64
	// MinPriority and BelowPriority bound the priority of the feed: at least
	// the first, less than the second.
	MinPriority   *int
	BelowPriority *int
	// OnlyFavorite keeps starred entries, Labeled those with a label, and
	// FavoriteOrLabeled those that are either.
	OnlyFavorite      bool
	Labeled           bool
	FavoriteOrLabeled bool
}

// anyLabel is true for an entry some label is attached to.
const anyLabel = `EXISTS (SELECT 1 FROM entry_tags t WHERE t.user_id = entries.user_id AND t.entry_id = entries.id)`

// where returns the condition on the entries table, starting with " AND",
// and its arguments.
func (set EntrySet) where(userID int64) (string, []any) {
	var (
		b    strings.Builder
		args []any
	)
	if set.FeedID != 0 {
		b.WriteString(` AND feed_id = ?`)
		args = append(args, set.FeedID)
	}
	if set.CategoryID != 0 || set.MinPriority != nil || set.BelowPriority != nil {
		b.WriteString(` AND feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = ?`)
		args = append(args, userID)
		if set.CategoryID != 0 {
			b.WriteString(` AND f.category_id = ?`)
			args = append(args, set.CategoryID)
		}
		if set.MinPriority != nil {
			b.WriteString(` AND f.priority >= ?`)
			args = append(args, *set.MinPriority)
		}
		if set.BelowPriority != nil {
			b.WriteString(` AND f.priority < ?`)
			args = append(args, *set.BelowPriority)
		}
		b.WriteString(`)`)
	}
	if set.LabelID != 0 {
		b.WriteString(` AND EXISTS (SELECT 1 FROM entry_tags t
			WHERE t.user_id = entries.user_id AND t.entry_id = entries.id AND t.tag_id = ?)`)
		args = append(args, set.LabelID)
	}
	if set.OnlyFavorite {
		// Compared, not bare: SQLite takes an index for a comparison only.
		b.WriteString(` AND is_favorite = TRUE`)
	}
	if set.Labeled {
		b.WriteString(` AND ` + anyLabel)
	}
	if set.FavoriteOrLabeled {
		b.WriteString(` AND (is_favorite OR ` + anyLabel + `)`)
	}
	return b.String(), args
}

// EntryQuery is a page of a stream of entries, ordered by identifier.
type EntryQuery struct {
	Set EntrySet
	// Read and Favorite, when set, keep the entries in that state.
	Read     *bool
	Favorite *bool
	// Since and Until are Unix seconds and follow the Google Reader API of
	// FreshRSS. Since keeps the entries added or changed by their feed at
	// that time or later. Until alone keeps those added and last changed at
	// that time or earlier; given together with Since it widens the result
	// instead of narrowing it: an entry passes when either bound accepts it.
	Since int64
	Until int64
	// Ascending lists the oldest first.
	Ascending bool
	// From, when not zero, is the identifier to start at, itself included.
	From int64
	// Limit bounds the number of entries; zero or less is no limit.
	Limit int
}

func (q EntryQuery) sql(userID int64, columns string) (string, []any) {
	query := `SELECT ` + columns + ` FROM entries WHERE user_id = ?`
	args := []any{userID}
	where, setArgs := q.Set.where(userID)
	query += where
	args = append(args, setArgs...)
	if q.Read != nil {
		query += ` AND is_read = ?`
		args = append(args, *q.Read)
	}
	if q.Favorite != nil {
		query += ` AND is_favorite = ?`
		args = append(args, *q.Favorite)
	}
	// An identifier is the time the entry was added, in microseconds.
	const second = 1_000_000
	switch {
	case q.Since != 0 && q.Until != 0:
		query += ` AND (id >= ? OR last_modified >= ? OR (id <= ? AND last_modified <= ?))`
		args = append(args, q.Since*second, q.Since, q.Until*second, q.Until)
	case q.Since != 0:
		query += ` AND (id >= ? OR last_modified >= ?)`
		args = append(args, q.Since*second, q.Since)
	case q.Until != 0:
		query += ` AND id <= ? AND last_modified <= ?`
		args = append(args, q.Until*second, q.Until)
	}
	order := `DESC`
	if q.Ascending {
		order = `ASC`
	}
	if q.From != 0 {
		if q.Ascending {
			query += ` AND id >= ?`
		} else {
			query += ` AND id <= ?`
		}
		args = append(args, q.From)
	}
	query += ` ORDER BY id ` + order
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	return query, args
}

// ListEntries returns the entries a query selects.
func (s *Store) ListEntries(ctx context.Context, userID int64, q EntryQuery) ([]*Entry, error) {
	query, args := q.sql(userID, entryColumns)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list entries: %w", err)
	}
	entries, err := collect(rows, func(sc scanner) (*Entry, error) { return scanEntry(userID, sc) })
	if err != nil {
		return nil, fmt.Errorf("store: list entries: %w", err)
	}
	return entries, nil
}

// ListEntryIDs returns the identifiers of the entries a query selects.
func (s *Store) ListEntryIDs(ctx context.Context, userID int64, q EntryQuery) ([]int64, error) {
	query, args := q.sql(userID, `id`)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list entry ids: %w", err)
	}
	ids, err := collect(rows, scanID)
	if err != nil {
		return nil, fmt.Errorf("store: list entry ids: %w", err)
	}
	return ids, nil
}

func scanID(sc scanner) (int64, error) {
	var id int64
	return id, sc.Scan(&id)
}

// idChunk bounds the number of identifiers in one statement, like guidChunk.
const idChunk = 500

// idArgs returns the query arguments: the leading ones, then the identifiers.
func idArgs(ids []int64, leading ...any) []any {
	args := make([]any, 0, len(leading)+len(ids))
	args = append(args, leading...)
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

// EntriesByIDs returns the entries with the given identifiers that exist,
// ordered by identifier.
func (s *Store) EntriesByIDs(ctx context.Context, userID int64, ids []int64, ascending bool) ([]*Entry, error) {
	var entries []*Entry
	for chunk := range slices.Chunk(ids, idChunk) {
		rows, err := s.query(ctx, `
			SELECT `+entryColumns+` FROM entries
			WHERE user_id = ? AND id IN (`+placeholders(len(chunk))+`)`, idArgs(chunk, userID)...)
		if err != nil {
			return nil, fmt.Errorf("store: entries by id: %w", err)
		}
		part, err := collect(rows, func(sc scanner) (*Entry, error) { return scanEntry(userID, sc) })
		if err != nil {
			return nil, fmt.Errorf("store: entries by id: %w", err)
		}
		entries = append(entries, part...)
	}
	slices.SortFunc(entries, func(a, b *Entry) int {
		if ascending {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(b.ID, a.ID)
	})
	// An identifier given twice may have come back from two chunks.
	return slices.CompactFunc(entries, func(a, b *Entry) bool { return a.ID == b.ID }), nil
}

// EntryLabels returns the names of the labels attached to the given entries,
// by entry identifier, in the order the labels were created.
func (s *Store) EntryLabels(ctx context.Context, userID int64, ids []int64) (map[int64][]string, error) {
	labels := map[int64][]string{}
	for chunk := range slices.Chunk(ids, idChunk) {
		rows, err := s.query(ctx, `
			SELECT et.entry_id, t.name FROM entry_tags et
			JOIN tags t ON t.user_id = et.user_id AND t.id = et.tag_id
			WHERE et.user_id = ? AND et.entry_id IN (`+placeholders(len(chunk))+`)
			ORDER BY t.id`, idArgs(chunk, userID)...)
		if err != nil {
			return nil, fmt.Errorf("store: labels of entries: %w", err)
		}
		type pair struct {
			id   int64
			name string
		}
		pairs, err := collect(rows, func(sc scanner) (pair, error) {
			var p pair
			return p, sc.Scan(&p.id, &p.name)
		})
		if err != nil {
			return nil, fmt.Errorf("store: labels of entries: %w", err)
		}
		for _, p := range pairs {
			labels[p.id] = append(labels[p.id], p.name)
		}
	}
	return labels, nil
}

// SetEntriesRead makes the given entries read or unread, records at as the
// time of the user's change on those whose state it changed, and returns
// their number.
func (s *Store) SetEntriesRead(ctx context.Context, userID int64, ids []int64, read bool, at int64) (int, error) {
	total := 0
	err := s.InTx(ctx, func(tx *Store) error {
		total = 0
		for chunk := range slices.Chunk(ids, idChunk) {
			n, err := tx.affected(ctx, `
				UPDATE entries SET is_read = ?, last_user_modified = ?
				WHERE user_id = ? AND is_read <> ? AND id IN (`+placeholders(len(chunk))+`)`,
				idArgs(chunk, read, at, userID, read)...)
			if err != nil {
				return fmt.Errorf("store: mark entries read: %w", err)
			}
			total += n
		}
		return nil
	})
	return total, err
}

// SetEntriesFavorite stars or unstars the given entries and records at as the
// time of the user's change on every one of them.
func (s *Store) SetEntriesFavorite(ctx context.Context, userID int64, ids []int64, favorite bool, at int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		for chunk := range slices.Chunk(ids, idChunk) {
			_, err := tx.exec(ctx, `
				UPDATE entries SET is_favorite = ?, last_user_modified = ?
				WHERE user_id = ? AND id IN (`+placeholders(len(chunk))+`)`,
				idArgs(chunk, favorite, at, userID)...)
			if err != nil {
				return fmt.Errorf("store: star entries: %w", err)
			}
		}
		return nil
	})
}

// MarkSetRead makes read the unread entries of a set whose identifier is at
// most maxID, records at as the time of the user's change and returns their
// number.
func (s *Store) MarkSetRead(ctx context.Context, userID int64, set EntrySet, maxID, at int64) (int, error) {
	where, args := set.where(userID)
	n, err := s.affected(ctx, `
		UPDATE entries SET is_read = ?, last_user_modified = ?
		WHERE user_id = ? AND NOT is_read AND id <= ?`+where,
		append([]any{true, at, userID, maxID}, args...)...)
	if err != nil {
		return 0, fmt.Errorf("store: mark entries read: %w", err)
	}
	return n, nil
}

// Counts is what the entries of a feed or of a label add up to.
type Counts struct {
	Unread int
	// Newest is the greatest entry identifier, zero when there is no entry.
	Newest int64
}

// FeedCounts returns the counts of the feeds that have entries, by feed.
func (s *Store) FeedCounts(ctx context.Context, userID int64) (map[int64]Counts, error) {
	counts, err := s.counts(ctx, `
		SELECT feed_id, SUM(CASE WHEN is_read THEN 0 ELSE 1 END), MAX(id)
		FROM entries WHERE user_id = ? GROUP BY feed_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: feed counts: %w", err)
	}
	return counts, nil
}

// A plain GROUP BY feed_id with "is_read = FALSE" will not do: SQLite then
// still reads the whole index, in the order of the feeds.
const unreadByFeed = `
	SELECT f.id, (SELECT COUNT(*) FROM entries e
		WHERE e.user_id = f.user_id AND e.feed_id = f.id AND e.is_read = FALSE)
	FROM feeds f WHERE f.user_id = ?`

// UnreadByFeed returns the number of unread entries of every feed that has
// one. It counts feed by feed, so that only the unread entries are read:
// FeedCounts reads every entry of the user for the newest of each feed.
func (s *Store) UnreadByFeed(ctx context.Context, userID int64) (map[int64]int, error) {
	rows, err := s.query(ctx, unreadByFeed, userID)
	if err != nil {
		return nil, fmt.Errorf("store: unread by feed: %w", err)
	}
	defer func() { _ = rows.Close() }()
	unread := map[int64]int{}
	for rows.Next() {
		var (
			id int64
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("store: unread by feed: %w", err)
		}
		if n > 0 {
			unread[id] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: unread by feed: %w", err)
	}
	return unread, nil
}

// LabelCounts returns the counts of the labels attached to entries, by label.
func (s *Store) LabelCounts(ctx context.Context, userID int64) (map[int64]Counts, error) {
	counts, err := s.counts(ctx, `
		SELECT et.tag_id, SUM(CASE WHEN e.is_read THEN 0 ELSE 1 END), MAX(e.id)
		FROM entry_tags et
		JOIN entries e ON e.user_id = et.user_id AND e.id = et.entry_id
		WHERE et.user_id = ? GROUP BY et.tag_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: label counts: %w", err)
	}
	return counts, nil
}

func (s *Store) counts(ctx context.Context, query string, userID int64) (map[int64]Counts, error) {
	rows, err := s.query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[int64]Counts{}
	for rows.Next() {
		var (
			id int64
			c  Counts
		)
		if err := rows.Scan(&id, &c.Unread, &c.Newest); err != nil {
			return nil, err
		}
		counts[id] = c
	}
	return counts, rows.Err()
}
