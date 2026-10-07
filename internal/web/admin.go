package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/mediaproxy"
	"github.com/juev/freshgo/internal/store"
)

// logPage is how many records a page of the journal lists.
const logPage = 100

// administrators lets a page be asked for only by administrators, and only
// shortly after they typed their password: an administrator whose login is
// older than the installation allows is sent to type it again.
func (h *Handler) administrators(page http.HandlerFunc) http.HandlerFunc {
	return h.protect(members, func(w http.ResponseWriter, r *http.Request) {
		s := state(r)
		if !s.who.admin {
			h.fail(w, r, http.StatusForbidden)
			return
		}
		if h.fresh(w, r) {
			page(w, r)
		}
	})
}

// fresh reports whether the administrator who asks typed the password
// recently enough to act as one; when not, the answer is given: the way to
// type it again.
func (h *Handler) fresh(w http.ResponseWriter, r *http.Request) bool {
	if !h.stale(state(r)) {
		return true
	}
	// What a form was about to do is not done: its page is where the
	// administrator comes back to.
	next := r.URL.RequestURI()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		next = "/admin/users"
	}
	http.Redirect(w, r, h.url("/reauth")+"?next="+url.QueryEscape(next), http.StatusSeeOther)
	return false
}

// stale reports whether the user has to type the password again before
// acting as an administrator. Users who are told apart without a login
// have no password to type.
func (h *Handler) stale(s *request) bool {
	if s.who.session == nil || s.system.ReauthTime <= 0 {
		return false
	}
	return h.now().Unix()-s.who.session.Authenticated > int64(s.system.ReauthTime)
}

// reauthPage asks an administrator for the password again.
func (h *Handler) reauthPage(w http.ResponseWriter, r *http.Request) {
	h.showReauth(w, r, http.StatusOK, r.URL.Query().Get("next"), "")
}

func (h *Handler) showReauth(w http.ResponseWriter, r *http.Request, status int, next, problem string) {
	v := h.view(r, "admin", "reauth.heading")
	if problem != "" {
		problem = v.T(problem)
	}
	form := loginForm{Next: next, Error: problem}
	// Signing in again is as good as the password, and all there is for a
	// user who has none.
	if s := state(r); h.oidcOn(s.system) {
		form.Provider, form.ProviderURL = oidcName(s.system), h.oidcStart(next)
	}
	v.Data = form
	h.render(w, r, status, "reauth", v)
}

// reauth takes the password of an administrator and lets them act again.
func (h *Handler) reauth(w http.ResponseWriter, r *http.Request) {
	who := state(r).who
	if !h.form(w, r) {
		return
	}
	next := r.PostForm.Get("next")
	if who.session == nil {
		http.Redirect(w, r, h.localTarget(next), http.StatusSeeOther)
		return
	}
	if !h.confirmed(w, r, func(status int, problem string) { h.showReauth(w, r, status, next, problem) }) {
		return
	}
	if err := h.db.ConfirmSession(r.Context(), who.session.TokenHash, h.now().Unix()); err != nil {
		h.broken(w, r, err)
		return
	}
	http.Redirect(w, r, h.localTarget(next), http.StatusSeeOther)
}

// ---- Users ----

// userRow is a user in the list of users.
type userRow struct {
	Name           string
	URL            string
	Admin, Default bool
	Enabled        bool
	Feeds, Entries int
}

// usersPage is what the page of users shows.
type usersPage struct {
	Users   []userRow
	Name    string
	Problem string
}

func (h *Handler) showUsers(w http.ResponseWriter, r *http.Request, status int, name, problem string) {
	ctx, s := r.Context(), state(r)
	users, err := h.db.Users(ctx)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	totals, err := h.db.UserTotals(ctx)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.adminView(r, "users")
	page := usersPage{Name: name}
	for _, u := range users {
		prefs := readPreferences(u)
		page.Users = append(page.Users, userRow{
			Name: u.Name, URL: h.url("/admin/users/" + u.Name), Default: u.Name == s.system.DefaultUser,
			Admin: prefs.IsAdmin || u.Name == s.system.DefaultUser, Enabled: prefs.enabled(),
			Feeds: totals[u.ID].Feeds, Entries: totals[u.ID].Entries,
		})
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "users", v)
}

func (h *Handler) usersPage(w http.ResponseWriter, r *http.Request) {
	h.showUsers(w, r, http.StatusOK, "", "")
}

// createUser adds a user with one password for the web interface and for
// API clients, as `freshgo user create` does.
func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	if !h.form(w, r) {
		return
	}
	name, password := strings.TrimSpace(r.PostForm.Get("name")), r.PostForm.Get("new")
	problem := passwordProblem(password, r.PostForm.Get("again"))
	if !store.ValidUserName(name) {
		problem = "admin.problem.name"
	}
	if problem != "" {
		h.showUsers(w, r, http.StatusBadRequest, name, problem)
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	settings := attrs{}
	settings.set("passwordHash", hash)
	if r.PostForm.Get("admin") != "" {
		settings.set("is_admin", true)
	}
	if language := r.PostForm.Get("language"); slices.Contains(h.texts.Languages(), language) {
		settings.set("language", language)
	}
	err = h.db.CreateUser(r.Context(), &store.User{Name: name, APIPasswordHash: hash, Settings: settings.raw()})
	switch {
	case errors.Is(err, store.ErrConflict):
		h.showUsers(w, r, http.StatusBadRequest, name, "admin.problem.name-taken")
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.user-added", 0)
		http.Redirect(w, r, h.url("/admin/users"), http.StatusSeeOther)
	}
}

// userPage is what the page of one user shows to an administrator.
type userPage struct {
	Row userRow
	// Self marks the administrator's own account, Protected the accounts an
	// administrator cannot disable, demote or delete: their own and that of
	// the default user.
	Self, Protected bool
	Email           string
	Problem         string
}

// namedUser finds the user a request names.
func (h *Handler) namedUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	name := r.PathValue("name")
	if !store.ValidUserName(name) {
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	}
	u, err := h.userNamed(r.Context(), name)
	if err != nil {
		h.broken(w, r, err)
		return nil, false
	}
	if u == nil {
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	}
	return u, true
}

func (h *Handler) showUser(w http.ResponseWriter, r *http.Request, status int, u *store.User, problem string) {
	s := state(r)
	totals, err := h.db.UserTotals(r.Context())
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.adminView(r, "users")
	prefs := readPreferences(u)
	isDefault := u.Name == s.system.DefaultUser
	page := userPage{
		Row: userRow{
			Name: u.Name, Default: isDefault, Admin: prefs.IsAdmin || isDefault, Enabled: prefs.enabled(),
			Feeds: totals[u.ID].Feeds, Entries: totals[u.ID].Entries,
		},
		Self: u.ID == s.who.user.ID, Protected: isDefault || u.ID == s.who.user.ID,
		Email: readAttrs(u.Settings).text("mail_login"),
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Heading, v.Data = u.Name, page
	h.render(w, r, status, "user", v)
}

func (h *Handler) userPage(w http.ResponseWriter, r *http.Request) {
	if u, ok := h.namedUser(w, r); ok {
		h.showUser(w, r, http.StatusOK, u, "")
	}
}

// updateUser does to a user what the form of an administrator says:
// another password, in or out, administrator or not, or gone.
func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	u, ok := h.namedUser(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	protected := u.Name == s.system.DefaultUser || u.ID == s.who.user.ID
	setting := func(key string, value any) error {
		return h.db.UpdateUserSettings(ctx, u.ID, func(settings map[string]json.RawMessage) error {
			attrs(settings).set(key, value)
			return nil
		})
	}
	var (
		err    error
		notice = "notice.saved"
		target = "/admin/users/" + u.Name
	)
	action := r.PostForm.Get("action")
	if protected && slices.Contains([]string{"disable", "demote", "delete"}, action) {
		h.showUser(w, r, http.StatusBadRequest, u, "admin.problem.protected")
		return
	}
	switch action {
	case "password":
		password := r.PostForm.Get("new")
		if problem := passwordProblem(password, r.PostForm.Get("again")); problem != "" {
			h.showUser(w, r, http.StatusBadRequest, u, problem)
			return
		}
		var hash string
		if hash, err = hashPassword(password); err != nil {
			break
		}
		// One password for the web interface and for API clients, and
		// whoever is logged in with the old one is logged out; the
		// administrator's own login stays.
		keep := ""
		if u.ID == s.who.user.ID && s.who.session != nil {
			keep = s.who.session.TokenHash
		}
		if err = h.setWebPassword(ctx, u.ID, hash, keep); err == nil {
			err = h.db.SetAPIPasswordHash(ctx, u.ID, hash)
		}
		notice = "notice.password"
	case "enable":
		err = setting("enabled", true)
	case "disable":
		if err = setting("enabled", false); err == nil {
			err = h.db.DeleteUserSessions(ctx, u.ID, "")
		}
	case "promote":
		err = setting("is_admin", true)
	case "demote":
		err = setting("is_admin", nil)
	case "delete":
		err = h.removeUser(ctx, u)
		notice, target = "notice.user-deleted", "/admin/users"
	default:
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, notice, 0)
	http.Redirect(w, r, h.url(target), http.StatusSeeOther)
}

// ---- The installation ----

// systemPage is what the pages of the settings of the installation show.
type systemPage struct {
	System    store.System
	Languages []option
	AuthTypes []option
	// MediaModes are the ways the images of entries are handed out.
	MediaModes []option
	// CookieDays and ReauthMinutes spell two periods in the units of their fields.
	CookieDays    int
	ReauthMinutes int
	// Allowlist are the internal addresses feeds may be fetched from, which
	// is set when the server starts.
	Allowlist []string
	// CanMail says the server has an SMTP server to send letters through.
	CanMail bool
	// OIDCCallback is the address a client is registered with at the
	// provider; OIDCSecret says the server was given the secret of one.
	OIDCCallback string
	OIDCSecret   bool
	Problem      string
}

func (h *Handler) showSystem(w http.ResponseWriter, r *http.Request, status int, tab string, system store.System, problem string) {
	v := h.adminView(r, tab)
	page := systemPage{
		System: system, CookieDays: system.Limits.CookieDuration / 86400, ReauthMinutes: system.ReauthTime / 60,
		Allowlist: h.allowlist, CanMail: h.mailer != nil,
		OIDCCallback: h.absolute(r, oidcCallbackPath), OIDCSecret: h.oidcSecret != "",
	}
	for _, code := range h.texts.Languages() {
		page.Languages = append(page.Languages, option{code, v.T("language." + code), code == system.Language})
	}
	for _, mode := range mediaproxy.Modes {
		page.MediaModes = append(page.MediaModes, option{mode, v.T("admin.system.media." + mode), mode == system.MediaProxy})
	}
	for _, kind := range []string{store.AuthForm, store.AuthHTTP, store.AuthNone} {
		page.AuthTypes = append(page.AuthTypes, option{kind, v.T("admin.auth." + kind), kind == system.AuthType})
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, tab, v)
}

func (h *Handler) systemPage(w http.ResponseWriter, r *http.Request) {
	h.showSystem(w, r, http.StatusOK, "system", state(r).system, "")
}

func (h *Handler) authenticationPage(w http.ResponseWriter, r *http.Request) {
	h.showSystem(w, r, http.StatusOK, "authentication", state(r).system, "")
}

// saveSystemWith stores what change makes of the settings of the
// installation, unless it names a problem.
func (h *Handler) saveSystemWith(w http.ResponseWriter, r *http.Request, tab string, change func(form url.Values, system *store.System) string) {
	if !h.form(w, r) {
		return
	}
	var (
		system  store.System
		problem string
	)
	err := h.db.InTx(r.Context(), func(tx *store.Store) error {
		var err error
		if system, err = tx.System(r.Context()); err != nil {
			return err
		}
		if problem = change(r.PostForm, &system); problem != "" {
			return errFormProblem
		}
		return tx.SetSystem(r.Context(), system)
	})
	switch {
	case errors.Is(err, errFormProblem):
		h.showSystem(w, r, http.StatusBadRequest, tab, system, problem)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.saved", 0)
		http.Redirect(w, r, h.url("/admin/"+tab), http.StatusSeeOther)
	}
}

func (h *Handler) saveSystem(w http.ResponseWriter, r *http.Request) {
	h.saveSystemWith(w, r, "system", func(form url.Values, system *store.System) string {
		problem := ""
		number := func(name string, into *int, scale int) {
			n, err := strconv.Atoi(strings.TrimSpace(form.Get(name)))
			if err != nil || n < 0 || n > 1<<31/max(scale, 1) {
				problem = "admin.problem.number"
				return
			}
			*into = n * scale
		}
		if title := strings.TrimSpace(form.Get("title")); title != "" {
			system.Title = title
		}
		if language := form.Get("language"); slices.Contains(h.texts.Languages(), language) {
			system.Language = language
		}
		number("max_feeds", &system.Limits.MaxFeeds, 1)
		number("max_categories", &system.Limits.MaxCategories, 1)
		number("max_registrations", &system.Limits.MaxRegistrations, 1)
		number("cookie_days", &system.Limits.CookieDuration, 86400)
		number("reauth_minutes", &system.ReauthTime, 60)
		if mode := form.Get("media_proxy"); slices.Contains(mediaproxy.Modes, mode) {
			system.MediaProxy = mode
		}
		if proxy, err := fetch.ParseProxy(form.Get("proxy")); err != nil {
			problem = "admin.problem.proxy"
		} else if proxy == nil {
			system.Proxy = ""
		} else {
			system.Proxy = proxy.String()
		}
		system.ClosedRegistrationMessage = strings.TrimSpace(form.Get("closed_registration_message"))
		system.TOS = strings.TrimSpace(form.Get("tos"))
		return problem
	})
}

func (h *Handler) saveAuthentication(w http.ResponseWriter, r *http.Request) {
	who := state(r).who
	h.saveSystemWith(w, r, "authentication", func(form url.Values, system *store.System) string {
		switch kind := form.Get("auth_type"); kind {
		case store.AuthForm, store.AuthHTTP, store.AuthNone:
			system.AuthType = kind
		}
		system.AllowAnonymous = form.Get("allow_anonymous") != ""
		system.AllowAnonymousRefresh = form.Get("allow_anonymous_refresh") != ""
		system.APIEnabled = form.Get("api_enabled") != ""
		system.HTTPAuthAutoRegister = form.Get("http_auth_auto_register") != ""
		system.ForceEmailValidation = form.Get("force_email_validation") != ""
		// Without a server to send letters through nobody could ever
		// confirm an address.
		if system.ForceEmailValidation && h.mailer == nil {
			return "admin.problem.no-mail"
		}
		issuer, client := strings.TrimSpace(form.Get("oidc_issuer")), strings.TrimSpace(form.Get("oidc_client_id"))
		if at, err := url.Parse(issuer); (issuer == "") != (client == "") ||
			(issuer != "" && (err != nil || at.Host == "" || (at.Scheme != "https" && at.Scheme != "http"))) {
			return "admin.problem.oidc"
		}
		system.OIDC = store.OIDC{Issuer: issuer, ClientID: client}
		// A login by password nobody has would lock everybody out, unless
		// there is a provider to sign in through.
		if system.AuthType == store.AuthForm && who.prefs.PasswordHash == "" && !h.oidcOn(*system) {
			return "admin.problem.no-password"
		}
		// So would a way of telling users apart that does not know the
		// administrator who asks for it: the reverse proxy has to vouch for
		// them on this very request, the default user has to be there.
		if system.AuthType != store.AuthForm {
			if after, err := h.identify(r, *system); err != nil || after == nil || after.anonymous || !after.admin {
				return "admin.problem.lockout"
			}
		}
		return ""
	})
}

// ---- The journal ----

// logRow is a record of the journal as its page shows it.
type logRow struct {
	Time, Level, User, Message string
}

// journalPage is what the page of the journal shows.
type journalPage struct {
	Rows  []logRow
	Query string
	// All says the records of every user are listed, which only an
	// administrator may ask for.
	All  bool
	Next string
}

// journal lists the warnings and errors of the server about the user who
// asks, or about everybody for an administrator who asks for all.
func (h *Handler) journal(w http.ResponseWriter, r *http.Request) {
	ctx, who := r.Context(), state(r).who
	params := r.URL.Query()
	all := params.Get("all") != "" && who.admin
	// What is about everybody is the administration's.
	if all && !h.fresh(w, r) {
		return
	}
	var v *view
	if all {
		v = h.adminView(r, "log")
	} else {
		v = h.settingsView(r, "log")
	}
	q := store.LogQuery{Text: strings.TrimSpace(params.Get("q")), Limit: logPage + 1}
	if !all {
		q.User = who.user.Name
	}
	q.Before, _ = strconv.ParseInt(params.Get("before"), 10, 64)
	logs, err := h.db.Logs(ctx, q)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	page := journalPage{Query: q.Text, All: all}
	loc := readReading(who.user).location()
	if len(logs) > logPage {
		logs = logs[:logPage]
		next := url.Values{"before": {strconv.FormatInt(logs[logPage-1].ID, 10)}}
		if q.Text != "" {
			next.Set("q", q.Text)
		}
		if all {
			next.Set("all", "1")
		}
		page.Next = h.url("/log") + "?" + next.Encode()
	}
	for _, l := range logs {
		page.Rows = append(page.Rows, logRow{time.Unix(l.Time, 0).In(loc).Format("2006-01-02 15:04:05"), l.Level, l.User, l.Message})
	}
	v.Data = page
	h.render(w, r, http.StatusOK, "log", v)
}

// clearJournal removes the records about the user who asks, or all of them
// for an administrator who asks for all.
func (h *Handler) clearJournal(w http.ResponseWriter, r *http.Request) {
	who := state(r).who
	if !h.form(w, r) {
		return
	}
	user, target := who.user.Name, "/log"
	if r.PostForm.Get("all") != "" {
		if !who.admin {
			h.fail(w, r, http.StatusForbidden)
			return
		}
		if !h.fresh(w, r) {
			return
		}
		user, target = "", "/log?all=1"
	}
	if err := h.db.DeleteLogs(r.Context(), user); err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.log-cleared", 0)
	http.Redirect(w, r, h.url(target), http.StatusSeeOther)
}
