package store

import (
	"context"
	"fmt"
)

// CreateCategory adds a category. A zero c.ID is replaced with a new
// identifier; a taken name or identifier gives ErrConflict.
func (s *Store) CreateCategory(ctx context.Context, c *Category) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.assignID(ctx, c.UserID, seqCategory, &c.ID); err != nil {
			return err
		}
		_, err := tx.exec(ctx, `
			INSERT INTO categories (user_id, id, name, kind, last_update, error, attributes)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.UserID, c.ID, c.Name, c.Kind, c.LastUpdate, c.Error, jsonObject(c.Attributes))
		if err != nil {
			return fmt.Errorf("store: create category %q: %w", c.Name, err)
		}
		return nil
	})
}

// UpdateCategory replaces every field of a category except its identifier.
// A missing category gives ErrNotFound, a taken name ErrConflict.
func (s *Store) UpdateCategory(ctx context.Context, c *Category) error {
	res, err := s.exec(ctx, `
		UPDATE categories SET name = ?, kind = ?, last_update = ?, error = ?, attributes = ?
		WHERE user_id = ? AND id = ?`,
		c.Name, c.Kind, c.LastUpdate, c.Error, jsonObject(c.Attributes), c.UserID, c.ID)
	if err != nil {
		return fmt.Errorf("store: update category %d: %w", c.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update category %d: %w", c.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: update category %d: %w", c.ID, ErrNotFound)
	}
	return nil
}

// Categories returns the categories of a user ordered by identifier.
func (s *Store) Categories(ctx context.Context, userID int64) ([]*Category, error) {
	rows, err := s.query(ctx, `
		SELECT id, name, kind, last_update, error, attributes
		FROM categories WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: categories: %w", err)
	}
	categories, err := collect(rows, func(sc scanner) (*Category, error) {
		c := &Category{UserID: userID}
		var attributes string
		if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &c.LastUpdate, &c.Error, &attributes); err != nil {
			return nil, err
		}
		c.Attributes = []byte(attributes)
		return c, nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: categories: %w", err)
	}
	return categories, nil
}
