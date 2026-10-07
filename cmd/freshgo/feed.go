package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/juev/freshgo/internal/greader"
	"github.com/juev/freshgo/internal/store"
)

func feedCommands() []command {
	return []command{
		{"add", "subscribe a user to a feed and fetch its entries", runFeedAdd},
	}
}

// categoryNamed returns the category of the user with the name, created if
// there is none. An empty name is the default category.
func categoryNamed(ctx context.Context, db *store.Store, u *store.User, name string) (int64, error) {
	if name == "" {
		return store.DefaultCategoryID, nil
	}
	categories, err := db.Categories(ctx, u.ID)
	if err != nil {
		return 0, err
	}
	for _, c := range categories {
		if c.Name == name {
			return c.ID, nil
		}
	}
	c := &store.Category{UserID: u.ID, Name: name}
	if err := db.CreateCategory(ctx, c); errors.Is(err, store.ErrConflict) {
		// Categories and labels are addressed alike by API clients.
		return 0, fmt.Errorf("%q is a label of %s and cannot name a category", name, u.Name)
	} else if err != nil {
		return 0, err
	}
	return c.ID, nil
}

func runFeedAdd(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "feed add")
	synopsis(fs, e, "feed add [flags] <address>")
	user := fs.String("user", "", "the user to subscribe (required)")
	category := fs.String("category", "", "category to put the feed into, created if missing; the default one if not given")
	db, err := openStore(ctx, fs, conf, args, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	address, err := operand(fs, "the address of a feed")
	if err != nil {
		return err
	}
	u, err := findUser(ctx, db, *user)
	if err != nil {
		return err
	}
	s, err := newServices(ctx, e, conf, db)
	if err != nil {
		return err
	}
	defer s.close()
	categoryID, err := categoryNamed(ctx, db, u, *category)
	if err != nil {
		return err
	}
	feed := &store.Feed{
		URL: address, Kind: greader.KindOf(address), CategoryID: categoryID, Priority: store.PriorityMain,
	}
	if err := s.refresher.AddFeed(ctx, u, feed); err != nil {
		return err
	}
	entries, err := db.EntriesByFeed(ctx, u.ID, feed.ID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "feed %d: %s (%s), %d entries\n", feed.ID, feed.Name, feed.URL, len(entries))
	return err
}
