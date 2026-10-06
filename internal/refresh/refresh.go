// Package refresh keeps the stored entries in step with the feeds: it picks
// the feeds that are due, downloads and reads them, adds the new entries and
// rewrites the changed ones.
//
// The behaviour follows FreshRSS_feed_Controller::actualizeFeeds and what it
// calls at commit 219eaf58. See docs/specs/refresh.md.
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/websub"
)

// feedConcurrency is the number of feeds of one user refreshed at a time.
// Requests to one host are limited further by the fetch client.
const feedConcurrency = 8

// Refresher refreshes feeds. It is safe for concurrent use; runs do not
// overlap.
type Refresher struct {
	db     *store.Store
	client *fetch.Client
	hooks  *hooks.Registry
	log    *slog.Logger
	now    func() time.Time

	// Icons, when set, keeps the icons of refreshed feeds up to date.
	Icons *favicon.Service
	// WebSub, when set, subscribes to the hubs feeds announce. A feed whose
	// hub delivers is then polled once a day only.
	WebSub *websub.Service

	// running keeps one run at a time: a second one would find the same
	// feeds due and fetch them again.
	running sync.Mutex
}

// New returns a Refresher. Handlers of the registry are called from several
// goroutines at once.
func New(db *store.Store, client *fetch.Client, registry *hooks.Registry, log *slog.Logger) *Refresher {
	return &Refresher{db: db, client: client, hooks: registry, log: log, now: time.Now}
}

// Options select what a run refreshes.
type Options struct {
	// Force refreshes the feeds whose refresh period has not passed yet as
	// well. Muted feeds stay out.
	Force bool
}

// Stats is the outcome of a run for one user.
type Stats struct {
	User string
	// Refreshed counts the feeds fetched without an error, unchanged ones
	// included; Failed the feeds that are now marked as failing.
	Refreshed int
	Failed    int
	// NewEntries and UpdatedEntries count stored entries.
	NewEntries     int
	UpdatedEntries int
}

// Run refreshes the due feeds of every enabled user. A feed that fails is
// marked and counted, and the run goes on; the error is for what stops the
// run as a whole: the database or a cancelled context.
func (r *Refresher) Run(ctx context.Context, o Options) ([]Stats, error) {
	r.running.Lock()
	defer r.running.Unlock()

	users, err := r.db.Users(ctx)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	https, err := r.httpsDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	var all []Stats
	for _, u := range users {
		conf := readUserSettings(u.Settings)
		if !conf.enabled {
			r.log.Info("user is disabled, feeds are not refreshed", "user", u.Name)
			continue
		}
		r.hooks.UserMaintenance.Call(ctx, u)
		j, err := r.newJob(ctx, u, conf, https)
		if err != nil {
			return all, fmt.Errorf("refresh: user %s: %w", u.Name, err)
		}
		st, err := r.refreshUser(ctx, j, o)
		all = append(all, st)
		if err != nil {
			return all, fmt.Errorf("refresh: user %s: %w", u.Name, err)
		}
		r.log.Info("feeds refreshed", "user", u.Name, "feeds", st.Refreshed, "failed", st.Failed,
			"new", st.NewEntries, "updated", st.UpdatedEntries)
	}
	return all, nil
}

// Schedule runs a refresh at once and then every interval until ctx is done.
// A run that fails is logged and the next one still happens.
func (r *Refresher) Schedule(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := r.Run(ctx, Options{}); err != nil && ctx.Err() == nil {
			r.log.Error("refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Refresher) httpsDomains(ctx context.Context) (*feed.HTTPSDomains, error) {
	extra, err := r.db.Setting(ctx, store.SettingForceHTTPS)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return feed.NewHTTPSDomains(strings.Split(extra, "\n")), nil
}

// job is what the feeds of one user have in common during a run.
type job struct {
	user  *store.User
	conf  userSettings
	https *feed.HTTPSDomains
	// categories are the user's, by identifier, as they were when the run
	// reached the user.
	categories map[int64]*category
	// search is what the queries of filters are read against; rules are the
	// user's own filters, labels the rules of the labels that have any.
	search search.Options
	rules  []search.Rule
	labels []labelRules
	// pushing are the WebSub topics whose hub can be relied on.
	pushing map[string]bool
}

// newJob reads what the feeds of the user have in common.
func (r *Refresher) newJob(ctx context.Context, u *store.User, conf userSettings, https *feed.HTTPSDomains) (*job, error) {
	categories, err := r.categories(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	j := &job{user: u, conf: conf, https: https, categories: categories}
	if err := r.loadRules(ctx, j); err != nil {
		return nil, err
	}
	if r.WebSub != nil {
		if j.pushing, err = r.WebSub.Working(ctx); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func (r *Refresher) refreshUser(ctx context.Context, j *job, o Options) (Stats, error) {
	st := Stats{User: j.user.Name}
	feeds, err := r.db.Feeds(ctx, j.user.ID)
	if err != nil {
		return st, err
	}
	// The feed that has waited longest goes first; a failed attempt counts
	// as an attempt.
	sort.SliceStable(feeds, func(a, b int) bool { return lastAttempt(feeds[a]) < lastAttempt(feeds[b]) })
	feeds, _ = r.hooks.FeedsListBeforeActualize.Call(ctx, feeds)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		slots = make(chan struct{}, feedConcurrency)
		now   = r.now().Unix()
	)
	for _, f := range feeds {
		f, ok := r.hooks.FeedBeforeActualize.Call(ctx, f)
		if !ok || !due(f, j.conf, now, o.Force, j.pushing[f.WebSubTopic]) {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			res, err := r.refreshFeed(ctx, j, f)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if ctx.Err() == nil {
					st.Failed++
					r.log.Warn("feed failed", "user", j.user.Name, "feed", f.ID, "url", withoutCredentials(f.URL), "error", err)
				}
				return
			}
			st.Refreshed++
			st.NewEntries += res.added
			st.UpdatedEntries += res.updated
		}()
	}
	wg.Wait()
	return st, ctx.Err()
}

func lastAttempt(f *store.Feed) int64 {
	return max(f.LastUpdate, f.Error)
}

// pushedPeriod is how often a feed is polled while its hub delivers.
const pushedPeriod = 24 * 3600

// due reports whether the feed is to be refreshed at the time now. pushed
// says a WebSub hub delivers its entries.
func due(f *store.Feed, conf userSettings, now int64, force, pushed bool) bool {
	if f.TTL < 0 {
		return false
	}
	if force {
		return true
	}
	ttl := f.TTL
	if ttl == 0 {
		ttl = conf.ttlDefault
	}
	if pushed {
		ttl = max(ttl, pushedPeriod)
	}
	return now > f.LastUpdate+int64(ttl)
}
