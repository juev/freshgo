// Package web is the web interface of freshgo: pages rendered by the server,
// every action a link or a form, with a script on top that makes them
// quicker to use from the keyboard. The contract is docs/specs/web.md.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web/i18n"
)

//go:embed static
var staticFiles embed.FS

// StaticPath is where the stylesheet and the script are served from.
const StaticPath = "/static/"

// Options are what the interface works with.
type Options struct {
	DB  *store.Store
	Log *slog.Logger
	// Refresher fetches feeds when a page asks for it.
	Refresher *refresh.Refresher
	// Hooks are the extension points pages call; nil stands for none.
	Hooks *hooks.Registry
	// BaseURL is the public address of the server, empty when unknown. Its
	// path, if it has one, is where a reverse proxy has put the interface:
	// links start with it, requests arrive without it.
	BaseURL string
	// Version is shown on the about page.
	Version string
	// TrustedProxies are the reverse proxies whose word is taken for who
	// the user is, when the installation tells users apart that way.
	TrustedProxies []netip.Prefix
}

// Handler serves the interface.
type Handler struct {
	db        *store.Store
	log       *slog.Logger
	refresher *refresh.Refresher
	hooks     *hooks.Registry
	baseURL   string
	// prefix is the path of the public address, without a trailing slash.
	prefix  string
	version string
	texts   *i18n.Bundle
	pages   pages
	// assets maps the name of a static file to what its address ends with
	// to tell its versions apart.
	assets  map[string]string
	mux     *http.ServeMux
	proxies []netip.Prefix
	// crossOrigin turns down requests that change something and come from
	// another site.
	crossOrigin *http.CrossOriginProtection
	guard       guard
	decoys      decoys
	now         func() time.Time
}

// New returns the interface, or an error when what is built into the binary
// does not hold together.
func New(o Options) (*Handler, error) {
	h := &Handler{
		db: o.DB, log: o.Log, refresher: o.Refresher, baseURL: o.BaseURL, version: o.Version, assets: map[string]string{},
		proxies: o.TrustedProxies, crossOrigin: http.NewCrossOriginProtection(), now: time.Now, hooks: o.Hooks,
	}
	if h.hooks == nil {
		h.hooks = &hooks.Registry{}
	}
	if o.BaseURL != "" {
		public, err := url.Parse(o.BaseURL)
		if err != nil {
			return nil, err
		}
		h.prefix = strings.TrimSuffix(public.Path, "/")
		// Behind a reverse proxy the Host of a request need not be the
		// one the browser sees; the public address is.
		if err := h.crossOrigin.AddTrustedOrigin(public.Scheme + "://" + public.Host); err != nil {
			return nil, err
		}
	}
	h.crossOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, http.StatusForbidden)
	}))
	var err error
	if h.texts, err = i18n.Load(); err != nil {
		return nil, err
	}
	if h.pages, err = loadPages(); err != nil {
		return nil, err
	}
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	err = fs.WalkDir(static, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(static, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		h.assets[name] = hex.EncodeToString(sum[:6])
		return nil
	})
	if err != nil {
		return nil, err
	}

	h.mux = http.NewServeMux()
	files := http.StripPrefix(StaticPath, http.FileServerFS(static))
	h.mux.HandleFunc("GET "+StaticPath, func(w http.ResponseWriter, r *http.Request) {
		// An address that names the version it wants never changes what it
		// answers with.
		name := strings.TrimPrefix(r.URL.Path, StaticPath)
		if version, known := h.assets[name]; known && r.URL.Query().Get("v") == version {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
	h.mux.HandleFunc("GET /{$}", h.protect(readers, h.reader(streamMain)))
	h.mux.HandleFunc("GET /all", h.protect(readers, h.reader(streamAll)))
	h.mux.HandleFunc("GET /starred", h.protect(readers, h.reader(streamStarred)))
	h.mux.HandleFunc("GET /feeds/{id}", h.protect(readers, h.reader(streamFeed)))
	h.mux.HandleFunc("GET /categories/{id}", h.protect(readers, h.reader(streamCategory)))
	h.mux.HandleFunc("GET /labels/{id}", h.protect(readers, h.reader(streamLabel)))
	h.mux.HandleFunc("GET /entries/{id}", h.protect(readers, h.entry))
	h.mux.HandleFunc("POST /entries/{id}/read", h.protect(members, h.markEntry))
	h.mux.HandleFunc("POST /entries/{id}/star", h.protect(members, h.starEntry))
	h.mux.HandleFunc("POST /entries/{id}/labels", h.protect(members, h.labelEntry))
	h.mux.HandleFunc("POST /read-all", h.protect(members, h.markAll))
	h.mux.HandleFunc("POST /refresh", h.protect(readers, h.refreshNow))
	h.mux.HandleFunc("GET /palette", h.protect(readers, h.palette))
	h.mux.HandleFunc("GET /subscriptions", h.protect(members, h.subscriptions))
	h.mux.HandleFunc("GET /subscriptions/add", h.protect(members, h.addPage))
	h.mux.HandleFunc("GET /subscriptions/problems", h.protect(members, h.problems))
	h.mux.HandleFunc("POST /subscriptions/feeds", h.protect(members, h.addFeed))
	h.mux.HandleFunc("GET /subscriptions/feeds/{id}", h.protect(members, h.feedPage))
	h.mux.HandleFunc("POST /subscriptions/feeds/{id}", h.protect(members, h.saveFeed))
	h.mux.HandleFunc("POST /subscriptions/feeds/{id}/preview", h.protect(members, h.previewFeed))
	h.mux.HandleFunc("POST /subscriptions/feeds/{id}/refresh", h.protect(members, h.feedAction(h.refreshFeed)))
	h.mux.HandleFunc("POST /subscriptions/feeds/{id}/reload", h.protect(members, h.feedAction(h.reloadFeed)))
	h.mux.HandleFunc("POST /subscriptions/feeds/{id}/truncate", h.protect(members, h.feedAction(h.truncateFeed)))
	h.mux.HandleFunc("POST /subscriptions/feeds/{id}/delete", h.protect(members, h.deleteFeed))
	h.mux.HandleFunc("POST /subscriptions/categories", h.protect(members, h.createCategory))
	h.mux.HandleFunc("GET /subscriptions/categories/{id}", h.protect(members, h.categoryPage))
	h.mux.HandleFunc("POST /subscriptions/categories/{id}", h.protect(members, h.saveCategory))
	h.mux.HandleFunc("POST /subscriptions/categories/{id}/delete", h.protect(members, h.categoryAction(h.deleteCategory)))
	h.mux.HandleFunc("POST /subscriptions/categories/{id}/empty", h.protect(members, h.categoryAction(h.emptyCategory)))
	h.mux.HandleFunc("POST /subscriptions/categories/{id}/opml", h.protect(members, h.categoryAction(h.refreshCategoryOPML)))
	h.mux.HandleFunc("POST /subscriptions/labels", h.protect(members, h.createLabel))
	h.mux.HandleFunc("GET /subscriptions/labels/{id}", h.protect(members, h.labelPage))
	h.mux.HandleFunc("POST /subscriptions/labels/{id}", h.protect(members, h.saveLabel))
	h.mux.HandleFunc("POST /subscriptions/labels/{id}/delete", h.protect(members, h.deleteLabel))
	h.mux.HandleFunc("GET /settings/keys", h.protect(members, h.keysPage))
	h.mux.HandleFunc("POST /settings/keys", h.protect(members, h.saveKeys))
	h.mux.HandleFunc("GET /about", h.about)
	h.mux.HandleFunc("GET /login", h.loginPage)
	h.mux.HandleFunc("POST /login", h.login)
	h.mux.HandleFunc("POST /logout", h.logout)
	return h, nil
}

// contentSecurityPolicy lets a page load its own stylesheet and script and
// nothing else that runs; entries bring pictures, sound and frames from
// anywhere.
const contentSecurityPolicy = "default-src 'self'; img-src * data: blob:; media-src *; frame-src *; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "same-origin")

	// Static files are the same for everybody.
	if !strings.HasPrefix(r.URL.Path, StaticPath) {
		system, err := h.db.System(r.Context())
		if err != nil {
			h.broken(w, r, err)
			return
		}
		who, err := h.identify(r, system)
		if err != nil {
			h.broken(w, r, err)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), requestKey{}, &request{system: system, who: who}))
	}
	h.crossOrigin.Handler(http.HandlerFunc(h.route)).ServeHTTP(w, r)
}

// route hands a request to its page.
func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	if !isText(r.URL.Query()) {
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	// The mux answers what it has no page for in plain text; ask it first
	// who would handle the request.
	if _, pattern := h.mux.Handler(r); pattern == "" {
		status := http.StatusNotFound
		if h.knowsPath(r) {
			status = http.StatusMethodNotAllowed
		}
		h.fail(w, r, status)
		return
	}
	h.mux.ServeHTTP(w, r)
}

// knowsPath reports whether the path of a request has a handler for some
// other method.
func (h *Handler) knowsPath(r *http.Request) bool {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if method == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := h.mux.Handler(probe); pattern != "" {
			return true
		}
	}
	return false
}

// url returns the address of a page of the interface as links have to
// spell it.
func (h *Handler) url(p string) string {
	return h.prefix + p
}

// asset returns the address of a static file, which changes when the file does.
func (h *Handler) asset(name string) string {
	return h.url(path.Join(StaticPath, name)) + "?v=" + h.assets[name]
}

func (h *Handler) about(w http.ResponseWriter, r *http.Request) {
	v := h.view(r, "about", "about.heading")
	api := h.baseURL
	if api == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		api = scheme + "://" + r.Host
	}
	v.Data = struct{ Version, API string }{h.version, api}
	h.render(w, r, http.StatusOK, "about", v)
}

// fail answers with the page of an HTTP error.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, status int) {
	key := "error.500"
	switch status {
	case http.StatusBadRequest:
		key = "error.400"
	case http.StatusForbidden:
		key = "error.403"
	case http.StatusNotFound:
		key = "error.404"
	case http.StatusMethodNotAllowed:
		key = "error.405"
	}
	v := h.view(r, "", key+".heading")
	v.Data = struct{ Text string }{v.T(key + ".text")}
	h.render(w, r, status, "error", v)
}

// broken logs what went wrong on the server and answers with the page that
// says so.
func (h *Handler) broken(w http.ResponseWriter, r *http.Request, err error) {
	// The reader went away: nobody is left to answer.
	if r.Context().Err() != nil && errors.Is(err, r.Context().Err()) {
		return
	}
	h.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	h.fail(w, r, http.StatusInternalServerError)
}
