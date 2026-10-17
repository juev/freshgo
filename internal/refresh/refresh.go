// Package refresh keeps the stored entries in step with the feeds: it picks
// the feeds that are due, downloads and reads them, adds the new entries and
// rewrites the changed ones.
//
// The behaviour follows FreshRSS_feed_Controller::actualizeFeeds and what it
// calls at commit 219eaf58. See docs/specs/refresh.md.
package refresh

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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
	// Browser, when set, reads the pages of the feeds that ask for it
	// (page_by_browser) in place of a request: it returns the address a
	// page ends at and its markup.
	Browser func(ctx context.Context, address string) (final, markup string, err error)

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
	// included; Unchanged those of them whose server answered 304 Not
	// Modified; Failed the feeds that are now marked as failing.
	Refreshed int
	Unchanged int
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
	// hubs are the subscriptions the feeds of this run announced.
	hubs := map[feedRef]announcement{}
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
		r.refreshOPMLs(ctx, j)
		st, err := r.refreshUser(ctx, j, o, hubs)
		all = append(all, st)
		if err != nil {
			return all, fmt.Errorf("refresh: user %s: %w", u.Name, err)
		}
		r.log.Info("feeds refreshed", "user", u.Name, "feeds", st.Refreshed, "unchanged", st.Unchanged, "failed", st.Failed,
			"new", st.NewEntries, "updated", st.UpdatedEntries)
	}
	r.subscribe(ctx, hubs)
	return all, nil
}

// subscribe makes sure the hubs of the feeds have been asked to push. It is
// not left to the polls: a feed that answers 304 names no hub, and its
// subscription is to be made and renewed all the same.
func (r *Refresher) subscribe(ctx context.Context, hubs map[feedRef]announcement) {
	if r.WebSub == nil {
		return
	}
	refs := slices.SortedFunc(maps.Keys(hubs), func(a, b feedRef) int {
		if byUser := cmp.Compare(a.user, b.user); byUser != 0 {
			return byUser
		}
		return cmp.Compare(a.feed, b.feed)
	})
	for _, ref := range refs {
		if ctx.Err() != nil {
			return
		}
		what := hubs[ref]
		r.WebSub.Ensure(ctx, ref.user, ref.feed, what.topic, what.hub)
	}
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

// feedRef names a feed of a user across the users of a run.
type feedRef struct {
	user, feed int64
}

// announcement is what a feed last said about its topic and hub.
type announcement struct {
	topic, hub string
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
	// pushing are the feeds whose WebSub hub can be relied on, by identifier.
	pushing map[int64]bool
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
		if j.pushing, err = r.WebSub.Working(ctx, u.ID); err != nil {
			return nil, err
		}
	}
	return j, nil
}

// refreshUser refreshes the due feeds of the user of the job. Into hubs go
// the topics and hubs the user's feeds announced when they were last read
// whole, muted feeds left out: nothing is pushed into those.
func (r *Refresher) refreshUser(ctx context.Context, j *job, o Options, hubs map[feedRef]announcement) (Stats, error) {
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
	// How much each feed has published lately says how often to ask it.
	// Entries dated ahead, as a list of events has them, say nothing of it.
	published, err := r.db.PublishedBetween(ctx, j.user.ID, now-frequencyWindow, now)
	if err != nil {
		return st, err
	}
	limit := maxPeriod(j.user)
	for _, f := range feeds {
		if f.TTL >= 0 && f.WebSubTopic != "" && f.WebSubHub != "" {
			hubs[feedRef{f.UserID, f.ID}] = announcement{f.WebSubTopic, f.WebSubHub}
		}
		f, ok := r.hooks.FeedBeforeActualize.Call(ctx, f)
		if !ok || !due(f, published[f.ID], now, limit, o.Force, j.pushing[f.ID]) {
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
			if res.unchanged {
				st.Unchanged++
			}
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

// A feed without a period of its own is asked as often as it has published
// entries within frequencyWindow, and at least once in the period the user
// has set for such feeds (ttl_default of FreshRSS), DefaultMaxPeriod without
// one. It is not asked more often than once in minPeriod however much it
// publishes, unless the period of the user is shorter still.
const (
	frequencyWindow = 7 * 24 * 3600
	minPeriod       = 300
	// DefaultMaxPeriod is that period, in seconds, of a user who has set none.
	DefaultMaxPeriod = 3600
)

// maxPeriod is the longest a feed of the user without a period of its own
// is left alone, in seconds.
func maxPeriod(u *store.User) int {
	if v, ok := get[int](readAttributes(u.Settings), "ttl_default"); ok && v > 0 {
		return v
	}
	return DefaultMaxPeriod
}

// longestPeriod is the longest the feed is left alone by its settings and
// those of its user, in seconds: its own period, else that of the user. Its
// server puts the next request off no further than that.
func longestPeriod(u *store.User, f *store.Feed) int64 {
	if f.TTL > 0 {
		return int64(f.TTL)
	}
	return int64(maxPeriod(u))
}

// period is the refresh period at the time now, in seconds, of a feed that
// is not muted; published is what it has published within frequencyWindow
// and limit the longest a feed without a period of its own is left alone.
//
// The entries are taken to speak for the time since the oldest of them, not
// for the whole window: a feed subscribed an hour ago has no more than its
// document listed, and a feed that was silent and comes to life is not as
// slow as its silence. The time runs on while nothing comes, so the period
// of a feed that falls silent grows by itself.
//
// Entries dated now would give a period of no time at all, and a feed just
// refreshed would be due again at once: hence minPeriod. It is shorter than
// the runs of the server are apart, so a busy feed is still due at each.
func period(f *store.Feed, published store.Published, now int64, limit int) int {
	if f.TTL > 0 {
		return f.TTL
	}
	if published.Entries == 0 {
		return limit
	}
	return int(min(int64(limit), max(minPeriod, (now-published.Oldest)/int64(published.Entries))))
}

// A feed that has failed twice in a row waits retryStep before it is tried
// again, and twice as long after every further failure, maxRetryWait at most.
const (
	retryStep    = 600
	maxRetryWait = 24 * 3600
)

// retryWait is how long, in seconds, a feed is left alone after its last
// failed refresh, which was the failures-th in a row.
func retryWait(failures int) int64 {
	if failures < 2 {
		return 0
	}
	// Beyond eight doublings the wait is past maxRetryWait.
	return min(int64(maxRetryWait), int64(retryStep)<<min(failures-2, 8))
}

// due reports whether the feed is to be refreshed at the time now. published
// is what it has published within frequencyWindow, limit the longest a feed
// without a period of its own is left alone; pushed says a WebSub hub
// delivers its entries.
func due(f *store.Feed, published store.Published, now int64, limit int, force, pushed bool) bool {
	if f.TTL < 0 {
		return false
	}
	if force {
		return true
	}
	if now < f.NotBefore || now < f.Error+retryWait(f.Failures) {
		return false
	}
	ttl := period(f, published, now, limit)
	if pushed {
		ttl = max(ttl, pushedPeriod)
	}
	return now > f.LastUpdate+int64(ttl)
}
