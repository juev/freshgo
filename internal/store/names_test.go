package store

import (
	"context"
	"errors"
	"testing"
)

func TestNamesMatchIgnoringCase(t *testing.T) {
	categories := []*Category{{ID: 5, Name: "NEWS"}, {ID: 3, Name: "News"}, {ID: 9, Name: "Новости"}}
	for _, tc := range []struct {
		name string
		want int64
	}{
		{"News", 3},    // the exact name wins over the older fold
		{"NEWS", 5},    // ... and over the oldest
		{"news", 3},    // no exact name: the oldest
		{"новости", 9}, // Cyrillic folds too
		{"НОВОСТИ", 9},
		{"sport", 0},
	} {
		var got int64
		if c := CategoryNamed(categories, tc.name); c != nil {
			got = c.ID
		}
		if got != tc.want {
			t.Errorf("CategoryNamed(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
	tags := []*Tag{{ID: 8, Name: "Важное"}, {ID: 2, Name: "ВАЖНОЕ"}}
	if got := TagNamed(tags, "важное"); got == nil || got.ID != 2 {
		t.Errorf("TagNamed(важное) = %+v, want the oldest, 2", got)
	}
	if got := TagNamed(tags, "Важное"); got == nil || got.ID != 8 {
		t.Errorf("TagNamed(Важное) = %+v, want the exact one, 8", got)
	}
}

func TestNamesDifferingInCaseAreTaken(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := &User{Name: "alice"}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		news := &Category{UserID: u.ID, Name: "News"}
		if err := s.CreateCategory(ctx, news); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCategory(ctx, &Category{UserID: u.ID, Name: "Новости"}); err != nil {
			t.Fatal(err)
		}
		label := &Tag{UserID: u.ID, Name: "Keep"}
		if err := s.CreateTag(ctx, label); err != nil {
			t.Fatal(err)
		}

		refused := map[string]func() error{
			"category NEWS next to News":    func() error { return s.CreateCategory(ctx, &Category{UserID: u.ID, Name: "NEWS"}) },
			"category новости":              func() error { return s.CreateCategory(ctx, &Category{UserID: u.ID, Name: "новости"}) },
			"label news next to category":   func() error { return s.CreateTag(ctx, &Tag{UserID: u.ID, Name: "news"}) },
			"category KEEP next to a label": func() error { return s.CreateCategory(ctx, &Category{UserID: u.ID, Name: "KEEP"}) },
			"label keep next to Keep":       func() error { return s.CreateTag(ctx, &Tag{UserID: u.ID, Name: "keep"}) },
			"label renamed to NEWS":         func() error { return s.RenameTag(ctx, u.ID, label.ID, "NEWS") },
			"label updated to НОВОСТИ":      func() error { return s.UpdateTag(ctx, &Tag{UserID: u.ID, ID: label.ID, Name: "НОВОСТИ"}) },
		}
		for what, create := range refused {
			if err := create(); !errors.Is(err, ErrConflict) {
				t.Errorf("%s: error = %v, want ErrConflict", what, err)
			}
		}

		// Another user has names of their own.
		bob := &User{Name: "bob"}
		if err := s.CreateUser(ctx, bob); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCategory(ctx, &Category{UserID: bob.ID, Name: "NEWS"}); err != nil {
			t.Errorf("category NEWS of another user: %v", err)
		}

		// A row may change the case of its own name.
		if err := s.UpdateCategory(ctx, &Category{UserID: u.ID, ID: news.ID, Name: "news"}); err != nil {
			t.Errorf("category News renamed to news: %v", err)
		}
		if err := s.RenameTag(ctx, u.ID, label.ID, "keep"); err != nil {
			t.Errorf("label Keep renamed to keep: %v", err)
		}
	})
}

// Pairs stored before names were matched ignoring case are not merged, and
// can still be edited without a rename.
func TestStoredPairIsKept(t *testing.T) {
	eachEngine(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		u := &User{Name: "alice"}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		for i, name := range []string{"News", "news"} {
			_, err := s.exec(ctx, `INSERT INTO categories (user_id, id, name, kind, last_update, error, attributes)
				VALUES (?, ?, ?, 0, 0, 0, '{}')`, u.ID, 10+i, name)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.UpdateCategory(ctx, &Category{UserID: u.ID, ID: 11, Name: "news", Kind: 1, Attributes: []byte("{}")}); err != nil {
			t.Errorf("update of a stored pair without a rename: %v", err)
		}
		categories, err := s.Categories(ctx, u.ID)
		if err != nil || len(categories) != 3 {
			t.Errorf("Categories = %v, %v; want the default one and both of the pair", categories, err)
		}
	})
}
