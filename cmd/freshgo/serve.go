package main

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/juev/freshgo/internal/authguard"
	"github.com/juev/freshgo/internal/digest"
	"github.com/juev/freshgo/internal/events"
	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/fever"
	"github.com/juev/freshgo/internal/greader"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web"
	"github.com/juev/freshgo/internal/web/i18n"
	"github.com/juev/freshgo/internal/websub"
)

// shutdownTimeout is how long requests in flight get to finish when the
// server is told to stop.
const shutdownTimeout = 10 * time.Second

// digestInterval is how often the users whose digest is due are looked for;
// a digest itself goes out once a day.
const digestInterval = time.Hour

// extensionsPath is where extensions answer requests of their own, as in
// FreshRSS: the name of the extension follows, or comes in the "ext"
// parameter.
const extensionsPath = "/api/misc.php"

// extensions sends a request under extensionsPath to the extension it names.
func extensions(registry *hooks.Registry) http.Handler {
	answer := func(w http.ResponseWriter, status int, text string) {
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(text))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("ext")
		if name == "" {
			name, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, extensionsPath), "/"), "/")
		}
		if name == "" {
			answer(w, http.StatusBadRequest, "Bad Request!")
			return
		}
		endpoint := registry.API(name)
		if endpoint == nil {
			answer(w, http.StatusNotFound, "Not Found!")
			return
		}
		endpoint.ServeHTTP(w, r)
	})
}

// routes sends a request to the part of the server its path belongs to. The
// Google Reader API lives at the root, as in the original service, and under
// the path FreshRSS serves it at; the Fever API has the path FreshRSS gives
// it. What belongs to no other part is a page of
// the web interface.
//
// hubs is nil when WebSub is off.
func routes(health, api, feverAPI, icons, misc, hubs, pages http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == healthPath:
			health.ServeHTTP(w, r)
		case strings.HasPrefix(path, favicon.Path):
			icons.ServeHTTP(w, r)
		case path == extensionsPath, strings.HasPrefix(path, extensionsPath+"/"):
			misc.ServeHTTP(w, r)
		case hubs != nil && strings.HasPrefix(path, websub.Path):
			hubs.ServeHTTP(w, r)
		case path == fever.Path:
			feverAPI.ServeHTTP(w, r)
		case path == greader.Alias, strings.HasPrefix(path, greader.Alias+"/"),
			strings.HasPrefix(path, "/accounts/"), strings.HasPrefix(path, "/reader/"):
			api.ServeHTTP(w, r)
		default:
			pages.ServeHTTP(w, r)
		}
	})
}

// cleanStoredTexts cleans, once for a database, the texts that were stored
// before every writer cleaned its own: an import used to store what
// FreshRSS let through, and the pages cleaned every text they showed. They
// show what is stored now, so nothing is shown before this is done.
func cleanStoredTexts(ctx context.Context, db *store.Store, log *slog.Logger) error {
	_, err := db.Setting(ctx, store.SettingTextsCleaned)
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	changed, err := db.RewriteEntryTexts(ctx, sanitize.Stored)
	if err != nil {
		return err
	}
	log.Info("cleaned the texts stored before", "changed", changed)
	return db.SetSetting(ctx, store.SettingTextsCleaned, "1")
}

// debugHandler hands out the profiles of the Go runtime and the metrics of
// the server. They are on a handler of their own, so that the address of
// the server never shows them.
func debugHandler(metrics *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics, promhttp.HandlerOpts{}))
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// databaseSizeTimeout bounds the query of the size of the database a scrape
// makes.
const databaseSizeTimeout = 5 * time.Second

// newMetrics returns the registry of the metrics of the server: those of the
// runtime and the process, of the refresh runs, the answers counted by
// requests and the size of the database.
func newMetrics(db *store.Store, refresher *refresh.Refresher, requests *prometheus.CounterVec, log *slog.Logger) *prometheus.Registry {
	metrics := prometheus.NewRegistry()
	metrics.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		requests,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "freshgo_database_size_bytes",
			Help: "How many bytes the database takes: the SQLite file without its write-ahead log, or the PostgreSQL database.",
		}, func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), databaseSizeTimeout)
			defer cancel()
			size, err := db.Size(ctx)
			if err != nil {
				log.Error("metrics: the size of the database", "error", err)
				return math.NaN()
			}
			return float64(size)
		}),
	)
	metrics.MustRegister(refresher.Collectors()...)
	return metrics
}

// newEvents returns what tells the open pages of a user that entries of
// theirs changed, and whether entries came: the store tells it, whichever
// part of the server changed them, a refresh, an import, a page or an API
// client.
func newEvents(db *store.Store) *events.Broker {
	broker := &events.Broker{}
	db.EntriesChanged = func(userID int64, added bool) {
		kind := events.Tree
		if added {
			kind = events.Entries
		}
		broker.Publish(userID, kind)
	}
	return broker
}

// newServer returns the server of handler. Its requests end with it, not
// with ctx; the streams of events end as soon as it begins to shut down, so
// that it does not wait for them.
func newServer(ctx context.Context, handler http.Handler, broker *events.Broker) *http.Server {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// A request ends with the server, not with the signal: Shutdown
		// gives it time first.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	server.RegisterOnShutdown(broker.Close)
	return server
}

// runServe answers API clients and refreshes the feeds until the process is
// told to stop.
func runServe(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "serve")
	db, err := openStore(ctx, fs, conf, args, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	s, err := newServices(ctx, e, conf, db)
	if err != nil {
		return err
	}
	defer s.close()
	if err := cleanStoredTexts(ctx, db, s.log); err != nil {
		return err
	}
	proxies, err := conf.Proxies()
	if err != nil {
		return err
	}
	// One guard for every way in, so that moving from the interface to the
	// API does not start the count of failed logins anew.
	guard := &authguard.Guard{}
	api := greader.New(greader.Options{
		DB: db, Refresher: s.refresher, Hooks: s.registry, Log: s.log, BaseURL: conf.BaseURL, MediaProxy: true,
		Guard: guard, TrustedProxies: proxies,
	})
	feverAPI := fever.New(fever.Options{
		DB: db, Hooks: s.registry, Log: s.log, Guard: guard, TrustedProxies: proxies,
	})
	// A nil service must not become a handler that is not nil.
	var hubs http.Handler
	if s.webSub != nil {
		hubs = s.webSub
	}
	// A nil sender must not become a mailer that is not nil.
	var mailer web.Mailer
	if conf.SMTPURL != "" {
		sender, err := mail.New(conf.SMTPURL)
		if err != nil {
			return err
		}
		mailer = sender
	}
	broker := newEvents(db)
	pages, err := web.New(web.Options{
		DB: db, Refresher: s.refresher, Hooks: s.registry, Log: s.log, BaseURL: conf.BaseURL, Version: buildVersion(),
		TrustedProxies: proxies, FetchAllowlist: conf.Allowlist(), Mailer: mailer, OIDCClientSecret: conf.OIDCClientSecret, Images: s.images, Services: s.images, Sources: s.images,
		Translator: s.translator, PushKey: conf.VAPIDPublicKey, Guard: guard, Events: broker,
	})
	if err != nil {
		return err
	}
	requests := requestCounter()
	listener, err := net.Listen("tcp", conf.Listen)
	if err != nil {
		return err
	}
	var profiles *http.Server
	if conf.DebugListen != "" {
		debugListener, err := net.Listen("tcp", conf.DebugListen)
		if err != nil {
			_ = listener.Close()
			return err
		}
		profiles = &http.Server{Handler: debugHandler(newMetrics(db, s.refresher, requests, s.log)), ReadHeaderTimeout: 10 * time.Second}
		s.log.Info("handing out profiles and metrics", "address", debugListener.Addr().String())
		go func() {
			if err := profiles.Serve(debugListener); !errors.Is(err, http.ErrServerClosed) {
				s.log.Error("the server of the profiles stopped", "error", err)
			}
		}()
	}
	server := newServer(ctx,
		observe(routes(healthHandler(db.Ping, s.refresher.LastRun, s.log), api, feverAPI, s.icons, extensions(s.registry), hubs, pages),
			s.log, requests),
		broker)
	s.log.Info("listening", "address", listener.Addr().String())

	// The scheduler stops with the server, whichever of the two ends first.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var background sync.WaitGroup
	background.Go(func() { s.refresher.Schedule(ctx, conf.RefreshInterval) })
	if mailer != nil {
		texts, err := i18n.Load()
		if err != nil {
			return err
		}
		digests := digest.New(digest.Options{DB: db, Mailer: mailer, Texts: texts, Log: s.log})
		background.Go(func() { digests.Schedule(ctx, digestInterval) })
	}
	background.Go(func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			s.log.Error("server did not stop cleanly", "error", err)
			_ = server.Close()
		}
		if profiles != nil {
			_ = profiles.Close()
		}
	})
	err = server.Serve(listener)
	cancel()
	background.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
