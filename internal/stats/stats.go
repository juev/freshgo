// Package stats counts what a user has: entries by state, by category and
// by feed, by the day, hour, weekday and month they are dated, and the days
// with the most unread ones.
//
// The numbers are those of FreshRSS_StatsDAO of FreshRSS at commit 219eaf58,
// counted here over the entries themselves instead of by the database, so
// that both engines give the same and dates follow the time zone of the
// user through its changes of clock. See docs/specs/web.md.
package stats

import (
	"cmp"
	"slices"
	"time"

	"github.com/juev/freshgo/internal/store"
)

// Days is how many days back the entries of each day are counted.
const Days = 30

// Totals are the entries of a set by state.
type Totals struct {
	Total, Unread, Read, Favorites int
}

func (t *Totals) add(f store.EntryFact) {
	t.Total++
	if f.Read {
		t.Read++
	} else {
		t.Unread++
	}
	if f.Favorite {
		t.Favorites++
	}
}

// Count is a number of things under a name.
type Count struct {
	ID    int64
	Name  string
	Count int
	// Category is the category of a feed, empty for other things.
	Category string
}

// Overview is what the first page of statistics shows.
type Overview struct {
	// Main counts the entries of the feeds shown in the main stream, All
	// those of every feed.
	Main, All Totals
	// PerDay counts the entries dated on each of the Days days before
	// today, the oldest first.
	PerDay [Days]int
	// FeedsByCategory and EntriesByCategory count by category, the largest
	// first; TopFeeds are the ten feeds with the most entries.
	FeedsByCategory   []Count
	EntriesByCategory []Count
	TopFeeds          []Count
}

// Subscriptions are the feeds and categories entries are counted by.
type Subscriptions struct {
	Categories []*store.Category
	Feeds      []*store.Feed
}

// byCount sorts the largest first; equal ones keep their order.
func byCount(list []Count) {
	slices.SortStableFunc(list, func(a, b Count) int { return cmp.Compare(b.Count, a.Count) })
}

// NewOverview counts facts for the first page. now says which day is today,
// in the time zone the days are told apart in.
func NewOverview(facts []store.EntryFact, subs Subscriptions, now time.Time) Overview {
	var o Overview
	feeds := make(map[int64]*store.Feed, len(subs.Feeds))
	names := make(map[int64]string, len(subs.Categories))
	feedsIn, entriesIn, entriesOf := map[int64]int{}, map[int64]int{}, map[int64]int{}
	for _, c := range subs.Categories {
		names[c.ID] = c.Name
	}
	for _, f := range subs.Feeds {
		feeds[f.ID] = f
		feedsIn[f.CategoryID]++
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, fact := range facts {
		feed := feeds[fact.FeedID]
		if feed == nil {
			continue
		}
		o.All.add(fact)
		if feed.Priority == store.PriorityMain {
			o.Main.add(fact)
		}
		entriesIn[feed.CategoryID]++
		entriesOf[feed.ID]++
		// The day an entry is dated, counted back from today.
		dated := time.Unix(fact.Published, 0).In(now.Location())
		day := time.Date(dated.Year(), dated.Month(), dated.Day(), 0, 0, 0, 0, now.Location())
		if back := int(midnight.Sub(day).Hours()/24 + 0.5); back >= 1 && back <= Days && dated.Before(midnight) {
			o.PerDay[Days-back]++
		}
	}
	for _, c := range subs.Categories {
		if feedsIn[c.ID] > 0 {
			o.FeedsByCategory = append(o.FeedsByCategory, Count{ID: c.ID, Name: c.Name, Count: feedsIn[c.ID]})
		}
		if entriesIn[c.ID] > 0 {
			o.EntriesByCategory = append(o.EntriesByCategory, Count{ID: c.ID, Name: c.Name, Count: entriesIn[c.ID]})
		}
	}
	for _, f := range subs.Feeds {
		if entriesOf[f.ID] > 0 {
			o.TopFeeds = append(o.TopFeeds, Count{ID: f.ID, Name: f.Name, Count: entriesOf[f.ID], Category: names[f.CategoryID]})
		}
	}
	byCount(o.FeedsByCategory)
	byCount(o.EntriesByCategory)
	byCount(o.TopFeeds)
	if len(o.TopFeeds) > 10 {
		o.TopFeeds = o.TopFeeds[:10]
	}
	return o
}

// Repartition is how the entries of a user, or of one feed, spread over
// the clock and the calendar.
type Repartition struct {
	Totals Totals
	// PerHour counts by the hour of the day, PerWeekday by the day of the
	// week from Sunday, PerMonth by the month from January.
	PerHour    [24]int
	PerWeekday [7]int
	PerMonth   [12]int
	// Hourly, Daily and Monthly are the entries an hour, a day of the week
	// and a month bring on average over the time the entries span.
	Hourly, Daily, Monthly float64
}

// NewRepartition counts the facts of a feed, or of every feed when feedID
// is zero, by the dates they have in loc.
func NewRepartition(facts []store.EntryFact, feedID int64, loc *time.Location) Repartition {
	var (
		r        Repartition
		min, max int64
	)
	for _, fact := range facts {
		if feedID != 0 && fact.FeedID != feedID {
			continue
		}
		if r.Totals.Total == 0 || fact.Published < min {
			min = fact.Published
		}
		if r.Totals.Total == 0 || fact.Published > max {
			max = fact.Published
		}
		r.Totals.add(fact)
		dated := time.Unix(fact.Published, 0).In(loc)
		r.PerHour[dated.Hour()]++
		r.PerWeekday[dated.Weekday()]++
		r.PerMonth[dated.Month()-1]++
	}
	if r.Totals.Total == 0 {
		return r
	}
	// Whole days between the oldest and the newest entry, as FreshRSS
	// counts them; entries of one day stand for one period each.
	days := float64(int64(time.Unix(max, 0).Sub(time.Unix(min, 0)).Hours() / 24))
	average := func(period float64) float64 {
		span := days
		if span <= 0 {
			span = period
		}
		return float64(r.Totals.Total) / (span / period)
	}
	r.Hourly, r.Daily, r.Monthly = average(1.0/24), average(7), average(30)
	return r
}

// Bucket is a stretch of time with the unread entries that fall into it.
type Bucket struct {
	// Name is the day, the month or the year: "2026-10-07", "2026-10", "2026".
	Name   string
	Unread int
}

// Granularities of the stretches unread entries are counted by.
const (
	ByDay   = "day"
	ByMonth = "month"
	ByYear  = "year"
)

// UnreadDates returns the stretches of time with the most unread entries,
// at most limit of them, the fullest first and of equally full ones the
// latest. received counts an entry by when it was received instead of by
// its date. Entries of feeds below minPriority are left out.
func UnreadDates(facts []store.EntryFact, subs Subscriptions, granularity string, received bool, minPriority, limit int, loc *time.Location) []Bucket {
	layout := "2006-01-02"
	switch granularity {
	case ByMonth:
		layout = "2006-01"
	case ByYear:
		layout = "2006"
	}
	priority := make(map[int64]int, len(subs.Feeds))
	for _, f := range subs.Feeds {
		priority[f.ID] = f.Priority
	}
	counts := map[string]int{}
	for _, fact := range facts {
		p, known := priority[fact.FeedID]
		if fact.Read || !known || p < minPriority {
			continue
		}
		at := time.Unix(fact.Published, 0)
		if received {
			at = time.UnixMicro(fact.ID)
		}
		counts[at.In(loc).Format(layout)]++
	}
	buckets := make([]Bucket, 0, len(counts))
	for name, n := range counts {
		buckets = append(buckets, Bucket{name, n})
	}
	slices.SortFunc(buckets, func(a, b Bucket) int {
		return cmp.Or(cmp.Compare(b.Unread, a.Unread), cmp.Compare(b.Name, a.Name))
	})
	if limit > 0 && len(buckets) > limit {
		buckets = buckets[:limit]
	}
	return buckets
}
