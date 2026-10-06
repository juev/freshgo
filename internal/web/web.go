// Package web is the web interface of freshgo: pages rendered by the server,
// every action a link or a form, with a script on top that makes them
// quicker to use from the keyboard. The contract is docs/specs/web.md.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"

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
	// BaseURL is the public address of the server, empty when unknown. Its
	// path, if it has one, is where a reverse proxy has put the interface:
	// links start with it, requests arrive without it.
	BaseURL string
	// Version is shown on the about page.
	Version string
}

// Handler serves the interface.
type Handler struct {
	db      *store.Store
	log     *slog.Logger
	baseURL string
	// prefix is the path of the public address, without a trailing slash.
	prefix  string
	version string
	texts   *i18n.Bundle
	pages   pages
	// assets maps the name of a static file to what its address ends with
	// to tell its versions apart.
	assets map[string]string
	mux    *http.ServeMux
}

// New returns the interface, or an error when what is built into the binary
// does not hold together.
func New(o Options) (*Handler, error) {
	h := &Handler{db: o.DB, log: o.Log, baseURL: o.BaseURL, version: o.Version, assets: map[string]string{}}
	if o.BaseURL != "" {
		public, err := url.Parse(o.BaseURL)
		if err != nil {
			return nil, err
		}
		h.prefix = strings.TrimSuffix(public.Path, "/")
	}
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
	h.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, h.url("/about"), http.StatusSeeOther)
	})
	h.mux.HandleFunc("GET /about", h.about)
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
