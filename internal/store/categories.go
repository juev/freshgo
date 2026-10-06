package store

import (
	"context"
	"fmt"
)

// CreateCategory adds a category. A zero c.ID is replaced with a new
// identifier; a taken identifier or a name taken by a category or by a label
// gives ErrConflict.
func (s *Store) CreateCategory(ctx context.Context, c *Category) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.nameFree(ctx, "tags", c.UserID, c.Name); err != nil {
			return fmt.Errorf("store: create category %q: %w", c.Name, err)
		}
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
// A missing category gives ErrNotFound, a name taken by another category or
// by a label ErrConflict.
func (s *Store) UpdateCategory(ctx context.Context, c *Category) error {
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.nameFree(ctx, "tags", c.UserID, c.Name); err != nil {
			return fmt.Errorf("store: update category %d: %w", c.ID, err)
		}
		n, err := tx.affected(ctx, `
			UPDATE categories SET name = ?, kind = ?, last_update = ?, error = ?, attributes = ?
			WHERE user_id = ? AND id = ?`,
			c.Name, c.Kind, c.LastUpdate, c.Error, jsonObject(c.Attributes), c.UserID, c.ID)
		if err != nil {
			return fmt.Errorf("store: update category %d: %w", c.ID, err)
		}
		if n == 0 {
			return fmt.Errorf("store: update category %d: %w", c.ID, ErrNotFound)
		}
		return nil
	})
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

// DeleteCategory removes a category after moving its feeds to the default
// one. The default category itself stays.
func (s *Store) DeleteCategory(ctx context.Context, userID, id int64) error {
	return s.InTx(ctx, func(tx *Store) error {
		_, err := tx.exec(ctx, `
			UPDATE feeds SET category_id = ? WHERE user_id = ? AND category_id = ?`, DefaultCategoryID, userID, id)
		if err != nil {
			return fmt.Errorf("store: delete category %d: %w", id, err)
		}
		if id == DefaultCategoryID {
			return nil
		}
		if _, err := tx.exec(ctx, `DELETE FROM categories WHERE user_id = ? AND id = ?`, userID, id); err != nil {
			return fmt.Errorf("store: delete category %d: %w", id, err)
		}
		return nil
	})
}
