package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/greader"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/web"
	"github.com/juev/freshgo/internal/websub"
)

// shutdownTimeout is how long requests in flight get to finish when the
// server is told to stop.
const shutdownTimeout = 10 * time.Second

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
// the path FreshRSS serves it at. What belongs to no other part is a page of
// the web interface.
//
// hubs is nil when WebSub is off.
func routes(api, icons, misc, hubs, pages http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, favicon.Path):
			icons.ServeHTTP(w, r)
		case path == extensionsPath, strings.HasPrefix(path, extensionsPath+"/"):
			misc.ServeHTTP(w, r)
		case hubs != nil && strings.HasPrefix(path, websub.Path):
			hubs.ServeHTTP(w, r)
		case path == greader.Alias, strings.HasPrefix(path, greader.Alias+"/"),
			strings.HasPrefix(path, "/accounts/"), strings.HasPrefix(path, "/reader/"):
			api.ServeHTTP(w, r)
		default:
			pages.ServeHTTP(w, r)
		}
	})
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
	api := greader.New(greader.Options{
		DB: db, Refresher: s.refresher, Hooks: s.registry, Log: s.log, BaseURL: conf.BaseURL, MediaProxy: true,
	})
	// A nil service must not become a handler that is not nil.
	var hubs http.Handler
	if s.webSub != nil {
		hubs = s.webSub
	}
	proxies, err := conf.Proxies()
	if err != nil {
		return err
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
	pages, err := web.New(web.Options{
		DB: db, Refresher: s.refresher, Hooks: s.registry, Log: s.log, BaseURL: conf.BaseURL, Version: buildVersion(),
		TrustedProxies: proxies, FetchAllowlist: conf.Allowlist(), Mailer: mailer, OIDCClientSecret: conf.OIDCClientSecret, Images: s.client,
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", conf.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           routes(api, s.icons, extensions(s.registry), hubs, pages),
		ReadHeaderTimeout: 10 * time.Second,
		// A request ends with the server, not with the signal: Shutdown
		// gives it time first.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	s.log.Info("listening", "address", listener.Addr().String())

	// The scheduler stops with the server, whichever of the two ends first.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var background sync.WaitGroup
	background.Go(func() { s.refresher.Schedule(ctx, conf.RefreshInterval) })
	background.Go(func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			s.log.Error("server did not stop cleanly", "error", err)
			_ = server.Close()
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
