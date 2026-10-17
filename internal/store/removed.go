package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// RemovedFeed is a feed that was deleted with what went with it: enough for
// RestoreFeed to put it back as it was.
type RemovedFeed struct {
	Feed    *Feed
	Entries []*Entry
	// Labels are the identifiers of the labels of each entry, by entry.
	Labels map[int64][]int64
	// Icon is the icon the user chose for the feed, nil without one.
	Icon *CustomIcon
}

// RemoveFeed deletes a feed as DeleteFeed does and returns what it was.
func (s *Store) RemoveFeed(ctx context.Context, userID, id int64) (*RemovedFeed, error) {
	var removed *RemovedFeed
	err := s.InTx(ctx, func(tx *Store) error {
		f, err := tx.FeedByID(ctx, userID, id)
		if err != nil {
			return err
		}
		removed = &RemovedFeed{Feed: f, Labels: map[int64][]int64{}}
		if removed.Entries, err = tx.EntriesByFeed(ctx, userID, id); err != nil {
			return err
		}
		rows, err := tx.query(ctx, `
			SELECT t.entry_id, t.tag_id FROM entry_tags t JOIN entries e ON e.user_id = t.user_id AND e.id = t.entry_id
			WHERE e.user_id = ? AND e.feed_id = ?`, userID, id)
		if err != nil {
			return fmt.Errorf("store: labels of feed %d: %w", id, err)
		}
		pairs, err := collect(rows, func(sc scanner) ([2]int64, error) {
			var p [2]int64
			err := sc.Scan(&p[0], &p[1])
			return p, err
		})
		if err != nil {
			return fmt.Errorf("store: labels of feed %d: %w", id, err)
		}
		for _, p := range pairs {
			removed.Labels[p[0]] = append(removed.Labels[p[0]], p[1])
		}
		icon := &CustomIcon{}
		err = tx.queryRow(ctx, `SELECT hash, content, modified FROM custom_icons WHERE user_id = ? AND feed_id = ?`, userID, id).
			Scan(&icon.Hash, &icon.Content, &icon.Modified)
		switch {
		case err == nil:
			removed.Icon = icon
		case !errors.Is(err, ErrNotFound):
			return fmt.Errorf("store: custom icon of feed %d: %w", id, err)
		}
		return tx.DeleteFeed(ctx, userID, id)
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}

// RestoreFeed puts a removed feed back under its identifier, with its
// entries, their labels and its icon. A feed whose category is gone goes to
// the default one, and labels that are gone stay off. A feed of that
// identifier gives ErrConflict.
func (s *Store) RestoreFeed(ctx context.Context, removed *RemovedFeed) error {
	f := *removed.Feed
	return s.InTx(ctx, func(tx *Store) error {
		categories, err := tx.Categories(ctx, f.UserID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(categories, func(c *Category) bool { return c.ID == f.CategoryID }) {
			f.CategoryID = DefaultCategoryID
		}
		if err := tx.CreateFeed(ctx, &f); err != nil {
			return err
		}
		entries := make([]*Entry, len(removed.Entries))
		for i, e := range removed.Entries {
			copied := *e
			entries[i] = &copied
		}
		if err := tx.InsertEntries(ctx, f.UserID, entries); err != nil {
			return err
		}
		tags, err := tx.Tags(ctx, f.UserID)
		if err != nil {
			return err
		}
		labeled := map[int64][]int64{}
		for entry, labels := range removed.Labels {
			for _, tag := range labels {
				labeled[tag] = append(labeled[tag], entry)
			}
		}
		for _, t := range tags {
			if err := tx.TagEntries(ctx, f.UserID, t.ID, labeled[t.ID]); err != nil {
				return err
			}
		}
		if removed.Icon != nil {
			return tx.SetCustomIcon(ctx, f.UserID, f.ID, *removed.Icon)
		}
		return nil
	})
}

// RemovedCategory is a category that was deleted, with the feeds that went
// from it to the default category.
type RemovedCategory struct {
	Category *Category
	Feeds    []int64
}

// RemoveCategory deletes a category as DeleteCategory does and returns what
// it was. The default category cannot be removed.
func (s *Store) RemoveCategory(ctx context.Context, userID, id int64) (*RemovedCategory, error) {
	if id == DefaultCategoryID {
		return nil, fmt.Errorf("store: remove category %d: %w", id, ErrConflict)
	}
	var removed *RemovedCategory
	err := s.InTx(ctx, func(tx *Store) error {
		categories, err := tx.Categories(ctx, userID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(categories, func(c *Category) bool { return c.ID == id })
		if i < 0 {
			return fmt.Errorf("store: remove category %d: %w", id, ErrNotFound)
		}
		removed = &RemovedCategory{Category: categories[i]}
		if removed.Feeds, err = tx.ids(ctx, `SELECT id FROM feeds WHERE user_id = ? AND category_id = ?`, userID, id); err != nil {
			return fmt.Errorf("store: remove category %d: %w", id, err)
		}
		return tx.DeleteCategory(ctx, userID, id)
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}

// RestoreCategory puts a removed category back under its identifier and
// takes back those of its feeds that are still in the default category. A
// name taken meanwhile, or a category of that identifier, gives ErrConflict.
func (s *Store) RestoreCategory(ctx context.Context, removed *RemovedCategory) error {
	c := *removed.Category
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.CreateCategory(ctx, &c); err != nil {
			return err
		}
		for chunk := range slices.Chunk(removed.Feeds, idChunk) {
			_, err := tx.exec(ctx, `
				UPDATE feeds SET category_id = ? WHERE user_id = ? AND category_id = ? AND id IN (`+placeholders(len(chunk))+`)`,
				idArgs(chunk, c.ID, c.UserID, DefaultCategoryID)...)
			if err != nil {
				return fmt.Errorf("store: restore category %d: %w", c.ID, err)
			}
		}
		return nil
	})
}

// RemovedTag is a label that was deleted, with the entries it was on.
type RemovedTag struct {
	Tag     *Tag
	Entries []int64
}

// RemoveTag deletes a label as DeleteTag does and returns what it was.
func (s *Store) RemoveTag(ctx context.Context, userID, id int64) (*RemovedTag, error) {
	var removed *RemovedTag
	err := s.InTx(ctx, func(tx *Store) error {
		tags, err := tx.Tags(ctx, userID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(tags, func(t *Tag) bool { return t.ID == id })
		if i < 0 {
			return fmt.Errorf("store: remove tag %d: %w", id, ErrNotFound)
		}
		removed = &RemovedTag{Tag: tags[i]}
		if removed.Entries, err = tx.ids(ctx, `SELECT entry_id FROM entry_tags WHERE user_id = ? AND tag_id = ?`, userID, id); err != nil {
			return fmt.Errorf("store: remove tag %d: %w", id, err)
		}
		return tx.DeleteTag(ctx, userID, id)
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}

// RestoreTag puts a removed label back under its identifier, on those of its
// entries that are still there. A name taken meanwhile, or a label of that
// identifier, gives ErrConflict.
func (s *Store) RestoreTag(ctx context.Context, removed *RemovedTag) error {
	t := *removed.Tag
	return s.InTx(ctx, func(tx *Store) error {
		if err := tx.CreateTag(ctx, &t); err != nil {
			return err
		}
		return tx.TagEntries(ctx, t.UserID, t.ID, removed.Entries)
	})
}
