package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
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
	layout, err := template.New("layout.html").Funcs(template.FuncMap{"dict": dict}).
		ParseFS(templateFiles, "templates/layout.html", "templates/parts.html")
	if err != nil {
		return nil, err
	}
	loaded := pages{}
	for _, f := range files {
		if f.Name() == "layout.html" || f.Name() == "parts.html" {
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
	// Wide lets the page take the width of the window: it has columns of
	// its own.
	Wide bool
	// Notice is what the action before this page has to say.
	Notice string
	// Config is what the script needs to know, as JSON.
	Config string
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
	v.Config = h.config(v, s.who)
	return v
}

// fragment writes a part of the reading screen for the script to put in
// place.
func (h *Handler) fragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	var out bytes.Buffer
	if err := h.pages["reader"].ExecuteTemplate(&out, name, data); err != nil {
		h.log.Error("part of a page cannot be rendered", "part", name, "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = out.WriteTo(w)
}

// render writes a page. It is rendered in full first, so that a template
// that fails gives an error page instead of half a page.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, page string, v *view) {
	v.Notice = h.notice(w, r, v)
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

// noticeCookie carries what an action has to say to the page the reader is
// sent to after it: the key of a text and a number for it.
const noticeCookie = "freshgo_notice"

// notify leaves a notice for the next page.
func (h *Handler) notify(w http.ResponseWriter, r *http.Request, key string, n int) {
	http.SetCookie(w, &http.Cookie{
		Name: noticeCookie, Value: key + ":" + strconv.Itoa(n), Path: h.prefix + "/", MaxAge: 60,
		HttpOnly: true, Secure: h.secure(r), SameSite: http.SameSiteLaxMode,
	})
}

// notice takes the notice left for this page, if there is one.
func (h *Handler) notice(w http.ResponseWriter, r *http.Request, v *view) string {
	cookie, err := r.Cookie(noticeCookie)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: noticeCookie, Path: h.prefix + "/", MaxAge: -1,
		HttpOnly: true, Secure: h.secure(r), SameSite: http.SameSiteLaxMode,
	})
	key, number, _ := strings.Cut(cookie.Value, ":")
	if !strings.HasPrefix(key, "notice.") {
		return ""
	}
	n, _ := strconv.Atoi(number)
	return noticeText(v, key, n)
}

// noticeText is the text of a notice about n things, empty when there is
// no such notice.
func noticeText(v *view, key string, n int) string {
	if text := v.N(key, n); text != key {
		return text
	}
	if text := v.T(key); text != key {
		return text
	}
	return ""
}

// dict lets a template hand several named values to another one.
func dict(pairs ...any) map[string]any {
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if key, ok := pairs[i].(string); ok {
			m[key] = pairs[i+1]
		}
	}
	return m
}
