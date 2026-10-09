package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	pagebrowser "github.com/juev/freshgo/internal/browser"
	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

// Where a feed is shown, the values of its priority as FreshRSS names them.
var priorities = []struct {
	value int
	name  string
}{{20, "important"}, {store.PriorityMain, "main"}, {0, "category"}, {-5, "feed"}, {-10, "hidden"}}

// What a feed is read as, the values of its kind.
var kinds = []struct {
	value int
	name  string
}{
	{refresh.KindRSS, "rss"}, {refresh.KindRSSForced, "rss-forced"}, {scrape.KindHTMLXPath, "html-xpath"},
	{scrape.KindXMLXPath, "xml-xpath"}, {scrape.KindJSONFeed, "json-feed"}, {scrape.KindJSONDotNotation, "json"},
	{scrape.KindHTMLXPathJSON, "html-json"},
}

// Refresh periods a form offers, in seconds; zero is the period of the user.
var periods = []int{
	0, 900, 1200, 1500, 1800, 2700, 3600, 5400, 7200, 10800, 14400, 18000, 21600, 25200, 28800, 36000, 43200,
	64800, 86400, 129600, 172800, 259200, 345600, 432000, 518400, 604800,
}

// scrapeFields are the settings of a scraped feed: what to take for each
// part of an entry.
var scrapeFields = []string{
	"item", "itemTitle", "itemContent", "itemUri", "itemAuthor", "itemTimestamp", "itemTimeFormat",
	"itemThumbnail", "itemCategories", "itemUid",
}

// Criteria by which the entries of a feed are told apart.
var unicityCriteria = []string{
	"id", "link", "sha1:link_published", "sha1:link_published_title", "sha1:link_published_title_content",
	"sha1:title", "sha1:title_published", "sha1:title_published_content", "sha1:content", "sha1:content_published",
	"sha1:published",
}

// Keys of the request settings of a feed, the numbers of CURLOPT_*, and the
// kinds of proxy freshgo can go through.
const (
	curlPost       = "47"
	curlFollow     = "52"
	curlMaxRedirs  = "68"
	curlProxyType  = "101"
	curlProxy      = "10004"
	curlPostFields = "10015"
	curlUserAgent  = "10018"
	curlCookie     = "10022"
	curlHTTPHeader = "10023"
	curlCookieFile = "10031"
)

// freshgo goes through the first four; the two SOCKS4 kinds are kept for
// feeds that came with them and fail until the kind is changed.
var proxyTypes = []struct{ value, name string }{
	{"0", "HTTP"}, {"2", "HTTPS"}, {"5", "SOCKS5"}, {"7", "SOCKS5H"}, {"4", "SOCKS4"}, {"6", "SOCKS4A"},
}

// proxyDirect is the kind of proxy of a feed that goes through none, the
// proxy of the installation included: CURLPROXY has no such value, and
// FreshRSS stores -1 for it.
const proxyDirect = "-1"

// maxIcon is the largest picture taken for the icon of a feed, in bytes.
const maxIcon = 1 << 20

// scrapeField is one setting of a scraped feed in a form.
type scrapeField struct {
	Name  string
	Label string
	Value string
}

// feedPage is what the page of the settings of a feed shows.
type feedPage struct {
	Feed       *store.Feed
	ReadURL    string
	Categories []option
	Priorities []option
	Kinds      []option
	Periods    []option
	Muted      bool
	Failing    string
	HTTPUser   string
	// Full text.
	Conditions string
	Filter     string
	Automatic  bool
	// Browser is whether the pages are read by the browser of the
	// installation; CanBrowse whether there is one.
	Browser       bool
	CanBrowse     bool
	ContentAction []option
	Preview       template.HTML
	PreviewNote   string
	// Rules.
	FiltersRead  string
	Retention    retention
	Units        []option
	UponGone     string
	UponArrival  string
	UpdatedAgain string
	MaxUnread    string
	SameTitle    string
	// Scraping.
	XPath       []scrapeField
	JSON        []scrapeField
	XPathToJSON string
	// The request.
	ProxyTypes []option
	Proxy      string
	Cookie     string
	CookieFile bool
	Redirects  string
	UserAgent  string
	Post       bool
	PostFields string
	Headers    string
	SSLVerify  string
	Timeout    string
	// The rest.
	Criteria   []option
	Forced     bool
	Sorts      []option
	Orders     []option
	Icon       string
	CustomIcon bool
	Problem    string
}

// ownFeed finds the feed a request names among those of the user.
func (h *Handler) ownFeed(w http.ResponseWriter, r *http.Request) (*store.Feed, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	}
	f, err := h.db.FeedByID(r.Context(), state(r).who.user.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
		return nil, false
	case err != nil:
		h.broken(w, r, err)
		return nil, false
	}
	return f, true
}

// curlText reads a request setting that is a text.
func curlText(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// curlOn tells whether PHP would take a request setting for true.
func curlOn(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "0", `""`, `"0"`, "[]", "{}":
		return false
	}
	return true
}

// headerLines reads the extra headers of a feed, which PHP writes as a list
// or as an object with numbers for keys.
func headerLines(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var keyed map[string]string
	if json.Unmarshal(raw, &keyed) != nil {
		return nil
	}
	keys := make([]string, 0, len(keyed))
	for key := range keyed {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x - y
	})
	for _, key := range keys {
		list = append(list, keyed[key])
	}
	return list
}

// showFeed answers with the page of the settings of a feed as f has them.
func (h *Handler) showFeed(w http.ResponseWriter, r *http.Request, status int, f *store.Feed, page feedPage) {
	ctx, who := r.Context(), state(r).who
	v := h.view(r, "subscriptions", "feed.heading")
	categories, err := h.db.Categories(ctx, who.user.ID)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	a := readAttrs(f.Attributes)
	page.Feed, page.ReadURL = f, "/feeds/"+strconv.FormatInt(f.ID, 10)
	for _, c := range categories {
		page.Categories = append(page.Categories, option{strconv.FormatInt(c.ID, 10), c.Name, c.ID == f.CategoryID})
	}
	for _, p := range priorities {
		page.Priorities = append(page.Priorities, option{strconv.Itoa(p.value), v.T("feed.priority." + p.name), p.value == f.Priority})
	}
	for _, k := range kinds {
		page.Kinds = append(page.Kinds, option{strconv.Itoa(k.value), v.T("feed.kind." + k.name), k.value == f.Kind})
	}
	ttl := max(f.TTL, -f.TTL)
	page.Muted = f.TTL < 0
	if !slices.Contains(periods, ttl) {
		page.Periods = append(page.Periods, option{strconv.Itoa(ttl), duration(v, ttl), true})
	}
	for _, seconds := range periods {
		name := v.T("form.inherit")
		if seconds > 0 {
			name = duration(v, seconds)
		}
		page.Periods = append(page.Periods, option{strconv.Itoa(seconds), name, seconds == ttl})
	}
	if f.Error != 0 {
		page.Failing = time.Unix(f.Error, 0).In(readReading(who.user).location()).Format("2006-01-02 15:04")
	}
	page.HTTPUser, _, _ = strings.Cut(f.HTTPAuth, ":")

	var conditions []string
	_ = json.Unmarshal(a["path_entries_conditions"], &conditions)
	page.Conditions, page.Filter = strings.Join(conditions, "\n"), a.text("path_entries_filter")
	_ = json.Unmarshal(a["path_entries_auto"], &page.Automatic)
	_ = json.Unmarshal(a["page_by_browser"], &page.Browser)
	page.CanBrowse = h.refresher.Browser != nil
	action := a.text("content_action")
	if action != "prepend" && action != "append" {
		action = "replace"
	}
	for _, name := range []string{"replace", "prepend", "append"} {
		page.ContentAction = append(page.ContentAction, option{name, v.T("feed.content-action." + name), name == action})
	}

	page.FiltersRead, page.Retention = a.filtersFor("read"), a.retention()
	for _, unit := range []string{"PT1H", "P1D", "P1W", "P1M", "P1Y"} {
		page.Units = append(page.Units, option{unit, v.T("retention.unit." + unit), unit == page.Retention.PeriodUnit})
	}
	page.UponGone, page.UponArrival = a.ternary("read_upon_gone"), a.ternary("read_upon_reception")
	page.UpdatedAgain = a.ternary("mark_updated_article_unread")
	if n, ok := a.number("keep_max_n_unread"); ok {
		page.MaxUnread = strconv.Itoa(n)
	}
	if n, ok := a.number("read_when_same_title_in_feed"); ok {
		page.SameTitle = strconv.Itoa(n)
	} else if string(a["read_when_same_title_in_feed"]) == "false" {
		page.SameTitle = "0"
	}

	var xpath, dots map[string]string
	_ = json.Unmarshal(a["xpath"], &xpath)
	_ = json.Unmarshal(a["json_dotnotation"], &dots)
	for _, field := range scrapeFields {
		page.XPath = append(page.XPath, scrapeField{"xpath-" + field, v.T("feed.scrape." + field), xpath[field]})
	}
	page.JSON = append(page.JSON, scrapeField{"json-feedTitle", v.T("feed.scrape.feedTitle"), dots["feedTitle"]})
	for _, field := range scrapeFields {
		page.JSON = append(page.JSON, scrapeField{"json-" + field, v.T("feed.scrape." + field), dots[field]})
	}
	page.XPathToJSON = a.text("xPathToJson")

	curl := readAttrs(a["curl_params"])
	proxyType, hasProxy := curl.number(curlProxyType)
	direct := hasProxy && (proxyType < 0 || proxyType == 3)
	page.ProxyTypes = []option{
		{"", v.T("feed.proxy.none"), !hasProxy},
		{proxyDirect, v.T("feed.proxy.direct"), direct},
	}
	for _, p := range proxyTypes {
		page.ProxyTypes = append(page.ProxyTypes, option{p.value, p.name, hasProxy && strconv.Itoa(proxyType) == p.value})
	}
	page.Proxy, page.Cookie = curlText(curl[curlProxy]), curlText(curl[curlCookie])
	_, page.CookieFile = curl[curlCookieFile]
	if n, ok := curl.number(curlMaxRedirs); ok {
		page.Redirects = strconv.Itoa(n)
	} else if raw, set := curl[curlFollow]; set && !curlOn(raw) {
		page.Redirects = "0"
	}
	page.UserAgent, page.Post = curlText(curl[curlUserAgent]), curlOn(curl[curlPost])
	page.PostFields = curlText(curl[curlPostFields])
	page.Headers = strings.Join(headerLines(curl[curlHTTPHeader]), "\n")
	page.SSLVerify = a.ternary("ssl_verify")
	if n, ok := a.number("timeout"); ok && n > 0 {
		page.Timeout = strconv.Itoa(n)
	}

	criteria := a.text("unicityCriteria")
	if criteria == "" {
		criteria = "id"
		if string(a["hasBadGuids"]) == "true" {
			criteria = "link"
		}
	}
	for _, name := range unicityCriteria {
		page.Criteria = append(page.Criteria, option{name, v.T("feed.unicity." + name), name == criteria})
	}
	page.Forced = string(a["unicityCriteriaForced"]) == "true"
	sort, order := a.sortChoice()
	page.Sorts, page.Orders = sortOptions(v, sort), orderOptions(v, order)

	salt, err := h.db.Salt(ctx)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	page.Icon, page.CustomIcon = h.url(favicon.Path+favicon.HashOf(salt, f)), string(a["customFavicon"]) == "true"

	v.Heading, v.Data = f.Name, page
	h.render(w, r, status, "feed", v)
}

func (h *Handler) feedPage(w http.ResponseWriter, r *http.Request) {
	if f, ok := h.ownFeed(w, r); ok {
		h.showFeed(w, r, http.StatusOK, f, feedPage{})
	}
}

// icon is the picture a form brings for a feed, nil when it brings none.
type icon struct {
	content []byte
	reset   bool
}

// applyFeed writes what the form of the settings of a feed says into f. It
// returns the key of the text that says what is wrong, empty when nothing is.
// canBrowse is whether the form asks about reading pages with a browser.
func applyFeed(r *http.Request, f *store.Feed, categories []*store.Category, ttlDefault int, canBrowse bool) string {
	form := r.PostForm
	get := func(name string) string { return strings.TrimSpace(form.Get(name)) }
	number := func(name string) (int, bool) {
		n, err := strconv.Atoi(get(name))
		return n, err == nil
	}
	problem := ""

	if name := get("name"); name != "" {
		f.Name = name
	}
	if address, err := refresh.CheckURL(get("url")); err != nil {
		problem = "sub.problem.address"
		f.URL = get("url")
	} else {
		f.URL = address
	}
	f.Website = ""
	if site, err := url.Parse(get("website")); err == nil && (site.Scheme == "http" || site.Scheme == "https") && site.Host != "" {
		f.Website = get("website")
	}
	f.Description = get("description")
	if id, err := strconv.ParseInt(get("category"), 10, 64); err == nil {
		for _, c := range categories {
			if c.ID == id {
				f.CategoryID = id
			}
		}
	}
	if priority, ok := number("priority"); ok {
		for _, p := range priorities {
			if p.value == priority {
				f.Priority = priority
			}
		}
	}
	if ttl, ok := number("ttl"); ok && ttl >= 0 {
		f.TTL = ttl
	} else {
		f.TTL = max(f.TTL, -f.TTL)
	}
	if get("mute") != "" {
		// A muted feed keeps its period, as FreshRSS stores it: below zero.
		if f.TTL == 0 {
			f.TTL = ttlDefault
		}
		f.TTL = -f.TTL
	}
	// The password is not shown: an empty field keeps the one there is.
	user, password := get("http_user"), form.Get("http_password")
	oldUser, oldPassword, _ := strings.Cut(f.HTTPAuth, ":")
	switch {
	case user == "":
		f.HTTPAuth = ""
	case password != "":
		f.HTTPAuth = user + ":" + password
	case user == oldUser && oldPassword != "":
		f.HTTPAuth = user + ":" + oldPassword
	default:
		f.HTTPAuth = ""
	}
	if kind, ok := number("kind"); ok {
		for _, k := range kinds {
			if k.value == kind {
				f.Kind = kind
			}
		}
	}
	f.PathEntries = get("path_entries")

	a := readAttrs(f.Attributes)
	if get("path_entries_auto") != "" {
		a.set("path_entries_auto", true)
	} else {
		delete(a, "path_entries_auto")
	}
	// Without a browser the form has no word on it, and what is stored stays.
	if canBrowse {
		if get("page_by_browser") != "" {
			a.set("page_by_browser", true)
		} else {
			delete(a, "page_by_browser")
		}
	}
	if conditions := lines(form.Get("path_entries_conditions")); len(conditions) > 0 {
		a.set("path_entries_conditions", conditions)
	} else {
		delete(a, "path_entries_conditions")
	}
	if filter := get("path_entries_filter"); filter != "" {
		a.set("path_entries_filter", filter)
	} else {
		delete(a, "path_entries_filter")
	}
	switch action := get("content_action"); action {
	case "prepend", "append":
		a.set("content_action", action)
	default:
		// Replacing the text of the feed is what happens without a word.
		delete(a, "content_action")
	}

	a.setFilters("read", lines(form.Get("filters_read")))
	a.setRetention(form, true)
	a.setTernary("read_upon_gone", get("read_upon_gone"))
	a.setTernary("read_upon_reception", get("read_upon_reception"))
	a.setTernary("mark_updated_article_unread", get("mark_updated_article_unread"))
	if n, ok := number("keep_max_n_unread"); ok && n >= 0 {
		a.set("keep_max_n_unread", n)
	} else {
		delete(a, "keep_max_n_unread")
	}
	switch n, ok := number("read_when_same_title_in_feed"); {
	case !ok:
		delete(a, "read_when_same_title_in_feed")
	case n <= 0:
		a.set("read_when_same_title_in_feed", false)
	default:
		a.set("read_when_same_title_in_feed", n)
	}

	scraped := func(prefix string, fields []string) map[string]string {
		settings := map[string]string{}
		for _, field := range fields {
			if value := get(prefix + field); value != "" {
				settings[field] = value
			}
		}
		return settings
	}
	switch f.Kind {
	case scrape.KindHTMLXPath, scrape.KindXMLXPath:
		if settings := scraped("xpath-", scrapeFields); len(settings) > 0 {
			a.set("xpath", settings)
		} else {
			delete(a, "xpath")
		}
	case scrape.KindJSONDotNotation, scrape.KindHTMLXPathJSON:
		if settings := scraped("json-", append([]string{"feedTitle"}, scrapeFields...)); len(settings) > 0 {
			a.set("json_dotnotation", settings)
		} else {
			delete(a, "json_dotnotation")
		}
		if path := get("xpath_to_json"); path != "" {
			a.set("xPathToJson", path)
		} else {
			delete(a, "xPathToJson")
		}
	}

	// Request settings the form has no field for stay as they are.
	curl := readAttrs(a["curl_params"])
	for _, key := range []string{
		curlProxyType, curlProxy, curlCookie, curlCookieFile, curlMaxRedirs, curlFollow, curlUserAgent, curlPost,
		curlPostFields, curlHTTPHeader,
	} {
		delete(curl, key)
	}
	if get("proxy_type") == proxyDirect {
		curl.set(curlProxyType, -1)
	} else if proxy := get("proxy"); proxy != "" {
		for _, p := range proxyTypes {
			if p.value == get("proxy_type") {
				kind, _ := strconv.Atoi(p.value)
				curl.set(curlProxyType, kind)
				curl.set(curlProxy, proxy)
			}
		}
	}
	if cookie := get("cookie"); cookie != "" {
		curl.set(curlCookie, cookie)
	}
	if get("cookie_file") != "" {
		curl.set(curlCookieFile, "")
	}
	// Zero is no redirect at all, an empty field the usual number of them.
	if n, ok := number("redirects"); ok {
		curl.set(curlMaxRedirs, n)
		curl.set(curlFollow, n != 0)
	}
	if agent := get("user_agent"); agent != "" {
		curl.set(curlUserAgent, agent)
	}
	headers := lines(form.Get("headers"))
	if get("method") == "POST" {
		curl.set(curlPost, true)
		if fields := form.Get("post_fields"); fields != "" {
			curl.set(curlPostFields, fields)
			if json.Valid([]byte(fields)) && !slices.ContainsFunc(headers, func(line string) bool {
				return strings.HasPrefix(strings.ToLower(line), "content-type:")
			}) {
				headers = append(headers, "Content-Type: application/json")
			}
		}
	}
	if len(headers) > 0 {
		curl.set(curlHTTPHeader, headers)
	}
	if len(curl) > 0 {
		a["curl_params"] = curl.raw()
	} else {
		delete(a, "curl_params")
	}
	a.setTernary("ssl_verify", get("ssl_verify"))
	if n, ok := number("timeout"); ok && n > 0 {
		a.set("timeout", n)
	} else {
		delete(a, "timeout")
	}

	delete(a, "hasBadGuids")
	if criteria := get("unicity"); criteria != "id" && slices.Contains(unicityCriteria, criteria) {
		a.set("unicityCriteria", criteria)
	} else {
		delete(a, "unicityCriteria")
	}
	if get("unicity_forced") != "" {
		a.set("unicityCriteriaForced", true)
	} else {
		delete(a, "unicityCriteriaForced")
	}
	a.setSortChoice(get("default_sort"), get("default_order"))
	f.Attributes = a.raw()
	return problem
}

// uploadedIcon reads the picture the form brings for the feed. The second
// result is the key of the text that says why it cannot be used.
func uploadedIcon(r *http.Request) (*icon, string) {
	if r.PostForm.Get("icon_reset") != "" {
		return &icon{reset: true}, ""
	}
	if r.MultipartForm == nil || len(r.MultipartForm.File["icon"]) == 0 {
		return nil, ""
	}
	header := r.MultipartForm.File["icon"][0]
	if header.Size == 0 {
		return nil, ""
	}
	if header.Size > maxIcon {
		return nil, "sub.problem.icon-large"
	}
	file, err := header.Open()
	if err != nil {
		return nil, "sub.problem.icon"
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxIcon+1))
	if err != nil || favicon.ImageType(content) == "" {
		return nil, "sub.problem.icon"
	}
	return &icon{content: content}, ""
}

var errFormProblem = errors.New("web: the form cannot be stored as it is")

// parsingKeys are the attributes of a feed that decide how it is fetched
// and read.
var parsingKeys = []string{
	"xpath", "json_dotnotation", "xPathToJson", "curl_params", "ssl_verify", "timeout", "unicityCriteria",
	"unicityCriteriaForced", "hasBadGuids", "path_entries_auto", "path_entries_conditions", "path_entries_filter",
	"content_action", "page_by_browser",
}

// sameParsing reports whether two feeds are fetched and read alike.
func sameParsing(a, b *store.Feed) bool {
	if a.URL != b.URL || a.Kind != b.Kind || a.HTTPAuth != b.HTTPAuth || a.PathEntries != b.PathEntries {
		return false
	}
	return sameAttributes(a, b, parsingKeys)
}

// fullTextKeys are the attributes of a feed that decide what text its
// entries get, beside the selector.
var fullTextKeys = []string{"path_entries_auto", "path_entries_conditions", "path_entries_filter", "content_action", "page_by_browser"}

// sameFullText reports whether the entries of two feeds get their text
// the same way.
func sameFullText(a, b *store.Feed) bool {
	return a.PathEntries == b.PathEntries && sameAttributes(a, b, fullTextKeys)
}

// sameAttributes reports whether two feeds have the same values under keys.
func sameAttributes(a, b *store.Feed, keys []string) bool {
	one, other := readAttrs(a.Attributes), readAttrs(b.Attributes)
	for _, key := range keys {
		var x, y any
		_ = json.Unmarshal(one[key], &x)
		_ = json.Unmarshal(other[key], &y)
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// editFeed writes the form of the settings of a feed into the feed and
// returns the key of the text that says what is wrong with the form, if
// anything. db is where the categories of the user are read.
func (h *Handler) editFeed(ctx context.Context, r *http.Request, db *store.Store, f *store.Feed) (problem string, err error) {
	user := state(r).who.user
	categories, err := db.Categories(ctx, user.ID)
	if err != nil {
		return "", err
	}
	ttlDefault := 3600
	if n, ok := readAttrs(user.Settings).number("ttl_default"); ok && n > 0 {
		ttlDefault = n
	}
	before := *f
	problem = applyFeed(r, f, categories, ttlDefault, h.refresher.Browser != nil)
	// The copy the validators stand for was fetched and read otherwise.
	if !sameParsing(&before, f) {
		f.HTTPETag, f.HTTPLastModified = "", ""
	}
	return problem, nil
}

// saveFeed stores the settings the form gives for a feed.
func (h *Handler) saveFeed(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	old, ok := h.ownFeed(w, r)
	if !ok || !h.settingsForm(w, r) {
		return
	}
	picture, iconProblem := uploadedIcon(r)
	salt, err := h.db.Salt(ctx)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	var (
		edited  *store.Feed
		problem string
	)
	err = h.db.InTx(ctx, func(tx *store.Store) error {
		var err error
		if edited, err = tx.LockFeed(ctx, user.ID, old.ID); err != nil {
			return err
		}
		if problem, err = h.editFeed(ctx, r, tx, edited); err != nil {
			return err
		}
		if problem == "" {
			problem = iconProblem
		}
		if problem != "" {
			return errFormProblem
		}
		if picture != nil {
			a := readAttrs(edited.Attributes)
			if picture.reset {
				delete(a, "customFavicon")
				err = tx.DeleteCustomIcon(ctx, user.ID, edited.ID)
			} else {
				a.set("customFavicon", true)
				err = tx.SetCustomIcon(ctx, user.ID, edited.ID, store.CustomIcon{
					Hash: favicon.CustomHash(salt, user.ID, edited.ID), Content: picture.content, Modified: h.now().Unix(),
				})
			}
			if err != nil {
				return err
			}
			edited.Attributes = a.raw()
		}
		return tx.UpdateFeed(ctx, edited)
	})
	switch {
	case errors.Is(err, errFormProblem):
		v := h.view(r, "", "feed.heading")
		h.showFeed(w, r, http.StatusBadRequest, edited, feedPage{Problem: v.T(problem)})
	case errors.Is(err, store.ErrNotFound):
		h.fail(w, r, http.StatusNotFound)
	case err != nil:
		h.broken(w, r, err)
	default:
		notice, n := "notice.saved", 0
		// The entries not read yet get their text the way the feed has it
		// now: the reader does not wait for new ones to see the change.
		if !sameFullText(old, edited) {
			n, err = h.refresher.CompleteUnread(ctx, user, old.ID)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				h.log.Warn("texts of unread entries were not brought in line", "user", user.Name, "feed", old.ID, "error", err)
			}
			notice = "notice.feed-texts"
		}
		// A browser for pages that nothing has read: said, since the box
		// looks like a switch of its own.
		if a := readAttrs(edited.Attributes); h.refresher.Browser != nil && a["page_by_browser"] != nil &&
			strings.TrimSpace(edited.PathEntries) == "" && a["path_entries_auto"] == nil {
			notice, n = "notice.feed-browser-alone", 0
		}
		h.notify(w, r, notice, n)
		http.Redirect(w, r, h.url("/subscriptions/feeds/"+strconv.FormatInt(old.ID, 10)), http.StatusSeeOther)
	}
}

// previewFeed shows what the selector of the form, or the automatic way,
// takes from the page of the newest entry of the feed. Nothing is stored.
func (h *Handler) previewFeed(w http.ResponseWriter, r *http.Request) {
	ctx, user := r.Context(), state(r).who.user
	old, ok := h.ownFeed(w, r)
	if !ok || !h.settingsForm(w, r) {
		return
	}
	edited := old
	if _, err := h.editFeed(ctx, r, h.db, edited); err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.view(r, "", "feed.heading")
	page := feedPage{}
	selector, automatic := strings.TrimSpace(r.PostForm.Get("path_entries")), r.PostForm.Get("path_entries_auto") != ""
	switch {
	case selector == "" && !automatic && h.refresher.Browser != nil && r.PostForm.Get("page_by_browser") != "":
		page.PreviewNote = v.T("feed.preview.browser-alone")
	case selector == "" && !automatic:
		page.PreviewNote = v.T("feed.preview.no-selector")
	default:
		article, err := h.refresher.PreviewArticle(ctx, user, old.ID, selector, automatic, r.PostForm.Get("path_entries_filter"),
			r.PostForm.Get("page_by_browser") != "")
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, refresh.ErrNoEntries):
			page.PreviewNote = v.T("feed.preview.no-entries")
		case errors.Is(err, pagebrowser.ErrChallenge):
			h.log.Warn("full text preview failed", "user", user.Name, "feed", old.ID, "error", err)
			page.PreviewNote = v.T("notice.fulltext-check")
		case err != nil:
			h.log.Warn("full text preview failed", "user", user.Name, "feed", old.ID, "error", err)
			page.PreviewNote = v.T("feed.preview.failed")
		case article == "":
			page.PreviewNote = v.T("feed.preview.empty")
		default:
			page.Preview = template.HTML(article) //nolint:gosec // cleaned by fulltext.Article
		}
	}
	h.showFeed(w, r, http.StatusOK, edited, page)
}

// feedAction does something to a feed and sends the reader back to its
// settings with what came of it.
func (h *Handler) feedAction(act func(ctx context.Context, user *store.User, f *store.Feed) (notice string, n int, err error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, user := r.Context(), state(r).who.user
		f, ok := h.ownFeed(w, r)
		if !ok || !h.form(w, r) {
			return
		}
		notice, n, err := act(ctx, user, f)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.broken(w, r, err)
			return
		}
		h.notify(w, r, notice, n)
		http.Redirect(w, r, h.url("/subscriptions/feeds/"+strconv.FormatInt(f.ID, 10)), http.StatusSeeOther)
	}
}

// refreshed turns the outcome of fetching a feed into a notice: a feed that
// does not answer is no failure of the server.
func (h *Handler) refreshed(ctx context.Context, user *store.User, f *store.Feed, err error) (string, int, error) {
	if err == nil {
		return "notice.feed-refreshed", 0, nil
	}
	// The feed is marked as failing when it is the feed that failed.
	if fresh, lookup := h.db.FeedByID(ctx, user.ID, f.ID); lookup != nil || fresh.Error == 0 {
		return "", 0, err
	}
	h.log.Warn("feed failed", "user", user.Name, "feed", f.ID, "error", err)
	return "notice.feed-failed", 0, nil
}

func (h *Handler) refreshFeed(ctx context.Context, user *store.User, f *store.Feed) (string, int, error) {
	return h.refreshed(ctx, user, f, h.refresher.RefreshFeed(ctx, user, f.ID))
}

// reloadedEntries is how many of the newest entries of a feed have their
// pages read again when the feed is reloaded.
const reloadedEntries = 10

func (h *Handler) reloadFeed(ctx context.Context, user *store.User, f *store.Feed) (string, int, error) {
	return h.refreshed(ctx, user, f, h.refresher.ReloadFeed(ctx, user, f.ID, reloadedEntries))
}

func (h *Handler) truncateFeed(ctx context.Context, user *store.User, f *store.Feed) (string, int, error) {
	n, err := h.db.DeleteFeedEntries(ctx, user.ID, f.ID)
	return "notice.feed-emptied", n, err
}

// deleteFeed removes a feed with its entries.
func (h *Handler) deleteFeed(w http.ResponseWriter, r *http.Request) {
	f, ok := h.ownFeed(w, r)
	if !ok || !h.form(w, r) {
		return
	}
	if err := h.db.DeleteFeed(r.Context(), f.UserID, f.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.broken(w, r, err)
		return
	}
	h.notify(w, r, "notice.feed-deleted", 0)
	http.Redirect(w, r, h.url("/subscriptions"), http.StatusSeeOther)
}
