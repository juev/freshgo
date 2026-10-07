package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/store"
)

// maxName bounds the name of a category, in characters, like that of a label.
const maxName = maxLabelName

// idleAfter is how long a feed has to have brought nothing to be listed
// among the silent ones.
const idleAfter = 30 * 24 * time.Hour

// duration names a period of seconds in the language of the reader.
func duration(v *view, seconds int) string {
	switch {
	case seconds%86400 == 0:
		return v.N("period.days", seconds/86400)
	case seconds%3600 == 0:
		return v.N("period.hours", seconds/3600)
	default:
		return v.N("period.minutes", seconds/60)
	}
}

// feedRow is a feed in a list of subscriptions.
type feedRow struct {
	Name     string
	Address  string
	Settings string
	Read     string
	Failing  bool
	Muted    bool
	// Since is when the feed began to fail, or brought its newest entry.
	Since string
}

// categoryRow is a category in the list of subscriptions.
type categoryRow struct {
	Name     string
	Settings string
	Read     string
	// OPML marks a category that mirrors an OPML document.
	OPML  bool
	Feeds []feedRow
}

// subscriptionsPage is what the page of subscriptions shows.
type subscriptionsPage struct {
	Categories []categoryRow
	Labels     []choice
	Problems   int
}

func (h *Handler) feedRow(f *store.Feed) feedRow {
	id := strconv.FormatInt(f.ID, 10)
	address := f.URL
	if u, err := url.Parse(f.URL); err == nil && u.Host != "" {
		u.User = nil
		address = u.String()
	}
	return feedRow{
		Name: f.Name, Address: address, Settings: h.url("/subscriptions/feeds/" + id), Read: h.url("/feeds/" + id),
		Failing: f.Error != 0, Muted: f.TTL < 0,
	}
}

func (h *Handler) subscriptions(w http.ResponseWriter, r *http.Request) {
	lib, err := h.library(r.Context(), state(r).who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "subscriptions", "sub.heading")
	var page subscriptionsPage
	for _, c := range lib.categories {
		id := strconv.FormatInt(c.ID, 10)
		row := categoryRow{
			Name: c.Name, Settings: h.url("/subscriptions/categories/" + id), Read: h.url("/categories/" + id),
			OPML: refresh.OPMLAddress(c) != "",
		}
		for _, f := range lib.feeds {
			if f.CategoryID == c.ID {
				row.Feeds = append(row.Feeds, h.feedRow(f))
				if f.Error != 0 {
					page.Problems++
				}
			}
		}
		page.Categories = append(page.Categories, row)
	}
	for _, l := range lib.labels {
		page.Labels = append(page.Labels, choice{Name: l.Name, URL: h.url("/subscriptions/labels/" + strconv.FormatInt(l.ID, 10))})
	}
	v.Data = page
	h.render(w, r, http.StatusOK, "subscriptions", v)
}

// addPage is what the page that adds a feed shows.
type addPage struct {
	Address    string
	User       string
	Categories []option
	Kinds      []option
	Problem    string
}

func (h *Handler) showAdd(w http.ResponseWriter, r *http.Request, status int, page addPage, categoryID int64, kind int) {
	v := h.view(r, "subscriptions", "add.heading")
	categories, err := h.db.Categories(r.Context(), state(r).who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	for _, c := range categories {
		page.Categories = append(page.Categories, option{strconv.FormatInt(c.ID, 10), c.Name, c.ID == categoryID})
	}
	for _, k := range kinds {
		page.Kinds = append(page.Kinds, option{strconv.Itoa(k.value), v.T("feed.kind." + k.name), k.value == kind})
	}
	if page.Problem != "" {
		page.Problem = v.T(page.Problem)
	}
	v.Data = page
	h.render(w, r, status, "add", v)
}

func (h *Handler) addPage(w http.ResponseWriter, r *http.Request) {
	categoryID, _ := strconv.ParseInt(r.URL.Query().Get("category"), 10, 64)
	h.showAdd(w, r, http.StatusOK, addPage{Address: r.URL.Query().Get("url")}, cmp.Or(categoryID, store.DefaultCategoryID), refresh.KindRSS)
}

// addFeed subscribes the user to the feed the form names.
func (h *Handler) addFeed(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	f := &store.Feed{URL: strings.TrimSpace(r.PostForm.Get("url")), Priority: store.PriorityMain, CategoryID: store.DefaultCategoryID}
	categories, err := h.db.Categories(ctx, user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if id, err := strconv.ParseInt(r.PostForm.Get("category"), 10, 64); err == nil {
		for _, c := range categories {
			if c.ID == id {
				f.CategoryID = id
			}
		}
	}
	if kind, err := strconv.Atoi(r.PostForm.Get("kind")); err == nil {
		for _, k := range kinds {
			if k.value == kind {
				f.Kind = kind
			}
		}
	}
	name, password := strings.TrimSpace(r.PostForm.Get("http_user")), r.PostForm.Get("http_password")
	if name != "" && password != "" {
		f.HTTPAuth = name + ":" + password
	}
	page := addPage{Address: f.URL, User: name}
	categoryID, kind := f.CategoryID, f.Kind

	err = h.refresher.AddFeed(ctx, user, f)
	switch {
	case err == nil:
		h.notify(w, r, "notice.feed-added", 0)
		http.Redirect(w, r, h.url("/subscriptions/feeds/"+strconv.FormatInt(f.ID, 10)), http.StatusSeeOther)
		return
	case ctx.Err() != nil:
		return
	case errors.Is(err, fetch.ErrBadURL):
		page.Problem = "sub.problem.address"
	case errors.Is(err, refresh.ErrAlreadySubscribed):
		page.Problem = "sub.problem.exists"
	case errors.Is(err, refresh.ErrTooManyFeeds):
		page.Problem = "sub.problem.limit"
	case errors.Is(err, refresh.ErrRefused):
		page.Problem = "sub.problem.refused"
	default:
		// The address does not answer, or not with a feed: the reader's
		// mistake or the site's, not the server's.
		h.log.Warn("subscription failed", "user", user.Name, "error", err)
		page.Problem = "sub.problem.fetch"
	}
	h.showAdd(w, r, http.StatusBadRequest, page, categoryID, kind)
}

// refreshNow fetches the feed of the stream the form names, or the due
// feeds of the user when the stream is not a feed.
func (h *Handler) refreshNow(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	// A visitor reads the feeds of the default user and may ask for them to
	// be fetched only where the installation says so.
	if s.who.anonymous && !s.system.AllowAnonymousRefresh {
		h.fail(w, r, http.StatusForbidden)
		return
	}
	if !h.form(w, r) {
		return
	}
	user := s.who.user
	next := h.localTarget(r.PostForm.Get("next"))
	if rest, ok := strings.CutPrefix(r.PostForm.Get("stream"), "/feeds/"); ok {
		id, err := strconv.ParseInt(rest, 10, 64)
		f, lookup := h.db.FeedByID(ctx, user.ID, id)
		if err != nil || errors.Is(lookup, store.ErrNotFound) {
			h.fail(w, r, http.StatusNotFound)
			return
		}
		if lookup != nil {
			h.broken(w, r, lookup)
			return
		}
		notice, n, err := h.refreshFeed(ctx, user, f)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.broken(w, r, err)
			return
		}
		h.notify(w, r, notice, n)
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	stats, err := h.refresher.RefreshUser(ctx, user, refresh.Options{})
	switch {
	case errors.Is(err, refresh.ErrBusy):
		h.notify(w, r, "notice.refresh-busy", 0)
	case ctx.Err() != nil:
		return
	case err != nil:
		h.broken(w, r, err)
		return
	default:
		h.notify(w, r, "notice.refreshed", stats.NewEntries)
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// problemsPage is what the page of feeds that need a look shows.
type problemsPage struct {
	Failing []feedRow
	Muted   []feedRow
	Idle    []feedRow
}

// problems lists the feeds whose last refresh failed, the muted ones, and
// those that have brought nothing for a month.
func (h *Handler) problems(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	feeds, err := h.db.Feeds(ctx, user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	newest, err := h.db.NewestEntryDates(ctx, user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	loc, now := readReading(user).location(), h.now()
	day := func(seconds int64) string { return time.Unix(seconds, 0).In(loc).Format("2006-01-02") }
	var page problemsPage
	// The feed that has been silent longest comes first.
	slices.SortStableFunc(feeds, func(a, b *store.Feed) int { return cmp.Compare(newest[a.ID], newest[b.ID]) })
	for _, f := range feeds {
		row := h.feedRow(f)
		switch {
		case f.TTL < 0:
			page.Muted = append(page.Muted, row)
		case f.Error != 0:
			row.Since = day(f.Error)
			page.Failing = append(page.Failing, row)
		}
		if last := newest[f.ID]; f.TTL >= 0 && now.Sub(time.Unix(last, 0)) > idleAfter {
			if last != 0 {
				row.Since = day(last)
			} else {
				row.Since = ""
			}
			page.Idle = append(page.Idle, row)
		}
	}
	v := h.view(r, "subscriptions", "problems.heading")
	v.Data = page
	h.render(w, r, http.StatusOK, "problems", v)
}

// ownCategory finds the category a request names among those of the user.
func (h *Handler) ownCategory(w http.ResponseWriter, r *http.Request) (*store.Category, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	}
	categories, err := h.db.Categories(r.Context(), state(r).who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return nil, false
	}
	for _, c := range categories {
		if c.ID == id {
			return c, true
		}
	}
	h.fail(w, r, http.StatusNotFound)
	return nil, false
}

// nameProblem is the key of the text that says why a name cannot be given
// to a category or a label, empty when it can.
func nameProblem(name string, err error) string {
	switch {
	case name == "":
		return "sub.problem.name"
	case utf8.RuneCountInString(name) > maxName:
		return "sub.problem.name-long"
	case errors.Is(err, store.ErrConflict):
		return "sub.problem.name-taken"
	}
	return ""
}

// createCategory adds a category of the name the form gives.
func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) {
	ctx, s := r.Context(), state(r)
	if !h.form(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	back := func(notice string) {
		h.notify(w, r, notice, 0)
		http.Redirect(w, r, h.url("/subscriptions"), http.StatusSeeOther)
	}
	if problem := nameProblem(name, nil); problem != "" {
		back(problem)
		return
	}
	categories, err := h.db.Categories(ctx, s.who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	if limit := s.system.Limits.MaxCategories; limit > 0 && len(categories) >= limit {
		back("notice.category-limit")
		return
	}
	c := &store.Category{UserID: s.who.user.ID, Name: name}
	// A new category lines up after those the user has put in order.
	position, ordered := 0, false
	for _, other := range categories {
		if p, ok := readAttrs(other.Attributes).number("position"); ok {
			position, ordered = max(position, p), true
		}
	}
	if ordered {
		a := attrs{}
		a.set("position", position+1)
		c.Attributes = a.raw()
	}
	switch err := h.db.CreateCategory(ctx, c); {
	case errors.Is(err, store.ErrConflict):
		back("sub.problem.name-taken")
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.category-added", 0)
		http.Redirect(w, r, h.url("/subscriptions/categories/"+strconv.FormatInt(c.ID, 10)), http.StatusSeeOther)
	}
}

// categoryPage is what the page of the settings of a category shows.
type categoryPage struct {
	Category *store.Category
	ReadURL  string
	// Default marks the category that cannot be renamed or deleted.
	Default     bool
	Position    string
	OPML        string
	OPMLFailed  bool
	FiltersRead string
	Retention   retention
	Units       []option
	SameTitleOn bool
	SameTitle   int
	SameGUIDOn  bool
	SameGUID    int
	Sorts       []option
	Orders      []option
	Feeds       int
	Problem     string
}

func (h *Handler) showCategory(w http.ResponseWriter, r *http.Request, status int, c *store.Category, problem string) {
	v := h.view(r, "subscriptions", "category.heading")
	feeds, err := h.db.Feeds(r.Context(), c.UserID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	a := readAttrs(c.Attributes)
	page := categoryPage{
		Category: c, ReadURL: "/categories/" + strconv.FormatInt(c.ID, 10), Default: c.ID == store.DefaultCategoryID,
		OPML: a.text("opml_url"), OPMLFailed: c.Error != 0, FiltersRead: a.filtersFor("read"), Retention: a.retention(),
	}
	if c.Kind != refresh.KindDynamicOPML {
		page.OPML = ""
	}
	if p, ok := a.number("position"); ok {
		page.Position = strconv.Itoa(p)
	}
	for _, unit := range []string{"PT1H", "P1D", "P1W", "P1M", "P1Y"} {
		page.Units = append(page.Units, option{unit, v.T("retention.unit." + unit), unit == page.Retention.PeriodUnit})
	}
	page.SameTitle, page.SameTitleOn = a.number("read_when_same_title_in_category")
	page.SameGUID, page.SameGUIDOn = a.number("read_when_same_guid_in_category")
	sort, order := a.sortChoice()
	page.Sorts, page.Orders = sortOptions(v, sort), orderOptions(v, order)
	for _, f := range feeds {
		if f.CategoryID == c.ID {
			page.Feeds++
		}
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Heading, v.Data = c.Name, page
	h.render(w, r, status, "category", v)
}

func (h *Handler) categoryPage(w http.ResponseWriter, r *http.Request) {
	if c, ok := h.ownCategory(w, r); ok {
		h.showCategory(w, r, http.StatusOK, c, "")
	}
}

// saveCategory stores the settings the form gives for a category.
func (h *Handler) saveCategory(w http.ResponseWriter, r *http.Request) {
	c, ok := h.ownCategory(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	form := r.PostForm
	get := func(name string) string { return strings.TrimSpace(form.Get(name)) }
	problem := ""
	if c.ID != store.DefaultCategoryID {
		if name := get("name"); nameProblem(name, nil) != "" {
			problem = nameProblem(name, nil)
		} else {
			c.Name = name
		}
	}
	a := readAttrs(c.Attributes)
	if position, err := strconv.Atoi(get("position")); err == nil {
		a.set("position", position)
	} else {
		delete(a, "position")
	}
	if address := get("opml_url"); address == "" {
		c.Kind = 0
		delete(a, "opml_url")
	} else if checked, err := refresh.CheckURL(address); err != nil {
		problem = cmp.Or(problem, "sub.problem.address")
	} else {
		if a.text("opml_url") != checked {
			// Another document is read at once, not when the old one was due.
			c.LastUpdate, c.Error = 0, 0
		}
		c.Kind = refresh.KindDynamicOPML
		a.set("opml_url", checked)
	}
	a.setFilters("read", lines(form.Get("filters_read")))
	a.setRetention(form, true)
	for _, key := range []string{"read_when_same_title_in_category", "read_when_same_guid_in_category"} {
		if get(key+"_on") == "" {
			delete(a, key)
			continue
		}
		n, _ := strconv.Atoi(get(key))
		a.set(key, max(n, 0))
	}
	a.setSortChoice(get("default_sort"), get("default_order"))
	c.Attributes = a.raw()

	if problem == "" {
		err := h.db.UpdateCategory(r.Context(), c)
		switch {
		case errors.Is(err, store.ErrConflict):
			problem = "sub.problem.name-taken"
		case errors.Is(err, store.ErrNotFound):
			h.fail(w, r, http.StatusNotFound)
			return
		case err != nil:
			h.broken(w, r, err)
			return
		}
	}
	if problem != "" {
		h.showCategory(w, r, http.StatusBadRequest, c, problem)
		return
	}
	h.notify(w, r, "notice.saved", 0)
	http.Redirect(w, r, h.url("/subscriptions/categories/"+strconv.FormatInt(c.ID, 10)), http.StatusSeeOther)
}

// categoryAction does something to a category and sends the reader on with
// what came of it.
func (h *Handler) categoryAction(act func(ctx context.Context, user *store.User, c *store.Category) (notice string, n int, target string, err error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, user := r.Context(), state(r).who.user
		c, ok := h.ownCategory(w, r)
		if !ok || !h.form(w, r) {
			return
		}
		notice, n, target, err := act(ctx, user, c)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.broken(w, r, err)
			return
		}
		h.notify(w, r, notice, n)
		http.Redirect(w, r, h.url(target), http.StatusSeeOther)
	}
}

// deleteCategory removes a category; its feeds go to the default one.
func (h *Handler) deleteCategory(ctx context.Context, user *store.User, c *store.Category) (string, int, string, error) {
	settings := "/subscriptions/categories/" + strconv.FormatInt(c.ID, 10)
	if c.ID == store.DefaultCategoryID {
		return "notice.category-default", 0, settings, nil
	}
	return "notice.category-deleted", 0, "/subscriptions", h.db.DeleteCategory(ctx, user.ID, c.ID)
}

// emptyCategory unsubscribes the user from every feed of a category.
func (h *Handler) emptyCategory(ctx context.Context, user *store.User, c *store.Category) (string, int, string, error) {
	feeds, err := h.db.Feeds(ctx, user.ID)
	n := 0
	for _, f := range feeds {
		if err != nil || f.CategoryID != c.ID {
			continue
		}
		if err = h.db.DeleteFeed(ctx, user.ID, f.ID); errors.Is(err, store.ErrNotFound) {
			err = nil
		} else if err == nil {
			n++
		}
	}
	return "notice.category-emptied", n, "/subscriptions/categories/" + strconv.FormatInt(c.ID, 10), err
}

// refreshCategoryOPML reads the OPML document of a category now.
func (h *Handler) refreshCategoryOPML(ctx context.Context, user *store.User, c *store.Category) (string, int, string, error) {
	settings := "/subscriptions/categories/" + strconv.FormatInt(c.ID, 10)
	err := h.refresher.RefreshOPML(ctx, user, c.ID)
	switch {
	case err == nil:
		return "notice.opml-refreshed", 0, settings, nil
	case errors.Is(err, refresh.ErrNoOPML):
		return "notice.opml-none", 0, settings, nil
	case errors.Is(err, refresh.ErrTooManyFeeds):
		return "sub.problem.limit", 0, settings, nil
	case errors.Is(err, store.ErrNotFound) || ctx.Err() != nil:
		return "", 0, settings, err
	}
	// The document does not answer, or is not OPML: nothing the server did.
	h.log.Warn("OPML of a category was not read", "user", user.Name, "category", c.ID, "error", err)
	return "notice.opml-failed", 0, settings, nil
}

// ownLabel finds the label a request names among those of the user.
func (h *Handler) ownLabel(w http.ResponseWriter, r *http.Request) (*store.Tag, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	}
	labels, err := h.db.Tags(r.Context(), state(r).who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return nil, false
	}
	for _, l := range labels {
		if l.ID == id {
			return l, true
		}
	}
	h.fail(w, r, http.StatusNotFound)
	return nil, false
}

// createLabel adds a label of the name the form gives.
func (h *Handler) createLabel(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	l := &store.Tag{UserID: user.ID, Name: name}
	var err error
	problem := nameProblem(name, nil)
	if problem == "" {
		err = h.db.CreateTag(ctx, l)
		problem = nameProblem(name, err)
	}
	switch {
	case problem != "":
		h.notify(w, r, problem, 0)
		http.Redirect(w, r, h.url("/subscriptions"), http.StatusSeeOther)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, "notice.label-added", 0)
		http.Redirect(w, r, h.url("/subscriptions/labels/"+strconv.FormatInt(l.ID, 10)), http.StatusSeeOther)
	}
}

// labelPage is what the page of the settings of a label shows.
type labelPage struct {
	Label   *store.Tag
	ReadURL string
	Filters string
	Problem string
}

func (h *Handler) showLabel(w http.ResponseWriter, r *http.Request, status int, l *store.Tag, problem string) {
	v := h.view(r, "subscriptions", "label.heading")
	page := labelPage{Label: l, ReadURL: "/labels/" + strconv.FormatInt(l.ID, 10), Filters: readAttrs(l.Attributes).filtersFor("label")}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Heading, v.Data = l.Name, page
	h.render(w, r, status, "label", v)
}

func (h *Handler) labelPage(w http.ResponseWriter, r *http.Request) {
	if l, ok := h.ownLabel(w, r); ok {
		h.showLabel(w, r, http.StatusOK, l, "")
	}
}

// saveLabel stores the name and the filters the form gives for a label.
func (h *Handler) saveLabel(w http.ResponseWriter, r *http.Request) {
	l, ok := h.ownLabel(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	a := readAttrs(l.Attributes)
	a.setFilters("label", lines(r.PostForm.Get("filters_label")))
	l.Attributes = a.raw()
	problem := nameProblem(name, nil)
	if problem == "" {
		renamed := *l
		renamed.Name = name
		err := h.db.UpdateTag(r.Context(), &renamed)
		switch {
		case errors.Is(err, store.ErrConflict):
			problem = "sub.problem.name-taken"
		case errors.Is(err, store.ErrNotFound):
			h.fail(w, r, http.StatusNotFound)
			return
		case err != nil:
			h.broken(w, r, err)
			return
		}
	}
	if problem != "" {
		l.Name = name
		h.showLabel(w, r, http.StatusBadRequest, l, problem)
		return
	}
	h.notify(w, r, "notice.saved", 0)
	http.Redirect(w, r, h.url("/subscriptions/labels/"+strconv.FormatInt(l.ID, 10)), http.StatusSeeOther)
}

// deleteLabel removes a label and takes it off every entry.
func (h *Handler) deleteLabel(w http.ResponseWriter, r *http.Request) {
	l, ok := h.ownLabel(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	if err := h.db.DeleteTag(r.Context(), l.UserID, l.ID); err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.label-deleted", 0)
	http.Redirect(w, r, h.url("/subscriptions"), http.StatusSeeOther)
}
