package store

import (
	"context"
	"fmt"
	"slices"
)

// CreateTag adds a user label. A zero t.ID is replaced with a new identifier.
// Labels and categories share one namespace in the Google Reader API
// (user/-/label/<name>), so a name taken by a label or by a category gives
// ErrConflict.
func (s *Store) CreateTag(ctx context.Context, t *Tag) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.nameFree(ctx, "categories", t.UserID, t.Name); err != nil {
			return fmt.Errorf("store: create tag %q: %w", t.Name, err)
		}
		if err := tx.assignID(ctx, t.UserID, seqTag, &t.ID); err != nil {
			return err
		}
		_, err := tx.exec(ctx, `
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

// RenameTag gives a label another name. A name taken by a label or by a
// category gives ErrConflict, a missing label ErrNotFound.
func (s *Store) RenameTag(ctx context.Context, userID, id int64, name string) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.nameFree(ctx, "categories", userID, name); err != nil {
			return fmt.Errorf("store: rename tag %d: %w", id, err)
		}
		n, err := tx.affected(ctx, `UPDATE tags SET name = ? WHERE user_id = ? AND id = ?`, name, userID, id)
		if err != nil {
			return fmt.Errorf("store: rename tag %d: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("store: rename tag %d: %w", id, ErrNotFound)
		}
		return nil
	})
}

// nameFree gives ErrConflict when a row of the table, categories or tags,
// already has the name: the two share the names of user/-/label/<name>.
func (s *Store) nameFree(ctx context.Context, table string, userID int64, name string) error {
	var taken int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE user_id = ? AND name = ?`, userID, name).Scan(&taken)
	if err != nil {
		return err
	}
	if taken > 0 {
		owner := "a category"
		if table == "tags" {
			owner = "a label"
		}
		return fmt.Errorf("%w: %s has this name", ErrConflict, owner)
	}
	return nil
}

// DeleteTag removes a label and takes it off every entry.
func (s *Store) DeleteTag(ctx context.Context, userID, id int64) error {
	if _, err := s.exec(ctx, `DELETE FROM tags WHERE user_id = ? AND id = ?`, userID, id); err != nil {
		return fmt.Errorf("store: delete tag %d: %w", id, err)
	}
	return nil
}

// TagEntries attaches a label to those of the given entries that exist.
func (s *Store) TagEntries(ctx context.Context, userID, tagID int64, entryIDs []int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		for chunk := range slices.Chunk(entryIDs, idChunk) {
			_, err := tx.exec(ctx, `
				INSERT INTO entry_tags (user_id, tag_id, entry_id)
				SELECT user_id, ?, id FROM entries WHERE user_id = ? AND id IN (`+placeholders(len(chunk))+`)
				ON CONFLICT DO NOTHING`, idArgs(chunk, tagID, userID)...)
			if err != nil {
				return fmt.Errorf("store: tag %d on entries: %w", tagID, err)
			}
		}
		return nil
	})
}

// UntagEntries takes a label off the given entries.
func (s *Store) UntagEntries(ctx context.Context, userID, tagID int64, entryIDs []int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		for chunk := range slices.Chunk(entryIDs, idChunk) {
			_, err := tx.exec(ctx, `
				DELETE FROM entry_tags WHERE user_id = ? AND tag_id = ? AND entry_id IN (`+placeholders(len(chunk))+`)`,
				idArgs(chunk, userID, tagID)...)
			if err != nil {
				return fmt.Errorf("store: tag %d off entries: %w", tagID, err)
			}
		}
		return nil
	})
}

// UpdateTag replaces the name and the attributes of a label. A name taken by
// another label or by a category gives ErrConflict, a missing label
// ErrNotFound.
func (s *Store) UpdateTag(ctx context.Context, t *Tag) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.nameFree(ctx, "categories", t.UserID, t.Name); err != nil {
			return fmt.Errorf("store: update tag %d: %w", t.ID, err)
		}
		n, err := tx.affected(ctx, `UPDATE tags SET name = ?, attributes = ? WHERE user_id = ? AND id = ?`,
			t.Name, jsonObject(t.Attributes), t.UserID, t.ID)
		if err != nil {
			return fmt.Errorf("store: update tag %d: %w", t.ID, err)
		}
		if n == 0 {
			return fmt.Errorf("store: update tag %d: %w", t.ID, ErrNotFound)
		}
		return nil
	})
}
