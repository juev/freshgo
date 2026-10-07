package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/juev/freshgo/internal/store"
)

// minPassword is the shortest password a user may choose, as in FreshRSS.
const minPassword = 7

// Pages of settings, in the order their menu lists them.
var settingsTabs = []struct{ name, path string }{
	{"display", "/settings/display"}, {"reading", "/settings/reading"}, {"archiving", "/settings/archiving"},
	{"queries", "/settings/queries"}, {"integrations", "/settings/integrations"}, {"keys", "/settings/keys"}, {"privacy", "/settings/privacy"}, {"profile", "/settings/profile"}, {"log", "/log"},
}

// Pages of the administration.
var adminTabs = []struct{ name, path string }{
	{"users", "/admin/users"}, {"system", "/admin/system"}, {"authentication", "/admin/authentication"}, {"log", "/log?all=1"},
}

// settingsView prepares a page of settings with the menu of its section.
func (h *Handler) settingsView(r *http.Request, tab string) *view {
	v := h.view(r, "settings", "settings."+tab+".heading")
	for _, t := range settingsTabs {
		v.Tabs = append(v.Tabs, choice{Name: v.T("settings." + t.name + ".heading"), URL: h.url(t.path), Current: t.name == tab})
	}
	return v
}

// adminView prepares a page of the administration with the menu of its section.
func (h *Handler) adminView(r *http.Request, tab string) *view {
	v := h.view(r, "admin", "admin."+tab+".heading")
	for _, t := range adminTabs {
		v.Tabs = append(v.Tabs, choice{Name: v.T("admin." + t.name + ".heading"), URL: h.url(t.path), Current: t.name == tab})
	}
	return v
}

// Widths of the text of an entry, as FreshRSS names them.
var contentWidths = []string{"thin", "medium", "large", "no_limit"}

// settingsPage is what a page of the settings of a user shows: the settings
// as its form has them, and what is wrong with them.
type settingsPage struct {
	S       attrs
	Problem string
	// Lists of choices, by the name of their field.
	Options map[string][]option
	// Flags and values a template cannot read off the settings itself.
	On   map[string]bool
	Text map[string]string
	// Retention is the form of the rules of keeping entries.
	Retention retention
	Units     []option
}

// userSettings returns the settings of the user who asks.
func userSettings(r *http.Request) attrs {
	return readAttrs(state(r).who.user.Settings)
}

// flag reads a setting that is on or off, def when it is not set.
func (a attrs) flag(key string, def bool) bool {
	var on bool
	if json.Unmarshal(a[key], &on) != nil {
		return def
	}
	return on
}

// saveSettings stores what a form changes in the settings of the user and
// sends the reader back to the page; when change names a problem, nothing
// is stored and show answers with the form as it was sent.
func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request, path string,
	change func(form url.Values, settings attrs) string, show func(status int, settings attrs, problem string)) {
	if !h.form(w, r) {
		return
	}
	var (
		problem string
		edited  attrs
	)
	err := h.db.UpdateUserSettings(r.Context(), state(r).who.user.ID, func(settings map[string]json.RawMessage) error {
		edited = settings
		if problem = change(r.PostForm, settings); problem != "" {
			return errFormProblem
		}
		return nil
	})
	switch {
	case errors.Is(err, errFormProblem):
		show(http.StatusBadRequest, edited, problem)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.saved", 0)
		http.Redirect(w, r, h.url(path), http.StatusSeeOther)
	}
}

// ---- Display ----

func (h *Handler) showDisplay(w http.ResponseWriter, r *http.Request, status int, s attrs, problem string) {
	v := h.settingsView(r, "display")
	page := settingsPage{S: s, Options: map[string][]option{}, On: map[string]bool{}, Text: map[string]string{}}
	language := s.text("language")
	for _, code := range h.texts.Languages() {
		page.Options["language"] = append(page.Options["language"], option{code, v.T("language." + code), code == language})
	}
	mode := preferences{DarkMode: s.text("darkMode")}.theme()
	for _, m := range []struct{ value, theme string }{{"auto", "auto"}, {"no", "light"}, {"dark", "dark"}} {
		page.Options["darkMode"] = append(page.Options["darkMode"], option{m.value, v.T("settings.theme." + m.theme), m.theme == mode})
	}
	width := s.text("content_width")
	if !slices.Contains(contentWidths, width) {
		width = "thin"
	}
	for _, name := range contentWidths {
		page.Options["content_width"] = append(page.Options["content_width"], option{name, v.T("settings.width." + name), name == width})
	}
	page.Text["timezone"] = s.text("timezone")
	page.On["show_feed"] = s.text("topline_website") != "none"
	page.On["show_date"] = s.flag("topline_date", true)
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "display", v)
}

func (h *Handler) displayPage(w http.ResponseWriter, r *http.Request) {
	h.showDisplay(w, r, http.StatusOK, userSettings(r), "")
}

func (h *Handler) saveDisplay(w http.ResponseWriter, r *http.Request) {
	h.saveSettings(w, r, "/settings/display", func(form url.Values, s attrs) string {
		if language := form.Get("language"); slices.Contains(h.texts.Languages(), language) {
			s.set("language", language)
		}
		switch mode := form.Get("darkMode"); mode {
		case "auto", "no", "dark":
			s.set("darkMode", mode)
		}
		if width := form.Get("content_width"); slices.Contains(contentWidths, width) {
			s.set("content_width", width)
		}
		if form.Get("show_feed") != "" {
			s.set("topline_website", "full")
		} else {
			s.set("topline_website", "none")
		}
		s.set("topline_date", form.Get("show_date") != "")
		zone := strings.TrimSpace(form.Get("timezone"))
		s.set("timezone", zone)
		if _, err := time.LoadLocation(zone); err != nil {
			return "settings.problem.timezone"
		}
		return ""
	}, func(status int, s attrs, problem string) { h.showDisplay(w, r, status, s, problem) })
}

// ---- Reading ----

func (h *Handler) showReading(w http.ResponseWriter, r *http.Request, status int, s attrs, problem string) {
	v := h.settingsView(r, "reading")
	page := settingsPage{S: s, Options: map[string][]option{}, On: map[string]bool{}, Text: map[string]string{}}
	prefs := readReading(&store.User{Settings: s.raw()})
	page.Text["posts_per_page"] = strconv.Itoa(prefs.PostsPerPage)
	view := prefs.DefaultView
	if view != "all" && view != "unread" && view != "unread_or_favorite" {
		view = "adaptive"
	}
	for _, name := range []string{"adaptive", "unread", "all", "unread_or_favorite"} {
		page.Options["default_view"] = append(page.Options["default_view"], option{name, v.T("settings.view." + name), name == view})
	}
	sort := prefs.Sort
	if _, known := freshRSSOrders[sort]; !known {
		sort = "id"
	}
	page.Options["sort"] = sortOptions(v, sort)[1:]
	order := "DESC"
	if strings.EqualFold(prefs.SortOrder, "ASC") {
		order = "ASC"
	}
	page.Options["sort_order"] = orderOptions(v, order)[1:]
	when := readAttrs(s["mark_when"])
	page.On["display_posts"] = prefs.DisplayPosts
	page.On["auto_load_more"] = prefs.AutoLoadMore == nil || *prefs.AutoLoadMore
	page.On["hide_read_feeds"] = prefs.HideReadFeeds == nil || *prefs.HideReadFeeds
	page.On["show_fav_unread"] = prefs.ShowFavUnread
	page.On["mark_article"] = when.flag("article", true)
	page.On["mark_reception"] = when.flag("reception", false)
	page.On["mark_gone"] = when.flag("gone", false)
	page.On["mark_updated_article_unread"] = s.flag("mark_updated_article_unread", false)
	if n, ok := when.number("max_n_unread"); ok && n >= 0 {
		page.On["max_n_unread"], page.Text["max_n_unread"] = true, strconv.Itoa(n)
	}
	if n, ok := when.number("same_title_in_feed"); ok && n > 0 {
		page.On["same_title_in_feed"], page.Text["same_title_in_feed"] = true, strconv.Itoa(n)
	} else if when.flag("same_title_in_feed", false) {
		page.On["same_title_in_feed"], page.Text["same_title_in_feed"] = true, "1"
	}
	page.Text["filters_read"], page.Text["filters_star"] = s.filtersFor("read"), s.filtersFor("star")
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "reading", v)
}

func (h *Handler) readingPage(w http.ResponseWriter, r *http.Request) {
	h.showReading(w, r, http.StatusOK, userSettings(r), "")
}

func (h *Handler) saveReading(w http.ResponseWriter, r *http.Request) {
	h.saveSettings(w, r, "/settings/reading", func(form url.Values, s attrs) string {
		on := func(name string) bool { return form.Get(name) != "" }
		number := func(name string) (int, bool) {
			n, err := strconv.Atoi(strings.TrimSpace(form.Get(name)))
			return n, err == nil
		}
		problem := ""
		if n, ok := number("posts_per_page"); ok && n >= 1 && n <= 500 {
			s.set("posts_per_page", n)
		} else {
			problem = "settings.problem.posts-per-page"
		}
		switch view := form.Get("default_view"); view {
		case "adaptive", "unread", "all", "unread_or_favorite":
			s.set("default_view", view)
		}
		if sort := form.Get("sort"); freshRSSOrders[sort] != "" {
			s.set("sort", sort)
		}
		if order := form.Get("sort_order"); order == "ASC" || order == "DESC" {
			s.set("sort_order", order)
		}
		s.set("display_posts", on("display_posts"))
		s.set("auto_load_more", on("auto_load_more"))
		s.set("hide_read_feeds", on("hide_read_feeds"))
		s.set("show_fav_unread", on("show_fav_unread"))
		s.set("mark_updated_article_unread", on("mark_updated_article_unread"))
		// The other ways FreshRSS marks entries read stay as they were set there.
		when := readAttrs(s["mark_when"])
		when.set("article", on("mark_article"))
		when.set("reception", on("mark_reception"))
		when.set("gone", on("mark_gone"))
		when.set("max_n_unread", false)
		if n, ok := number("max_n_unread"); on("max_n_unread_on") && ok && n >= 0 {
			when.set("max_n_unread", n)
		}
		when.set("same_title_in_feed", false)
		if n, ok := number("same_title_in_feed"); on("same_title_in_feed_on") && ok && n > 0 {
			when.set("same_title_in_feed", n)
		}
		s["mark_when"] = when.raw()
		s.setFilters("read", lines(form.Get("filters_read")))
		s.setFilters("star", lines(form.Get("filters_star")))
		return problem
	}, func(status int, s attrs, problem string) { h.showReading(w, r, status, s, problem) })
}

// ---- Archiving ----

func (h *Handler) showArchiving(w http.ResponseWriter, r *http.Request, status int, s attrs) {
	v := h.settingsView(r, "archiving")
	page := settingsPage{S: s, Options: map[string][]option{}, Retention: s.retention()}
	if _, own := s["archiving"]; !own {
		// What FreshRSS gives a user who has set nothing.
		page.Retention = retention{Own: true, PeriodOn: true, PeriodCount: 3, PeriodUnit: "P1M", MaxOn: true, Max: defaultKeepMax, Min: 50, Favorites: true, Labels: true}
	}
	for _, unit := range []string{"PT1H", "P1D", "P1W", "P1M", "P1Y"} {
		page.Units = append(page.Units, option{unit, v.T("retention.unit." + unit), unit == page.Retention.PeriodUnit})
	}
	ttl, ok := s.number("ttl_default")
	if !ok || ttl <= 0 {
		ttl = 3600
	}
	if !slices.Contains(periods, ttl) {
		page.Options["ttl_default"] = append(page.Options["ttl_default"], option{strconv.Itoa(ttl), duration(v, ttl), true})
	}
	for _, seconds := range periods[1:] {
		page.Options["ttl_default"] = append(page.Options["ttl_default"], option{strconv.Itoa(seconds), duration(v, seconds), seconds == ttl})
	}
	v.Data = page
	h.render(w, r, status, "archiving", v)
}

func (h *Handler) archivingPage(w http.ResponseWriter, r *http.Request) {
	h.showArchiving(w, r, http.StatusOK, userSettings(r))
}

func (h *Handler) saveArchiving(w http.ResponseWriter, r *http.Request) {
	h.saveSettings(w, r, "/settings/archiving", func(form url.Values, s attrs) string {
		if ttl, err := strconv.Atoi(form.Get("ttl_default")); err == nil && ttl > 0 {
			s.set("ttl_default", ttl)
		}
		s.setRetention(form, false)
		return ""
	}, func(status int, s attrs, _ string) { h.showArchiving(w, r, status, s) })
}

// purgeNow deletes the entries the rules of the user give up, without
// waiting for the feeds to be refreshed.
func (h *Handler) purgeNow(w http.ResponseWriter, r *http.Request) {
	if !h.form(w, r) {
		return
	}
	stats, err := h.refresher.PurgeUser(r.Context(), state(r).who.user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.purged", stats.Deleted)
	http.Redirect(w, r, h.url("/settings/archiving"), http.StatusSeeOther)
}

// ---- Privacy ----

var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

func (h *Handler) showPrivacy(w http.ResponseWriter, r *http.Request, status int, s attrs, problem string) {
	v := h.settingsView(r, "privacy")
	var hosts []string
	_ = json.Unmarshal(s["send_referrer_allowlist"], &hosts)
	page := settingsPage{S: s, Text: map[string]string{"hosts": strings.Join(hosts, "\n")}}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "privacy", v)
}

func (h *Handler) privacyPage(w http.ResponseWriter, r *http.Request) {
	h.showPrivacy(w, r, http.StatusOK, userSettings(r), "")
}

func (h *Handler) savePrivacy(w http.ResponseWriter, r *http.Request) {
	h.saveSettings(w, r, "/settings/privacy", func(form url.Values, s attrs) string {
		hosts := lines(strings.ToLower(form.Get("hosts")))
		s.set("send_referrer_allowlist", append([]string{}, hosts...))
		for _, host := range hosts {
			if !hostName.MatchString(host) {
				return "settings.problem.host"
			}
		}
		return ""
	}, func(status int, s attrs, problem string) { h.showPrivacy(w, r, status, s, problem) })
}

// ---- Profile ----

// profilePage is what the page of the profile shows.
type profilePage struct {
	Email, Token string
	// Feed and OPML are the addresses the token opens, empty without one.
	Feed, OPML string
	// HasPassword and HasAPI say which passwords the user has; CanDelete is
	// false for the default user, who stays.
	HasPassword, HasAPI, CanDelete bool
	Problem                        string
}

var (
	emailAddress = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)
	tokenText    = regexp.MustCompile(`^[\x21-\x7e]{0,191}$`)
)

func (h *Handler) showProfile(w http.ResponseWriter, r *http.Request, status int, email, token, problem string) {
	s := state(r)
	v := h.settingsView(r, "profile")
	page := profilePage{
		Email: email, Token: token, HasPassword: s.who.prefs.PasswordHash != "", HasAPI: s.who.user.APIPasswordHash != "",
		CanDelete: s.who.user.Name != s.system.DefaultUser,
	}
	if stored := s.who.prefs.Token; stored != "" {
		address := "?" + url.Values{"user": {s.who.user.Name}, "token": {stored}}.Encode()
		page.Feed, page.OPML = h.public("/rss")+address, h.public("/opml")+address
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Data = page
	h.render(w, r, status, "profile", v)
}

func (h *Handler) profilePage(w http.ResponseWriter, r *http.Request) {
	s := userSettings(r)
	h.showProfile(w, r, http.StatusOK, s.text("mail_login"), s.text("token"), "")
}

func (h *Handler) saveProfile(w http.ResponseWriter, r *http.Request) {
	st := state(r)
	// The letter goes out once the settings are stored, not while the
	// database waits for it.
	var mailTo, secret string
	defer func() {
		if secret != "" {
			h.sendValidation(r, h.view(r, "", "validate.heading"), st.who.user.Name, mailTo, secret)
		}
	}()
	h.saveSettings(w, r, "/settings/profile", func(form url.Values, s attrs) string {
		email, token := strings.TrimSpace(form.Get("email")), strings.TrimSpace(form.Get("token"))
		mailTo, secret = "", ""
		// Another address has to be confirmed like the first one, where
		// addresses are confirmed at all.
		if st.system.ForceEmailValidation && !st.who.admin && email != s.text("mail_login") && emailAddress.MatchString(email) && tokenText.MatchString(token) {
			var err error
			if secret, err = newQueryToken(); err != nil {
				return "settings.problem.email"
			}
			mailTo = email
			s.set("email_validation_token", secret)
		}
		if st.system.ForceEmailValidation && !st.who.admin && email == "" {
			return "settings.problem.email"
		}
		s.set("mail_login", email)
		s.set("token", token)
		switch {
		case email != "" && (!emailAddress.MatchString(email) || len(email) > 191):
			return "settings.problem.email"
		case !tokenText.MatchString(token):
			return "settings.problem.token"
		}
		return ""
	}, func(status int, s attrs, problem string) {
		h.showProfile(w, r, status, s.text("mail_login"), s.text("token"), problem)
	})
}

// profileProblem answers with the page of the profile and what is wrong.
func (h *Handler) profileProblem(w http.ResponseWriter, r *http.Request, status int, problem string) {
	s := userSettings(r)
	h.showProfile(w, r, status, s.text("mail_login"), s.text("token"), problem)
}

// hashPassword hashes a password for storing.
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// passwordProblem is the key of the text that says why a password cannot
// be taken, empty when it can. bcrypt reads no more than 72 bytes.
func passwordProblem(password, again string) string {
	switch {
	case len(password) < minPassword:
		return "settings.problem.password-short"
	case len(password) > 72:
		return "settings.problem.password-long"
	case password != again:
		return "settings.problem.password-differs"
	}
	return ""
}

// confirmed reports whether the form carries the web password of the user
// who asks. Guesses are slowed down like those of the login form; when they
// have to wait, or the password is wrong, the answer is given.
func (h *Handler) confirmed(w http.ResponseWriter, r *http.Request, refuse func(status int, problem string)) bool {
	who := state(r).who
	key := h.guardKey(r, who.user.Name)
	if wait := h.guard.attempt(key, h.now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		refuse(http.StatusTooManyRequests, "settings.problem.password-wait")
		return false
	}
	if bcrypt.CompareHashAndPassword([]byte(who.prefs.PasswordHash), []byte(r.PostForm.Get("password"))) != nil {
		refuse(http.StatusForbidden, "settings.problem.password-wrong")
		return false
	}
	h.guard.succeeded(key)
	return true
}

// changePassword gives the user another web password. Whoever has one has
// to type it; every other login of the user ends.
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	ctx, who := r.Context(), state(r).who
	if !h.form(w, r) {
		return
	}
	refuse := func(status int, problem string) { h.profileProblem(w, r, status, problem) }
	if who.prefs.PasswordHash != "" && !h.confirmed(w, r, refuse) {
		return
	}
	password := r.PostForm.Get("new")
	if problem := passwordProblem(password, r.PostForm.Get("again")); problem != "" {
		refuse(http.StatusBadRequest, problem)
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	keep := ""
	if who.session != nil {
		keep = who.session.TokenHash
	}
	if err := h.setWebPassword(ctx, who.user.ID, hash, keep); err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.password", 0)
	http.Redirect(w, r, h.url("/settings/profile"), http.StatusSeeOther)
}

// setWebPassword stores the hash of the web password of a user and ends the
// logins of the user, except the one with the token hash keep.
func (h *Handler) setWebPassword(ctx context.Context, userID int64, hash, keep string) error {
	return h.db.InTx(ctx, func(tx *store.Store) error {
		err := tx.UpdateUserSettings(ctx, userID, func(settings map[string]json.RawMessage) error {
			attrs(settings).set("passwordHash", hash)
			return nil
		})
		if err != nil {
			return err
		}
		return tx.DeleteUserSessions(ctx, userID, keep)
	})
}

// changeAPIPassword gives the user another password for API clients, or
// with an empty one takes the access of API clients away.
func (h *Handler) changeAPIPassword(w http.ResponseWriter, r *http.Request) {
	ctx, who := r.Context(), state(r).who
	if !h.form(w, r) {
		return
	}
	password, hash := r.PostForm.Get("new"), ""
	if password != "" {
		if problem := passwordProblem(password, password); problem != "" {
			h.profileProblem(w, r, http.StatusBadRequest, problem)
			return
		}
		var err error
		if hash, err = hashPassword(password); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	if err := h.db.SetAPIPasswordHash(ctx, who.user.ID, hash); err != nil {
		h.broken(w, r, err)
		return
	}
	notice := "notice.api-password"
	if hash == "" {
		notice = "notice.api-password-removed"
	}
	h.notify(w, r, notice, 0)
	http.Redirect(w, r, h.url("/settings/profile"), http.StatusSeeOther)
}

// deleteAccount removes the user who asks, with everything they have. The
// default user of the installation stays.
func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	if !h.form(w, r) {
		return
	}
	refuse := func(status int, problem string) { h.profileProblem(w, r, status, problem) }
	if s.who.user.Name == s.system.DefaultUser {
		refuse(http.StatusBadRequest, "settings.problem.default-user")
		return
	}
	if s.who.prefs.PasswordHash != "" && !h.confirmed(w, r, refuse) {
		return
	}
	if err := h.removeUser(ctx, s.who.user); err != nil {
		h.broken(w, r, err)
		return
	}
	h.setSession(w, r, "", 0)
	http.Redirect(w, r, h.url("/"), http.StatusSeeOther)
}

// removeUser deletes a user and what the journal says about them.
func (h *Handler) removeUser(ctx context.Context, u *store.User) error {
	if err := h.db.DeleteUser(ctx, u.ID); err != nil {
		return err
	}
	return h.db.DeleteLogs(ctx, u.Name)
}
