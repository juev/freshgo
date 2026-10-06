package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web/i18n"
)

//go:embed templates
var templateFiles embed.FS

// pages are the templates by page name, each together with the layout.
type pages map[string]*template.Template

func loadPages() (pages, error) {
	files, err := fs.ReadDir(templateFiles, "templates")
	if err != nil {
		return nil, err
	}
	layout, err := template.ParseFS(templateFiles, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	loaded := pages{}
	for _, f := range files {
		if f.Name() == "layout.html" {
			continue
		}
		page, err := layout.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := page.ParseFS(templateFiles, path.Join("templates", f.Name())); err != nil {
			return nil, err
		}
		loaded[strings.TrimSuffix(f.Name(), ".html")] = page
	}
	return loaded, nil
}

// view is what every page is rendered from. The texts of the reader's
// language are its methods T and N.
type view struct {
	*i18n.Localizer
	h *Handler
	// Site is the name of the installation, Heading the name of the page.
	Site    string
	Heading string
	// Section is the entry of the main menu the page belongs to.
	Section string
	// Theme is "auto", "light" or "dark".
	Theme string
	// User is the name of the user who is logged in, empty for a visitor.
	User string
	// CanLogin and CanLogout say whether the page offers to log in or out:
	// neither makes sense when users are told apart without a login.
	CanLogin, CanLogout bool
	// Data is what the page itself shows.
	Data any
}

// URL and Asset spell addresses for links.
func (v *view) URL(p string) string      { return v.h.url(p) }
func (v *view) Asset(name string) string { return v.h.asset(name) }

// view prepares a page in the language of the reader. heading is the key of
// the text that names the page.
func (h *Handler) view(r *http.Request, section, heading string) *view {
	s := state(r)
	v := &view{h: h, Site: s.system.Title, Section: section, Theme: "auto"}
	language := ""
	if s.who != nil {
		// A visitor reads the entries of the default user, not the
		// interface in that user's language.
		if !s.who.anonymous {
			language = s.who.prefs.Language
			v.User = s.who.user.Name
			v.CanLogout = s.who.session != nil
		}
		v.Theme = s.who.prefs.theme()
	}
	v.CanLogin = v.User == "" && s.system.AuthType == store.AuthForm
	v.Localizer = h.texts.Match(language, r.Header.Get("Accept-Language"), s.system.Language)
	v.Heading = v.T(heading)
	return v
}

// render writes a page. It is rendered in full first, so that a template
// that fails gives an error page instead of half a page.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page string, v *view) {
	var out bytes.Buffer
	if err := h.pages[page].ExecuteTemplate(&out, "layout", v); err != nil {
		h.log.Error("page cannot be rendered", "page", page, "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		// Pages show the reader's own data and change with every action.
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_, _ = out.WriteTo(w)
}
