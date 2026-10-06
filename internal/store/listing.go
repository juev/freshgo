package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/search"
)

// ErrCursor is returned for a Listing.After that no listing gave out.
var ErrCursor = errors.New("store: not a place in a listing")

// Order is what a listing is sorted by. Entries equal in it go by identifier.
type Order int

const (
	// OrderAdded sorts by the time the entry was added, which is its identifier.
	OrderAdded Order = iota
	// OrderPublished sorts by the date of the entry.
	OrderPublished
	// OrderTitle and OrderFeed sort by the title and by the name of the feed,
	// byte by byte: capital letters before small ones, ASCII before the rest.
	OrderTitle
	OrderFeed
	// OrderRandom shuffles. The first page picks the shuffle, the following
	// ones keep it.
	OrderRandom
)

// Listing is a page of the entries of a user, as a reader pages through
// them: sorted one of several ways and continued from where the page before
// ended, so that entries arriving meanwhile neither repeat nor hide others.
type Listing struct {
	Set EntrySet
	// Read and Favorite, when set, keep the entries in that state.
	Read     *bool
	Favorite *bool
	// UnreadOrFavorite keeps the entries that are unread or starred.
	UnreadOrFavorite bool
	// Search, when set, keeps the entries the query matches.
	Search *search.Query
	Order  Order
	// Ascending lists the smallest first; it means nothing to OrderRandom.
	Ascending bool
	// After is what the page before returned as next; empty for the first page.
	After string
	// Limit is the size of the page; zero or less is no limit.
	Limit int
}

// shuffleMod is the prime the keys of OrderRandom are taken modulo: an entry
// stands at (identifier mod p)·seed mod p, with a seed from 1 to p-1.
const shuffleMod = 1_000_003

// place is where a page ended: the entry and what it was sorted by.
type place struct {
	ID   int64  `json:"i"`
	Num  int64  `json:"n,omitempty"`
	Text string `json:"t,omitempty"`
	Seed int64  `json:"s,omitempty"`
}

func (p place) String() string {
	data, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(data)
}

func parsePlace(after string) (place, error) {
	var p place
	data, err := base64.RawURLEncoding.DecodeString(after)
	if err == nil {
		err = json.Unmarshal(data, &p)
	}
	if err != nil || p.Seed < 0 || p.Seed >= shuffleMod {
		return place{}, ErrCursor
	}
	return p, nil
}

// feedNameOfEntry is the name of the feed of the entry of the outer query.
const feedNameOfEntry = `(SELECT f.name FROM feeds f WHERE f.user_id = entries.user_id AND f.id = entries.feed_id)`

// ListPage returns a page of a listing and, when entries follow it, what to
// give as After to get them.
func (s *Store) ListPage(ctx context.Context, userID int64, l Listing) (entries []*Entry, next string, err error) {
	var after place
	if l.After != "" {
		if after, err = parsePlace(l.After); err != nil {
			return nil, "", err
		}
	}

	// key is the expression entries are sorted by before the identifier.
	var (
		key  string
		text bool
		seed int64
	)
	switch l.Order {
	case OrderPublished:
		key = `published`
	case OrderTitle:
		key, text = `title`, true
	case OrderFeed:
		key, text = feedNameOfEntry, true
	case OrderRandom:
		if seed = after.Seed; seed == 0 {
			seed = 1 + rand.Int64N(shuffleMod-1)
		}
		key = fmt.Sprintf(`((id %% %d) * %d) %% %d`, shuffleMod, seed, shuffleMod)
		l.Ascending = true
	}
	if text && s.driver == config.DriverPostgres {
		// SQLite compares bytes; PostgreSQL would go by the locale of the database.
		key += ` COLLATE "C"`
	}

	columns := entryColumns
	if key != "" {
		columns += `, ` + key
	}
	query := `SELECT ` + columns + ` FROM entries WHERE user_id = ?`
	args := []any{userID}
	where, setArgs := l.Set.where(userID)
	query += where
	args = append(args, setArgs...)
	if l.Read != nil {
		query += ` AND is_read = ?`
		args = append(args, *l.Read)
	}
	if l.Favorite != nil {
		query += ` AND is_favorite = ?`
		args = append(args, *l.Favorite)
	}
	if l.UnreadOrFavorite {
		query += ` AND (is_read = ? OR is_favorite = ?)`
		args = append(args, false, true)
	}
	if l.Search != nil {
		var b strings.Builder
		args = writeCondition(&b, args, userID, l.Search.Condition())
		query += ` AND ` + b.String()
	}
	compare, direction := `<`, `DESC`
	if l.Ascending {
		compare, direction = `>`, `ASC`
	}
	if l.After != "" {
		switch {
		case key == "":
			query += ` AND id ` + compare + ` ?`
			args = append(args, after.ID)
		case text:
			query += ` AND (` + key + `, id) ` + compare + ` (?, ?)`
			args = append(args, after.Text, after.ID)
		default:
			query += ` AND (` + key + `, id) ` + compare + ` (?, ?)`
			args = append(args, after.Num, after.ID)
		}
	}
	query += ` ORDER BY `
	if key != "" {
		query += key + ` ` + direction + `, `
	}
	query += `id ` + direction
	// A search reads on until the page is full: how many rows that takes is
	// not known beforehand.
	if l.Limit > 0 && l.Search == nil {
		query += ` LIMIT ?`
		args = append(args, l.Limit+1)
	}

	var match func(*Entry) bool
	if l.Search != nil {
		if match, err = s.matcher(ctx, userID, l.Search); err != nil {
			return nil, "", fmt.Errorf("store: list page: %w", err)
		}
	}

	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	last := place{Seed: seed}
	for rows.Next() {
		at := place{Seed: seed}
		var sc scanner = rows
		switch {
		case text:
			sc = withColumn{rows, &at.Text}
		case key != "":
			sc = withColumn{rows, &at.Num}
		}
		e, err := scanEntry(userID, sc)
		if err != nil {
			return nil, "", fmt.Errorf("store: list page: %w", err)
		}
		if match != nil && !match(e) {
			continue
		}
		if l.Limit > 0 && len(entries) == l.Limit {
			return entries, last.String(), nil
		}
		at.ID = e.ID
		entries, last = append(entries, e), at
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list page: %w", err)
	}
	return entries, "", nil
}

// withColumn scans one more column than the scanner is asked for.
type withColumn struct {
	sc   scanner
	dest any
}

func (w withColumn) Scan(dest ...any) error {
	return w.sc.Scan(append(dest, w.dest)...)
}

// matcher returns the test of a query on a stored entry, with what the query
// needs to know besides the entry: the categories of the feeds and, when it
// asks for labels, the labels of the entries.
func (s *Store) matcher(ctx context.Context, userID int64, q *search.Query) (func(*Entry) bool, error) {
	feeds, err := s.Feeds(ctx, userID)
	if err != nil {
		return nil, err
	}
	categories := make(map[int64]int64, len(feeds))
	for _, f := range feeds {
		categories[f.ID] = f.CategoryID
	}
	labels := map[int64][]search.Label{}
	if q.UsesLabels() {
		rows, err := s.query(ctx, `
			SELECT et.entry_id, t.id, t.name FROM entry_tags et
			JOIN tags t ON t.user_id = et.user_id AND t.id = et.tag_id
			WHERE et.user_id = ?`, userID)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				entryID int64
				l       search.Label
			)
			if err := rows.Scan(&entryID, &l.ID, &l.Name); err != nil {
				return nil, err
			}
			labels[entryID] = append(labels[entryID], l)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return func(e *Entry) bool {
		return q.Match(&search.Entry{
			ID: e.ID, FeedID: e.FeedID, CategoryID: categories[e.FeedID], Title: e.Title, Authors: e.Authors,
			Content: e.Content, Link: e.Link, Tags: e.Tags, Published: e.Published, LastModified: e.LastModified,
			LastUserModified: e.LastUserModified, Labels: labels[e.ID],
		})
	}, nil
}

// labelOfEntry starts the test for a label on the entry of the outer query.
const labelOfEntry = `EXISTS (SELECT 1 FROM entry_tags t WHERE t.user_id = entries.user_id AND t.entry_id = entries.id`

// writeCondition writes a condition of a search as SQL on the entries table
// and returns the arguments with those of the condition added.
func writeCondition(b *strings.Builder, args []any, userID int64, c *search.Condition) []any {
	in := func(ids []int64) {
		b.WriteString(` IN (` + placeholders(len(ids)) + `)`)
		args = idArgs(ids, args...)
	}
	between := func(column string, scale int64) {
		b.WriteString(`(TRUE`)
		if c.Min != 0 {
			b.WriteString(` AND ` + column + ` >= ?`)
			args = append(args, c.Min*scale)
		}
		if c.Max != 0 {
			b.WriteString(` AND ` + column + ` <= ?`)
			args = append(args, c.Max*scale)
		}
		b.WriteString(`)`)
	}
	switch c.Kind {
	case search.CondAnd, search.CondOr:
		empty, operator := `TRUE`, ` AND `
		if c.Kind == search.CondOr {
			empty, operator = `FALSE`, ` OR `
		}
		if len(c.Of) == 0 {
			b.WriteString(empty)
			break
		}
		b.WriteString(`(`)
		for i, sub := range c.Of {
			if i > 0 {
				b.WriteString(operator)
			}
			args = writeCondition(b, args, userID, sub)
		}
		b.WriteString(`)`)
	case search.CondNot:
		b.WriteString(`NOT (`)
		args = writeCondition(b, args, userID, c.Of[0])
		b.WriteString(`)`)
	case search.CondEntries:
		b.WriteString(`id`)
		in(c.IDs)
	case search.CondFeeds:
		b.WriteString(`feed_id`)
		in(c.IDs)
	case search.CondCategories:
		b.WriteString(`feed_id IN (SELECT f.id FROM feeds f WHERE f.user_id = ? AND f.category_id`)
		args = append(args, userID)
		in(c.IDs)
		b.WriteString(`)`)
	case search.CondLabels:
		b.WriteString(labelOfEntry + ` AND t.tag_id`)
		in(c.IDs)
		b.WriteString(`)`)
	case search.CondAnyLabel:
		b.WriteString(labelOfEntry + `)`)
	case search.CondLabelNames:
		b.WriteString(labelOfEntry + ` AND t.tag_id IN (SELECT n.id FROM tags n WHERE n.user_id = ? AND n.name IN (` +
			placeholders(len(c.Names)) + `)))`)
		args = append(args, userID)
		for _, name := range c.Names {
			args = append(args, name)
		}
	case search.CondAdded:
		between(`id`, 1_000_000)
	case search.CondPublished:
		between(`published`, 1)
	case search.CondModified:
		between(`last_modified`, 1)
	case search.CondUserModified:
		between(`last_user_modified`, 1)
	default:
		panic("store: search condition of kind " + strconv.Itoa(int(c.Kind)))
	}
	return args
}

// UnreadFavorites returns the number of starred entries the user has not read.
func (s *Store) UnreadFavorites(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.queryRow(ctx, `
		SELECT COUNT(*) FROM entries WHERE user_id = ? AND is_favorite = ? AND is_read = ?`,
		userID, true, false).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: unread starred entries: %w", err)
	}
	return n, nil
}
