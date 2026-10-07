package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// States of entries a saved query asks for, the bits FreshRSS keeps them in.
const (
	stateBitRead        = 1
	stateBitUnread      = 2
	stateBitFavorite    = 4
	stateBitNotFavorite = 8
	stateBitOrUnread    = 32
	stateBitOrFavorite  = 64
)

// savedQuery is a view of entries a user has saved, under the names
// FreshRSS keeps it by in the "queries" setting.
type savedQuery struct {
	Name string `json:"name"`
	// Get names the stream the way FreshRSS does: "a", "s", "T", "f_<id>",
	// "c_<id>", "t_<id>" and their like.
	Get    string `json:"get"`
	Search string `json:"search"`
	State  int    `json:"state"`
	// Order is "ASC" or "DESC", Sort one of the orders FreshRSS names.
	Order string `json:"order"`
	Sort  string `json:"sort"`
	// Token is the secret of the public address of the query; ShareRss
	// opens it as a feed and a page, ShareOpml as OPML and JSON.
	Token     string `json:"token"`
	ShareRss  bool   `json:"shareRss"`
	ShareOpml bool   `json:"shareOpml"`
	// What the feed of the query says of itself and of its entries.
	Description        string `json:"description"`
	ImageURL           string `json:"imageUrl"`
	IncludeUserLabels  bool   `json:"includeUserLabels"`
	ExcludeArticleTags bool   `json:"excludeArticleTags"`
	UserLabelPrefix    string `json:"userLabelPrefix"`
}

// readQueries reads the saved queries of a user. A query that cannot be
// read keeps its place, empty: queries are referred to by position.
func readQueries(u *store.User) []savedQuery {
	var list []json.RawMessage
	if json.Unmarshal(readAttrs(u.Settings)["queries"], &list) != nil {
		return nil
	}
	queries := make([]savedQuery, len(list))
	for i, raw := range list {
		// State and the switches come as numbers and texts from PHP as well.
		var loose struct {
			savedQuery
			State     json.RawMessage `json:"state"`
			ShareRss  json.RawMessage `json:"shareRss"`
			ShareOpml json.RawMessage `json:"shareOpml"`
			Labels    json.RawMessage `json:"includeUserLabels"`
			NoTags    json.RawMessage `json:"excludeArticleTags"`
			Legacy    json.RawMessage `json:"publishLabelsInsteadOfTags"`
		}
		_ = json.Unmarshal(raw, &loose)
		q := loose.savedQuery
		q.State, _ = strconv.Atoi(strings.Trim(string(loose.State), `"`))
		q.ShareRss, q.ShareOpml = curlOn(loose.ShareRss), curlOn(loose.ShareOpml)
		q.IncludeUserLabels = curlOn(loose.Labels) || curlOn(loose.Legacy)
		q.ExcludeArticleTags = curlOn(loose.NoTags) || curlOn(loose.Legacy)
		queries[i] = q
	}
	return queries
}

var getPattern = regexp.MustCompile(`^([aAcfistTZ])(?:_(\d+))?$`)

// queryStream finds the stream a saved query lists; ok is false when the
// query names a feed, a category or a label the user no longer has.
func (lib *library) queryStream(n int, q savedQuery) (s stream, ok bool) {
	s = stream{kind: streamQuery, id: int64(n), name: q.Name, base: q.Search}
	m := getPattern.FindStringSubmatch(q.Get)
	if m == nil {
		m = []string{"", "a", ""}
	}
	id, _ := strconv.ParseInt(m[2], 10, 64)
	priority := func(least int) { s.set.MinPriority = &least }
	switch m[1] {
	case "a":
		priority(priorityMain)
	case "A":
		priority(-5)
	case "Z":
	case "i":
		priority(20)
	case "s":
		s.set.OnlyFavorite = true
	case "T":
		s.set.Labeled = true
	default:
		kind := map[string]string{"f": streamFeed, "c": streamCategory, "t": streamLabel}[m[1]]
		inner, found := lib.stream(kind, id)
		if !found {
			return s, false
		}
		s.set, s.sorting = inner.set, inner.sorting
	}
	if _, known := freshRSSOrders[q.Sort]; known {
		s.Sort = q.Sort
	}
	if order := strings.ToUpper(q.Order); order == "ASC" || order == "DESC" {
		s.Order = order
	}
	s.state = q.State
	return s, true
}

// getOf spells a stream the way FreshRSS names it in a saved query.
func getOf(s stream) string {
	switch s.kind {
	case streamStarred:
		return "s"
	case streamFeed:
		return "f_" + strconv.FormatInt(s.id, 10)
	case streamCategory:
		return "c_" + strconv.FormatInt(s.id, 10)
	case streamLabel:
		return "t_" + strconv.FormatInt(s.id, 10)
	}
	return "a"
}

// stateOfBits is the state of the reading screen nearest to the bits of a
// saved query, empty when the query does not say.
func stateOfBits(bits int) string {
	switch {
	case bits == 0:
		return ""
	case bits&(stateBitOrUnread|stateBitOrFavorite) != 0:
		return stateEither
	case bits&stateBitFavorite != 0:
		return stateStar
	case bits&stateBitUnread != 0 && bits&stateBitRead == 0:
		return stateUnread
	}
	return stateAll
}

// bitsOfState spells a state of the reading screen in the bits of FreshRSS.
func bitsOfState(state string) int {
	switch state {
	case stateUnread:
		return stateBitUnread
	case stateStar:
		return stateBitFavorite
	case stateEither:
		return stateBitUnread | stateBitOrFavorite
	}
	return stateBitRead | stateBitUnread
}

// applyBits narrows a listing to the states the bits of a saved query ask
// for, exactly: a public feed lists what the query says, not the nearest
// state the reading screen has.
func applyBits(l *store.Listing, bits int) {
	yes, no := true, false
	switch {
	case bits&stateBitOrFavorite != 0 && bits&stateBitUnread != 0, bits&stateBitOrUnread != 0 && bits&stateBitFavorite != 0:
		l.UnreadOrFavorite = true
		return
	}
	switch bits & (stateBitRead | stateBitUnread) {
	case stateBitRead:
		l.Read = &yes
	case stateBitUnread:
		l.Read = &no
	}
	switch bits & (stateBitFavorite | stateBitNotFavorite) {
	case stateBitFavorite:
		l.Favorite = &yes
	case stateBitNotFavorite:
		l.Favorite = &no
	}
}

// changeQueries rewrites the list of saved queries of the user who asks.
// Queries are kept as objects, so that what freshgo does not know of one
// stays with it.
func (h *Handler) changeQueries(r *http.Request, change func(list []attrs) ([]attrs, error)) error {
	return h.db.UpdateUserSettings(r.Context(), state(r).who.user.ID, func(settings map[string]json.RawMessage) error {
		var raw []json.RawMessage
		_ = json.Unmarshal(settings["queries"], &raw)
		list := make([]attrs, len(raw))
		for i, one := range raw {
			list[i] = readAttrs(one)
		}
		list, err := change(list)
		if err != nil {
			return err
		}
		attrs(settings).set("queries", append([]attrs{}, list...))
		return nil
	})
}

// newQueryToken returns the secret of a public address: letters and digits,
// which is all FreshRSS takes for one.
func newQueryToken() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

// saveQuery keeps the view the form describes, the one a page of the
// reading screen was showing, under a name.
func (h *Handler) saveQuery(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	if !h.form(w, r) {
		return
	}
	next := h.localTarget(r.PostForm.Get("next"))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if problem := nameProblem(name, nil); problem != "" {
		h.notify(w, r, problem, 0)
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	lib, err := h.library(ctx, user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	s, ok := lib.streamAt(r.PostForm.Get("stream"))
	if !ok {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	prefs := readReading(user)
	showing := show(r.PostForm, prefs, s)
	if _, err := h.parseSearch(showing.query, prefs); err != nil {
		h.fail(w, r, http.StatusBadRequest)
		return
	}
	q := attrs{}
	q.set("name", name)
	q.set("get", getOf(s))
	q.set("state", bitsOfState(showing.state))
	q.set("search", showing.query)
	if s.kind == streamQuery {
		// A view of a saved query is that query, narrowed.
		base := lib.queries[s.id]
		q.set("get", base.Get)
		q.set("search", strings.TrimSpace(base.Search+" "+showing.query))
	}
	for name, spelled := range freshRSSOrders {
		if spelled == showing.sort {
			q.set("sort", name)
		}
	}
	q.set("order", map[bool]string{true: "ASC", false: "DESC"}[showing.asc])
	position := 0
	err = h.changeQueries(r, func(list []attrs) ([]attrs, error) {
		position = len(list)
		return append(list, q), nil
	})
	if err != nil {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.query-saved", 0)
	http.Redirect(w, r, h.url("/settings/queries/"+strconv.Itoa(position)), http.StatusSeeOther)
}

// queryRow is a saved query in the list of them.
type queryRow struct {
	N        int
	Name     string
	Read     string
	Settings string
	Shared   bool
	// Broken marks a query whose feed, category or label is gone.
	Broken bool
}

// queriesPage is what the page of saved queries shows.
type queriesPage struct {
	Queries []queryRow
}

func (h *Handler) queriesPage(w http.ResponseWriter, r *http.Request) {
	user := state(r).who.user
	lib, err := h.library(r.Context(), user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.settingsView(r, "queries")
	var page queriesPage
	for n, q := range lib.queries {
		_, ok := lib.queryStream(n, q)
		page.Queries = append(page.Queries, queryRow{
			N: n, Name: q.Name, Read: h.url("/queries/" + strconv.Itoa(n)), Settings: h.url("/settings/queries/" + strconv.Itoa(n)),
			Shared: q.Token != "" && (q.ShareRss || q.ShareOpml), Broken: !ok,
		})
	}
	v.Data = page
	h.render(w, r, http.StatusOK, "queries", v)
}

// queryPage is what the page of one saved query shows.
type queryPage struct {
	N       int
	Query   savedQuery
	Streams []option
	States  []option
	Sorts   []option
	Orders  []option
	// Links are the public addresses of the query, by format.
	Links   []choice
	Problem string
}

// ownQuery finds the saved query a request names.
func (h *Handler) ownQuery(w http.ResponseWriter, r *http.Request) (int, []savedQuery, bool) {
	queries := readQueries(state(r).who.user)
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= len(queries) {
		h.fail(w, r, http.StatusNotFound)
		return 0, nil, false
	}
	return n, queries, true
}

func (h *Handler) showQuery(w http.ResponseWriter, r *http.Request, status, n int, q savedQuery, problem string) {
	user := state(r).who.user
	lib, err := h.library(r.Context(), user)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.settingsView(r, "queries")
	page := queryPage{N: n, Query: q}
	get := q.Get
	if !getPattern.MatchString(get) {
		get = "a"
	}
	add := func(value, name string) { page.Streams = append(page.Streams, option{value, name, value == get}) }
	add("a", v.T("stream.main"))
	add("A", v.T("query.stream.A"))
	add("Z", v.T("query.stream.Z"))
	add("i", v.T("query.stream.i"))
	add("s", v.T("stream.starred"))
	add("T", v.T("query.stream.T"))
	for _, c := range lib.categories {
		add("c_"+strconv.FormatInt(c.ID, 10), v.T("stream.category")+": "+c.Name)
	}
	for _, f := range lib.feeds {
		add("f_"+strconv.FormatInt(f.ID, 10), v.T("stream.feed")+": "+f.Name)
	}
	for _, l := range lib.labels {
		add("t_"+strconv.FormatInt(l.ID, 10), v.T("stream.label")+": "+l.Name)
	}
	current := stateOfBits(q.State)
	page.States = []option{{"", v.T("form.inherit"), current == ""}}
	for _, name := range []string{stateUnread, stateAll, stateStar, stateEither} {
		page.States = append(page.States, option{name, v.T("state." + name), name == current})
	}
	sort := q.Sort
	if _, known := freshRSSOrders[sort]; !known {
		sort = ""
	}
	order := strings.ToUpper(q.Order)
	if order != "ASC" && order != "DESC" {
		order = ""
	}
	page.Sorts, page.Orders = sortOptions(v, sort), orderOptions(v, order)
	if q.Token != "" {
		for _, format := range sharedFormats {
			if format.opml && q.ShareOpml || !format.opml && q.ShareRss {
				page.Links = append(page.Links, choice{Name: v.T("query.format." + format.name), URL: h.public("/shared/" + q.Token + "." + format.name)})
			}
		}
	}
	if problem != "" {
		page.Problem = v.T(problem)
	}
	v.Heading, v.Data = q.Name, page
	h.render(w, r, status, "query", v)
}

func (h *Handler) queryPage(w http.ResponseWriter, r *http.Request) {
	if n, queries, ok := h.ownQuery(w, r); ok {
		h.showQuery(w, r, http.StatusOK, n, queries[n], "")
	}
}

// public is the address of a page as somebody outside spells it: with the
// public address of the server when it is known.
func (h *Handler) public(path string) string {
	if h.baseURL != "" {
		return strings.TrimSuffix(h.baseURL, "/") + path
	}
	return h.url(path)
}

var errQueryGone = errors.New("web: no such saved query")

// editQuery stores what the form says about a saved query, or does to it
// what the form asks for: deletes it, moves it, gives it another secret.
func (h *Handler) editQuery(w http.ResponseWriter, r *http.Request) {
	user := state(r).who.user
	n, queries, ok := h.ownQuery(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	form := r.PostForm
	get := func(name string) string { return strings.TrimSpace(form.Get(name)) }
	target, notice, problem := "/settings/queries/"+strconv.Itoa(n), "notice.saved", ""
	shown := queries[n]
	token := ""
	if get("action") == "token" || shown.Token == "" && (get("share_rss") != "" || get("share_opml") != "") {
		var err error
		if token, err = newQueryToken(); err != nil {
			h.broken(w, r, err)
			return
		}
	}
	err := h.changeQueries(r, func(list []attrs) ([]attrs, error) {
		if n >= len(list) {
			return nil, errQueryGone
		}
		q := list[n]
		switch get("action") {
		case "delete":
			target, notice = "/settings/queries", "notice.query-deleted"
			return append(list[:n], list[n+1:]...), nil
		case "up", "down":
			other := n - 1
			if get("action") == "down" {
				other = n + 1
			}
			if other >= 0 && other < len(list) {
				list[n], list[other] = list[other], list[n]
			}
			target = "/settings/queries"
			return list, nil
		case "token":
			q.set("token", token)
			notice = "notice.query-token"
			return list, nil
		}
		name := get("name")
		if problem = nameProblem(name, nil); problem == "" {
			q.set("name", name)
		}
		if getPattern.MatchString(get("get")) {
			q.set("get", get("get"))
		}
		q.set("search", get("search"))
		if _, err := h.parseSearch(get("search"), readReading(user)); err != nil {
			problem = searchProblem(err)
		}
		if state := get("state"); state == "" {
			delete(q, "state")
		} else if state == stateUnread || state == stateAll || state == stateStar || state == stateEither {
			// The bits the form cannot show stay when it changes nothing.
			if stateOfBits(shown.State) != state {
				q.set("state", bitsOfState(state))
			}
		}
		if _, known := freshRSSOrders[get("default_sort")]; known {
			q.set("sort", get("default_sort"))
		} else {
			delete(q, "sort")
		}
		if order := get("default_order"); order == "ASC" || order == "DESC" {
			q.set("order", order)
		} else {
			delete(q, "order")
		}
		q.set("shareRss", get("share_rss") != "")
		q.set("shareOpml", get("share_opml") != "")
		if token != "" {
			q.set("token", token)
		}
		q.set("description", get("description"))
		image := get("image_url")
		if address, err := url.Parse(image); image != "" && (err != nil || address.Scheme != "http" && address.Scheme != "https" || address.Host == "") {
			problem = "sub.problem.address"
		}
		q.set("imageUrl", image)
		q.set("includeUserLabels", get("include_labels") != "")
		q.set("excludeArticleTags", get("exclude_tags") != "")
		q.set("userLabelPrefix", form.Get("label_prefix"))
		delete(q, "publishLabelsInsteadOfTags")
		if problem != "" {
			// What the form holds is shown again as it is.
			shown = savedQuery{
				Name: name, Get: get("get"), Search: get("search"), State: bitsOfState(get("state")), Sort: get("default_sort"),
				Order: get("default_order"), Token: shown.Token, ShareRss: get("share_rss") != "", ShareOpml: get("share_opml") != "",
				Description: get("description"), ImageURL: image, IncludeUserLabels: get("include_labels") != "",
				ExcludeArticleTags: get("exclude_tags") != "", UserLabelPrefix: form.Get("label_prefix"),
			}
			return nil, errFormProblem
		}
		return list, nil
	})
	switch {
	case errors.Is(err, errFormProblem):
		h.showQuery(w, r, http.StatusBadRequest, n, shown, problem)
	case errors.Is(err, errQueryGone):
		h.fail(w, r, http.StatusNotFound)
	case err != nil:
		h.broken(w, r, err)
	default:
		h.notify(w, r, notice, 0)
		http.Redirect(w, r, h.url(target), http.StatusSeeOther)
	}
}

// streamSearch reads the search of a page of a stream: what the reader
// typed, and for a saved query what the query itself searches for as well.
func (h *Handler) streamSearch(s stream, typed string, prefs reading) (*search.Query, error) {
	query, err := h.parseSearch(typed, prefs)
	if err != nil || s.base == "" {
		return query, err
	}
	base, err := h.parseSearch(s.base, prefs)
	if err != nil {
		return nil, err
	}
	return search.And(base, query), nil
}
