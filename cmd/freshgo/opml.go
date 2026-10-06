package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/juev/freshgo/internal/opml"
)

func opmlCommands() []command {
	return []command{
		{"import", "add the subscriptions of an OPML file, or of standard input, to those of a user", runOPMLImport},
		{"export", "write the subscriptions of a user as OPML to standard output", runOPMLExport},
	}
}

func runOPMLImport(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "opml import")
	synopsis(fs, e, "opml import [flags] [<file>]")
	user := fs.String("user", "", "the user to subscribe (required)")
	db, err := openStore(ctx, fs, conf, args, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	u, err := findUser(ctx, db, *user)
	if err != nil {
		return err
	}
	var data []byte
	if fs.NArg() == 1 {
		data, err = os.ReadFile(fs.Arg(0))
	} else {
		data, err = io.ReadAll(e.stdin)
	}
	if err != nil {
		return err
	}
	s, err := newServices(ctx, e, conf, db)
	if err != nil {
		return err
	}

	// When some feeds could not be added, the rest still are.
	added, importErr := opml.Import(ctx, db, s.registry, u, data)
	if importErr != nil && !errors.Is(importErr, opml.ErrIncomplete) {
		return importErr
	}
	failed := 0
	for _, f := range added {
		if f.TTL < 0 {
			// Muted in the document.
			continue
		}
		// A feed that fails is marked as failing and tried again later.
		if err := s.refresher.RefreshFeed(ctx, u, f.ID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			s.log.Warn("imported feed failed", "user", u.Name, "feed", f.ID, "error", err)
		}
	}
	if _, err := fmt.Fprintf(e.stdout, "%s: %d feeds added, %d of them failed to refresh\n", u.Name, len(added), failed); err != nil {
		return err
	}
	return importErr
}

func runOPMLExport(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "opml export")
	user := fs.String("user", "", "the user whose subscriptions to write (required)")
	db, err := openStore(ctx, fs, conf, args, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	u, err := findUser(ctx, db, *user)
	if err != nil {
		return err
	}
	data, err := opml.Export(ctx, db, u, time.Now())
	if err != nil {
		return err
	}
	_, err = e.stdout.Write(data)
	return err
}
