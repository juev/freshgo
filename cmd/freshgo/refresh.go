package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
)

// newHooks returns the extension points with their handlers. freshgo ships
// none; a build with extensions adds them here, see docs/specs/hooks.md.
func newHooks() *hooks.Registry {
	return &hooks.Registry{}
}

// userAgent is what freshgo calls itself when it fetches feeds.
func userAgent() string {
	return "freshgo/" + buildVersion() + " (+https://github.com/juev/freshgo)"
}

func newRefresher(ctx context.Context, e env, conf *config.Config, db *store.Store) (*refresh.Refresher, error) {
	client, err := fetch.New(fetch.Options{UserAgent: userAgent(), Allowlist: conf.Allowlist()})
	if err != nil {
		return nil, err
	}
	registry := newHooks()
	registry.Init.Call(ctx, struct{}{})
	return refresh.New(db, client, registry, slog.New(slog.NewTextHandler(e.stderr, nil))), nil
}

func runRefresh(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "refresh")
	var opts refresh.Options
	fs.BoolVar(&opts.Force, "force", false, "also refresh the feeds whose refresh period has not passed yet")
	db, err := openStore(ctx, fs, conf, args)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	r, err := newRefresher(ctx, e, conf, db)
	if err != nil {
		return err
	}

	// A feed that fails is reported and does not fail the command: the next
	// run tries it again.
	stats, err := r.Run(ctx, opts)
	for _, st := range stats {
		if _, werr := fmt.Fprintf(e.stdout, "%s: %d feeds refreshed, %d failed, %d new and %d updated entries\n",
			st.User, st.Refreshed, st.Failed, st.NewEntries, st.UpdatedEntries); werr != nil {
			return errors.Join(err, werr)
		}
	}
	return err
}

// runPurge deletes the entries the retention settings give up, without
// waiting for the feeds to be refreshed.
func runPurge(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "purge")
	db, err := openStore(ctx, fs, conf, args)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	r, err := newRefresher(ctx, e, conf, db)
	if err != nil {
		return err
	}
	stats, err := r.Purge(ctx)
	for _, st := range stats {
		if _, werr := fmt.Fprintf(e.stdout, "%s: %d entries deleted\n", st.User, st.Deleted); werr != nil {
			return errors.Join(err, werr)
		}
	}
	return err
}

// runServe runs the refresh scheduler until the process is told to stop.
func runServe(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "serve")
	db, err := openStore(ctx, fs, conf, args)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	r, err := newRefresher(ctx, e, conf, db)
	if err != nil {
		return err
	}
	r.Schedule(ctx, conf.RefreshInterval)
	return nil
}
