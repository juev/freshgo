package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/websub"
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

// services are the parts of freshgo that work on the database of a command.
type services struct {
	registry  *hooks.Registry
	log       *slog.Logger
	refresher *refresh.Refresher
	icons     *favicon.Service
	// webSub is nil when WebSub is off.
	webSub *websub.Service
}

func newServices(ctx context.Context, e env, conf *config.Config, db *store.Store) (*services, error) {
	client, err := fetch.New(fetch.Options{UserAgent: userAgent(), Allowlist: conf.Allowlist()})
	if err != nil {
		return nil, err
	}
	s := &services{registry: newHooks(), log: slog.New(slog.NewTextHandler(e.stderr, nil))}
	s.registry.Init.Call(ctx, struct{}{})
	s.icons = favicon.New(db, client, s.log)
	s.refresher = refresh.New(db, client, s.registry, s.log)
	s.refresher.Icons = s.icons
	if conf.WebSub {
		s.webSub, err = websub.New(db, client, s.log, conf.BaseURL)
		switch {
		case errors.Is(err, websub.ErrNotPublic):
			// Not fatal: the feeds are polled as without WebSub.
			s.log.Warn("WebSub is off: hubs cannot reach the public URL of the server", "base-url", conf.BaseURL)
			s.webSub = nil
		case err != nil:
			return nil, err
		default:
			s.webSub.Pusher = s.refresher
			s.refresher.WebSub = s.webSub
		}
	}
	return s, nil
}

func runRefresh(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "refresh")
	var opts refresh.Options
	fs.BoolVar(&opts.Force, "force", false, "also refresh the feeds whose refresh period has not passed yet")
	db, err := openStore(ctx, fs, conf, args, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	s, err := newServices(ctx, e, conf, db)
	if err != nil {
		return err
	}

	// A feed that fails is reported and does not fail the command: the next
	// run tries it again.
	stats, err := s.refresher.Run(ctx, opts)
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
	db, err := openStore(ctx, fs, conf, args, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	s, err := newServices(ctx, e, conf, db)
	if err != nil {
		return err
	}
	stats, err := s.refresher.Purge(ctx)
	for _, st := range stats {
		if _, werr := fmt.Fprintf(e.stdout, "%s: %d entries deleted\n", st.User, st.Deleted); werr != nil {
			return errors.Join(err, werr)
		}
	}
	return err
}
