package store

import (
	"context"
	"fmt"
)

// CreateTag adds a user label. A zero t.ID is replaced with a new identifier.
// Labels and categories share one namespace in the Google Reader API
// (user/-/label/<name>), so a name taken by a label or by a category gives
// ErrConflict.
func (s *Store) CreateTag(ctx context.Context, t *Tag) error {
	return s.InTx(ctx, func(tx *Store) error {
		var taken int
		err := tx.queryRow(ctx, `
			SELECT COUNT(*) FROM categories WHERE user_id = ? AND name = ?`, t.UserID, t.Name).Scan(&taken)
		if err != nil {
			return fmt.Errorf("store: create tag %q: %w", t.Name, err)
		}
		if taken > 0 {
			return fmt.Errorf("store: create tag %q: %w: a category has this name", t.Name, ErrConflict)
		}
		if err := tx.assignID(ctx, t.UserID, seqTag, &t.ID); err != nil {
			return err
		}
		_, err = tx.exec(ctx, `
			INSERT INTO tags (user_id, id, name, attributes) VALUES (?, ?, ?, ?)`,
			t.UserID, t.ID, t.Name, jsonObject(t.Attributes))
		if err != nil {
			return fmt.Errorf("store: create tag %q: %w", t.Name, err)
		}
		return nil
	})
}

// Tags returns the labels of a user ordered by identifier.
func (s *Store) Tags(ctx context.Context, userID int64) ([]*Tag, error) {
	rows, err := s.query(ctx, `
		SELECT id, name, attributes FROM tags WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: tags: %w", err)
	}
	tags, err := collect(rows, func(sc scanner) (*Tag, error) {
		t := &Tag{UserID: userID}
		var attributes string
		if err := sc.Scan(&t.ID, &t.Name, &attributes); err != nil {
			return nil, err
		}
		t.Attributes = []byte(attributes)
		return t, nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: tags: %w", err)
	}
	return tags, nil
}

// TagEntry attaches a label to an entry. Attaching it twice is not an error.
func (s *Store) TagEntry(ctx context.Context, userID, tagID, entryID int64) error {
	_, err := s.exec(ctx, `
		INSERT INTO entry_tags (user_id, tag_id, entry_id) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, userID, tagID, entryID)
	if err != nil {
		return fmt.Errorf("store: tag %d on entry %d: %w", tagID, entryID, err)
	}
	return nil
}

// EntryTagIDs returns the identifiers of the labels attached to an entry, ascending.
func (s *Store) EntryTagIDs(ctx context.Context, userID, entryID int64) ([]int64, error) {
	rows, err := s.query(ctx, `
		SELECT tag_id FROM entry_tags WHERE user_id = ? AND entry_id = ? ORDER BY tag_id`, userID, entryID)
	if err != nil {
		return nil, fmt.Errorf("store: tags of entry %d: %w", entryID, err)
	}
	ids, err := collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
	if err != nil {
		return nil, fmt.Errorf("store: tags of entry %d: %w", entryID, err)
	}
	return ids, nil
}
