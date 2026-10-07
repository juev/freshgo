package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// maxSettingsForm bounds the body of a form of settings, in bytes: its
// fields and a file that comes with them.
const maxSettingsForm = 4 << 20

// settingsForm reads the body of a request that changes settings, which may
// carry a file; ok is false when it cannot be read, and the answer is given.
func (h *Handler) settingsForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSettingsForm)
	err := r.ParseMultipartForm(maxSettingsForm)
	if err != nil && !errors.Is(err, http.ErrNotMultipart) || !isText(r.PostForm) {
		h.fail(w, r, http.StatusBadRequest)
		return false
	}
	return true
}

// attrs is a JSON object changed key by key: what a form does not know
// about stays as it is.
type attrs map[string]json.RawMessage

func readAttrs(raw json.RawMessage) attrs {
	var a attrs
	if json.Unmarshal(raw, &a) != nil || a == nil {
		return attrs{}
	}
	return a
}

// set stores a value under a key; nil takes the key out.
func (a attrs) set(key string, value any) {
	if value == nil {
		delete(a, key)
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		// Forms store texts, numbers, booleans and lists of them.
		panic(err)
	}
	a[key] = raw
}

func (a attrs) raw() json.RawMessage {
	raw, err := json.Marshal(a)
	if err != nil {
		panic(err)
	}
	return raw
}

func (a attrs) text(key string) string {
	var s string
	_ = json.Unmarshal(a[key], &s)
	return s
}

// number reads a number, or a text that holds one, as PHP stores either.
func (a attrs) number(key string) (int, bool) {
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(a[key])), `"`))
	return n, err == nil
}

// ternary spells a setting that is on, off or left to the level above the
// way a form does: "1", "0" or "".
func (a attrs) ternary(key string) string {
	var on bool
	if json.Unmarshal(a[key], &on) != nil {
		return ""
	}
	if on {
		return "1"
	}
	return "0"
}

// setTernary stores what a form says about such a setting.
func (a attrs) setTernary(key, value string) {
	switch value {
	case "1":
		a.set(key, true)
	case "0":
		a.set(key, false)
	default:
		delete(a, key)
	}
}

var lineBreak = regexp.MustCompile(`\r\n|\r|\n`)

// lines splits what a textarea holds into its lines that are not empty.
func lines(text string) []string {
	var out []string
	for _, line := range lineBreak.Split(text, -1) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// filter is a filter action as the settings keep it.
type filter struct {
	Search  string   `json:"search"`
	Actions []string `json:"actions"`
}

// filtersFor returns the searches whose matches get an action, one a line.
func (a attrs) filtersFor(action string) string {
	var filters []filter
	_ = json.Unmarshal(a["filters"], &filters)
	var searches []string
	for _, f := range filters {
		for _, has := range f.Actions {
			if has == action {
				searches = append(searches, f.Search)
				break
			}
		}
	}
	return strings.Join(searches, "\n")
}

// setFilters makes the given searches the ones whose matches get an action,
// and leaves the other actions of every search alone.
func (a attrs) setFilters(action string, searches []string) {
	var filters []filter
	_ = json.Unmarshal(a["filters"], &filters)
	wanted := map[string]bool{}
	for _, search := range searches {
		wanted[search] = true
	}
	var kept []filter
	for _, f := range filters {
		var actions []string
		for _, has := range f.Actions {
			if has != action {
				actions = append(actions, has)
			}
		}
		if wanted[f.Search] {
			actions = append(actions, action)
			delete(wanted, f.Search)
		}
		if len(actions) > 0 {
			kept = append(kept, filter{f.Search, actions})
		}
	}
	for _, search := range searches {
		if wanted[search] {
			kept = append(kept, filter{search, []string{action}})
			delete(wanted, search)
		}
	}
	if len(kept) == 0 {
		delete(a, "filters")
		return
	}
	a.set("filters", kept)
}

// retention is the "archiving" value of a feed, a category or a user as a
// form shows it.
type retention struct {
	// Own is false when the level above decides.
	Own bool
	// MaxOn and PeriodOn switch the two rules; Max is a number of entries,
	// Period a count of units, "P1Y", "P1M", "P1W", "P1D" or "PT1H".
	MaxOn       bool
	Max         int
	PeriodOn    bool
	PeriodCount int
	PeriodUnit  string
	Min         int
	Favorites   bool
	Labels      bool
	Unreads     bool
}

var (
	retentionPeriod = regexp.MustCompile(`^(PT?)(\d+)([YMWDH])$`)
	retentionUnit   = regexp.MustCompile(`^PT?1[YMWDH]$`)
)

// Defaults of FreshRSS for a rule that is switched on without a value.
const (
	defaultKeepMax    = 200
	defaultKeepPeriod = "P3M"
)

func (a attrs) retention() retention {
	raw := strings.TrimSpace(string(a["archiving"]))
	if raw == "" || raw[0] != '{' && raw[0] != '[' {
		return retention{PeriodCount: 3, PeriodUnit: "P1M", Max: defaultKeepMax}
	}
	v := readAttrs(a["archiving"])
	p := retention{Own: true, PeriodCount: 3, PeriodUnit: "P1M", Max: defaultKeepMax}
	if m := retentionPeriod.FindStringSubmatch(v.text("keep_period")); m != nil {
		p.PeriodOn = true
		p.PeriodCount, _ = strconv.Atoi(m[2])
		p.PeriodUnit = m[1] + "1" + m[3]
	}
	if n, ok := v.number("keep_max"); ok && n > 0 {
		p.MaxOn, p.Max = true, n
	}
	p.Min, _ = v.number("keep_min")
	_ = json.Unmarshal(v["keep_favourites"], &p.Favorites)
	_ = json.Unmarshal(v["keep_labels"], &p.Labels)
	_ = json.Unmarshal(v["keep_unreads"], &p.Unreads)
	return p
}

// setRetention stores what the fields of a form say about retention. With
// inherit a form may leave the rules to the level above.
func (a attrs) setRetention(form map[string][]string, inherit bool) {
	get := func(name string) string {
		if values := form[name]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	if inherit && get("keep_own") == "" {
		delete(a, "archiving")
		return
	}
	var period, limit any = false, false
	if get("keep_period_on") != "" {
		period = defaultKeepPeriod
		count, err := strconv.Atoi(strings.TrimSpace(get("keep_period_count")))
		if unit := get("keep_period_unit"); err == nil && count > 0 && retentionUnit.MatchString(unit) {
			period = strings.Replace(unit, "1", strconv.Itoa(count), 1)
		}
	}
	if get("keep_max_on") != "" {
		limit = defaultKeepMax
		if n, err := strconv.Atoi(strings.TrimSpace(get("keep_max"))); err == nil && n > 0 {
			limit = n
		}
	}
	keepMin, _ := strconv.Atoi(strings.TrimSpace(get("keep_min")))
	a.set("archiving", map[string]any{
		"keep_period": period, "keep_max": limit, "keep_min": max(keepMin, 0),
		"keep_favourites": get("keep_favourites") != "", "keep_labels": get("keep_labels") != "",
		"keep_unreads": get("keep_unreads") != "",
	})
}

// option is a choice of a select element.
type option struct {
	Value string
	Name  string
	On    bool
}

// sortChoice is the order a feed or a category lists its entries in unless
// the reader asks otherwise, as a form shows it.
func (a attrs) sortChoice() (sort, order string) {
	sort, order = a.text("defaultSort"), strings.ToUpper(a.text("defaultOrder"))
	if _, known := freshRSSOrders[sort]; !known {
		sort = ""
	}
	if order != "ASC" && order != "DESC" {
		order = ""
	}
	return sort, order
}

func (a attrs) setSortChoice(sort, order string) {
	if _, known := freshRSSOrders[sort]; known {
		a.set("defaultSort", sort)
	} else {
		delete(a, "defaultSort")
	}
	if order == "ASC" || order == "DESC" {
		a.set("defaultOrder", order)
	} else {
		delete(a, "defaultOrder")
	}
}

// sortOptions are the orders a form offers, under the names FreshRSS keeps
// them by.
func sortOptions(v *view, current string) []option {
	options := []option{{"", v.T("form.inherit"), current == ""}}
	for _, name := range []string{"id", "date", "title", "f.name", "rand"} {
		options = append(options, option{name, v.T("sort." + freshRSSOrders[name]), current == name})
	}
	return options
}

func orderOptions(v *view, current string) []option {
	return []option{
		{"", v.T("form.inherit"), current == ""},
		{"DESC", v.T("order.desc"), current == "DESC"},
		{"ASC", v.T("order.asc"), current == "ASC"},
	}
}
