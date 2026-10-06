package store

import (
	"context"
	"fmt"
)

// Sequence names in the sequences table.
const (
	seqCategory = "category"
	seqFeed     = "feed"
	seqTag      = "tag"
	seqEntry    = "entry"
)

// Identifiers are never reused: a deleted feed must not hand its id, which
// API clients may still remember, to a new one. Hence a counter per user and
// kind instead of MAX(id)+1.

// assignID gives *id the next identifier of the sequence when it is zero.
// A non-zero *id (import) is kept, and the sequence is raised to it so that
// later identifiers do not collide.
func (s *Store) assignID(ctx context.Context, userID int64, name string, id *int64) error {
	if *id != 0 {
		return s.reserveID(ctx, userID, name, *id)
	}
	err := s.queryRow(ctx, `
		INSERT INTO sequences (user_id, name, value) VALUES (?, ?, 1)
		ON CONFLICT (user_id, name) DO UPDATE SET value = sequences.value + 1
		RETURNING value`, userID, name).Scan(id)
	if err != nil {
		return fmt.Errorf("store: next %s id: %w", name, err)
	}
	return nil
}

func (s *Store) reserveID(ctx context.Context, userID int64, name string, id int64) error {
	_, err := s.exec(ctx, `
		INSERT INTO sequences (user_id, name, value) VALUES (?, ?, ?)
		ON CONFLICT (user_id, name) DO UPDATE SET value =
			CASE WHEN sequences.value > excluded.value THEN sequences.value ELSE excluded.value END`,
		userID, name, id)
	if err != nil {
		return fmt.Errorf("store: reserve %s id: %w", name, err)
	}
	return nil
}

// allocEntryIDs returns the first of n consecutive entry identifiers. They
// are the current time in microseconds, moved forward when the clock has not
// advanced past the last issued identifier.
//
// The update locks the user's sequence row until the transaction ends, so
// transactions inserting entries for one user commit in identifier order: a
// client that has synchronized up to some id will not see a smaller one
// appear later.
func (s *Store) allocEntryIDs(ctx context.Context, userID int64, n int) (int64, error) {
	var last int64
	floor := s.now().UnixMicro() - 1 + int64(n)
	err := s.queryRow(ctx, `
		INSERT INTO sequences (user_id, name, value) VALUES (?, ?, ?)
		ON CONFLICT (user_id, name) DO UPDATE SET value =
			CASE WHEN sequences.value + ? > excluded.value THEN sequences.value + ? ELSE excluded.value END
		RETURNING value`, userID, seqEntry, floor, n, n).Scan(&last)
	if err != nil {
		return 0, fmt.Errorf("store: allocate entry ids: %w", err)
	}
	return last - int64(n) + 1, nil
}

// Counters holds the last identifier issued for each kind of object.
type Counters struct {
	Category int64
	Feed     int64
	Tag      int64
}

// RaiseCounters makes sure identifiers issued later for the user are greater
// than the given ones. Import uses it to carry over the counters of the
// source, which may be ahead of the largest identifier still in use.
func (s *Store) RaiseCounters(ctx context.Context, userID int64, c Counters) error {
	return s.InTx(ctx, func(tx *Store) error {
		for name, value := range map[string]int64{seqCategory: c.Category, seqFeed: c.Feed, seqTag: c.Tag} {
			if value <= 0 {
				continue
			}
			if err := tx.reserveID(ctx, userID, name, value); err != nil {
				return err
			}
		}
		return nil
	})
}
