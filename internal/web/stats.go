package web

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/juev/freshgo/internal/stats"
	"github.com/juev/freshgo/internal/store"
)

// Pages of the statistics.
var statsTabs = []struct{ name, path string }{
	{"overview", "/stats"}, {"repartition", "/stats/repartition"}, {"unread", "/stats/unread"}, {"idle", "/subscriptions/problems"},
}

func (h *Handler) statsView(r *http.Request, tab string) *view {
	v := h.view(r, "stats", "stats."+tab+".heading")
	for _, t := range statsTabs {
		v.Tabs = append(v.Tabs, choice{Name: v.T("stats." + t.name + ".heading"), URL: h.url(t.path), Current: t.name == tab})
	}
	return v
}

// bar is a number of a chart.
type bar struct {
	Label string
	Value int
	// X, Height and Y place the bar in the picture.
	X, Y, Height int
}

// chart is numbers shown as bars and as a table of the same numbers.
type chart struct {
	Title string
	// Unit names what the numbers count, in the head of the table.
	Unit string
	Bars []bar
	// Width and Step are the picture's and a bar's; the picture is
	// chartHeight high.
	Width, Step int
}

const (
	chartHeight = 120
	barWidth    = 18
)

// newChart lays numbers out as bars.
func newChart(title, unit string, labels []string, values []int) chart {
	c := chart{Title: title, Unit: unit, Step: barWidth + 4, Width: len(values) * (barWidth + 4)}
	most := 1
	for _, v := range values {
		most = max(most, v)
	}
	for i, v := range values {
		height := v * chartHeight / most
		if v > 0 {
			// The smallest number is still a bar.
			height = max(height, 2)
		}
		c.Bars = append(c.Bars, bar{Label: labels[i], Value: v, X: i * c.Step, Y: chartHeight - height, Height: height})
	}
	return c
}

// facts reads what the statistics of the user who asks are counted from.
func (h *Handler) facts(r *http.Request) ([]store.EntryFact, stats.Subscriptions, error) {
	ctx, user := r.Context(), state(r).who.user
	var (
		facts []store.EntryFact
		subs  stats.Subscriptions
		err   error
	)
	if subs.Categories, err = h.db.Categories(ctx, user.ID); err != nil {
		return nil, subs, err
	}
	if subs.Feeds, err = h.db.Feeds(ctx, user.ID); err != nil {
		return nil, subs, err
	}
	err = h.db.EntryFacts(ctx, user.ID, func(f store.EntryFact) { facts = append(facts, f) })
	return facts, subs, err
}

// overviewPage is what the first page of the statistics shows.
type overviewPage struct {
	Main, All         stats.Totals
	PerDay            chart
	Average           string
	FeedsByCategory   []stats.Count
	EntriesByCategory []stats.Count
	TopFeeds          []stats.Count
}

func (h *Handler) statsOverview(w http.ResponseWriter, r *http.Request) {
	facts, subs, err := h.facts(r)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.statsView(r, "overview")
	now := h.now().In(readReading(state(r).who.user).location())
	o := stats.NewOverview(facts, subs, now)
	labels, sum := make([]string, stats.Days), 0
	for i := range labels {
		labels[i] = now.AddDate(0, 0, i-stats.Days).Format("2006-01-02")
		sum += o.PerDay[i]
	}
	v.Data = overviewPage{
		Main: o.Main, All: o.All, PerDay: newChart(v.T("stats.per-day"), v.T("stats.entries"), labels, o.PerDay[:]),
		Average:         strconv.FormatFloat(float64(sum)/stats.Days, 'f', 2, 64),
		FeedsByCategory: o.FeedsByCategory, EntriesByCategory: o.EntriesByCategory, TopFeeds: o.TopFeeds,
	}
	h.render(w, r, http.StatusOK, "stats", v)
}

// repartitionPage is what the page of the spread of entries shows.
type repartitionPage struct {
	Feeds                 []option
	Feed                  int64
	Totals                stats.Totals
	Hours, Days, Months   chart
	Hourly, Daily, Monthy string
}

func (h *Handler) statsRepartition(w http.ResponseWriter, r *http.Request) {
	facts, subs, err := h.facts(r)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.statsView(r, "repartition")
	feedID, _ := strconv.ParseInt(r.URL.Query().Get("feed"), 10, 64)
	page := repartitionPage{Feeds: []option{{"", v.T("stats.every-feed"), true}}}
	for _, f := range subs.Feeds {
		on := f.ID == feedID
		if on {
			page.Feed, page.Feeds[0].On = f.ID, false
		}
		page.Feeds = append(page.Feeds, option{strconv.FormatInt(f.ID, 10), f.Name, on})
	}
	rep := stats.NewRepartition(facts, page.Feed, readReading(state(r).who.user).location())
	hours, days, months := make([]string, 24), make([]string, 7), make([]string, 12)
	for i := range hours {
		hours[i] = fmt.Sprintf("%02d", i)
	}
	for i := range days {
		days[i] = v.T("stats.weekday." + strconv.Itoa(i))
	}
	for i := range months {
		months[i] = v.T("stats.month." + strconv.Itoa(i+1))
	}
	number := func(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
	page.Totals = rep.Totals
	page.Hours = newChart(v.T("stats.per-hour"), v.T("stats.entries"), hours, rep.PerHour[:])
	page.Days = newChart(v.T("stats.per-weekday"), v.T("stats.entries"), days, rep.PerWeekday[:])
	page.Months = newChart(v.T("stats.per-month"), v.T("stats.entries"), months, rep.PerMonth[:])
	page.Hourly, page.Daily, page.Monthy = number(rep.Hourly), number(rep.Daily), number(rep.Monthly)
	v.Data = page
	h.render(w, r, http.StatusOK, "repartition", v)
}

// unreadPage is what the page of unread entries by date shows.
type unreadPage struct {
	Granularities []option
	Fields        []option
	Buckets       chart
}

func (h *Handler) statsUnread(w http.ResponseWriter, r *http.Request) {
	facts, subs, err := h.facts(r)
	if err != nil {
		h.broken(w, r, err)
		return
	}
	v := h.statsView(r, "unread")
	params := r.URL.Query()
	granularity := params.Get("by")
	if granularity != stats.ByMonth && granularity != stats.ByYear {
		granularity = stats.ByDay
	}
	received := params.Get("field") != "published"
	var page unreadPage
	for _, name := range []string{stats.ByDay, stats.ByMonth, stats.ByYear} {
		page.Granularities = append(page.Granularities, option{name, v.T("stats.by." + name), name == granularity})
	}
	page.Fields = []option{{"received", v.T("sort.added"), received}, {"published", v.T("sort.published"), !received}}
	buckets := stats.UnreadDates(facts, subs, granularity, received, -10, 100, readReading(state(r).who.user).location())
	labels, values := make([]string, len(buckets)), make([]int, len(buckets))
	for i, b := range buckets {
		labels[i], values[i] = b.Name, b.Unread
	}
	page.Buckets = newChart(v.T("stats.unread.heading"), v.T("stats.unread-entries"), labels, values)
	v.Data = page
	h.render(w, r, http.StatusOK, "unread", v)
}
