package store

import (
	"context"
	"fmt"
	"regexp"
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

// userNamePattern is what FreshRSS takes for a user name
// (FreshRSS_user_Controller::USERNAME_PATTERN). API tokens start with the
// name, so the rule is part of what clients rely on.
var userNamePattern = regexp.MustCompile(`^([0-9a-zA-Z_][0-9a-zA-Z_.@\-]{1,38}|[0-9a-zA-Z])$`)

// ValidUserName reports whether a user can be called name.
func ValidUserName(name string) bool {
	return userNamePattern.MatchString(name)
}

// SetAPIPasswordHash replaces the hash of the API password of a user. The
// tokens issued for the old password stop working.
func (s *Store) SetAPIPasswordHash(ctx context.Context, userID int64, hash string) error {
	n, err := s.affected(ctx, `UPDATE users SET api_password_hash = ? WHERE id = ?`, hash, userID)
	if err != nil {
		return fmt.Errorf("store: set API password of user %d: %w", userID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: set API password of user %d: %w", userID, ErrNotFound)
	}
	return nil
}

// DeleteUser removes a user with everything that belongs to them.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	n, err := s.affected(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("store: delete user %d: %w", userID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: delete user %d: %w", userID, ErrNotFound)
	}
	return nil
}
