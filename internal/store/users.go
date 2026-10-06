package store

import (
	"context"
	"fmt"
)

// CreateUser adds a user together with the default category and sets u.ID.
// A taken name gives ErrConflict.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	return s.InTx(ctx, func(tx *Store) error {
		err := tx.queryRow(ctx, `
			INSERT INTO users (name, api_password_hash, settings) VALUES (?, ?, ?)
			RETURNING id`, u.Name, u.APIPasswordHash, jsonObject(u.Settings)).Scan(&u.ID)
		if err != nil {
			return fmt.Errorf("store: create user %q: %w", u.Name, err)
		}
		return tx.CreateCategory(ctx, &Category{UserID: u.ID, ID: DefaultCategoryID, Name: DefaultCategoryName})
	})
}

// UserByName returns the user with the given name or ErrNotFound.
func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	u, err := scanUser(s.queryRow(ctx, `
		SELECT id, name, api_password_hash, settings FROM users WHERE name = ?`, name))
	if err != nil {
		return nil, fmt.Errorf("store: user %q: %w", name, err)
	}
	return u, nil
}

// Users returns all users ordered by name.
func (s *Store) Users(ctx context.Context) ([]*User, error) {
	rows, err := s.query(ctx, `SELECT id, name, api_password_hash, settings FROM users ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: users: %w", err)
	}
	users, err := collect(rows, scanUser)
	if err != nil {
		return nil, fmt.Errorf("store: users: %w", err)
	}
	return users, nil
}

func scanUser(sc scanner) (*User, error) {
	u := &User{}
	var settings string
	if err := sc.Scan(&u.ID, &u.Name, &u.APIPasswordHash, &settings); err != nil {
		return nil, err
	}
	u.Settings = []byte(settings)
	return u, nil
}
