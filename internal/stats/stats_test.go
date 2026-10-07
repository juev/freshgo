package stats

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

const reference = "../../testdata/reference/"

// phpNames undoes what FreshRSS does to the names it stores.
var phpNames = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`)

// alice reads what the statistics of alice of the reference installation
// are counted from, on one engine.
func alice(t *testing.T, e storetest.Engine) ([]store.EntryFact, Subscriptions) {
	t.Helper()
	ctx := context.Background()
	driver, dsn := e.New(t)
	db, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := importer.Run(ctx, db, importer.Options{DataDir: reference + "sqlite/data"}); err != nil {
		t.Fatal(err)
	}
	u, err := db.UserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var (
		facts []store.EntryFact
		subs  Subscriptions
	)
	if subs.Categories, err = db.Categories(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if subs.Feeds, err = db.Feeds(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.EntryFacts(ctx, u.ID, func(f store.EntryFact) { facts = append(facts, f) }); err != nil {
		t.Fatal(err)
	}
	return facts, subs
}

type phpTotals struct {
	Total     int `json:"total"`
	Unread    int `json:"count_unreads"`
	Read      int `json:"count_reads"`
	Favorites int `json:"count_favorites"`
}

type phpRepartition struct {
	Totals  phpTotals `json:"totals"`
	Hour    []int     `json:"hour"`
	Weekday []int     `json:"weekday"`
	Month   []int     `json:"month"`
	Hourly  float64   `json:"hourly"`
	Daily   float64   `json:"daily"`
	Monthly float64   `json:"monthly"`
}

// R14: the numbers are those FreshRSS 1.30.1 shows for the same
// installation.
func TestMatchesFreshRSS(t *testing.T) {
	var want struct {
		Totals struct {
			Main phpTotals `json:"main_stream"`
			All  phpTotals `json:"all_feeds"`
		} `json:"totals"`
		FeedsByCategory, EntriesByCategory []struct {
			Label string `json:"label"`
			Data  int    `json:"data"`
		}
		TopFeeds []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Category string `json:"category"`
			Count    int    `json:"count"`
		}
		All, Feed phpRepartition
		Unread    map[string][]struct {
			Granularity string `json:"granularity"`
			Unread      int    `json:"unread_count"`
		}
	}
	raw, err := os.ReadFile(reference + "stats/stats.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			facts, subs := alice(t, e)
			// The reference was made on a server in UTC.
			o := NewOverview(facts, subs, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
			totals := func(p phpTotals) Totals { return Totals(p) }
			if o.Main != totals(want.Totals.Main) || o.All != totals(want.Totals.All) || o.All.Total == 0 {
				t.Errorf("totals = %+v and %+v, want %+v", o.Main, o.All, want.Totals)
			}
			// Equal counts come in an order FreshRSS leaves to the database.
			sorted := func(list []Count) []Count {
				out := append([]Count{}, list...)
				sort.SliceStable(out, func(a, b int) bool {
					return out[a].Count > out[b].Count || out[a].Count == out[b].Count && out[a].Name < out[b].Name
				})
				for i := range out {
					out[i].ID = 0
				}
				return out
			}
			var feedsBy, entriesBy, top []Count
			for _, c := range want.FeedsByCategory {
				feedsBy = append(feedsBy, Count{Name: phpNames.Replace(c.Label), Count: c.Data})
			}
			for _, c := range want.EntriesByCategory {
				entriesBy = append(entriesBy, Count{Name: phpNames.Replace(c.Label), Count: c.Data})
			}
			for _, f := range want.TopFeeds {
				top = append(top, Count{Name: phpNames.Replace(f.Name), Count: f.Count, Category: phpNames.Replace(f.Category)})
			}
			for name, pair := range map[string][2][]Count{
				"feeds by category": {o.FeedsByCategory, feedsBy}, "entries by category": {o.EntriesByCategory, entriesBy}, "top feeds": {o.TopFeeds, top},
			} {
				if got, want := sorted(pair[0]), sorted(pair[1]); !reflect.DeepEqual(got, want) || len(want) == 0 {
					t.Errorf("%s = %+v, want %+v", name, got, want)
				}
			}
			for name, c := range map[string]struct {
				feed int64
				want phpRepartition
			}{"every feed": {0, want.All}, "feed 2": {2, want.Feed}} {
				r := NewRepartition(facts, c.feed, time.UTC)
				// FreshRSS leaves December out unless an entry is dated there.
				months := r.PerMonth[:len(c.want.Month)]
				if r.Totals != totals(c.want.Totals) || !reflect.DeepEqual(r.PerHour[:], c.want.Hour) || !reflect.DeepEqual(r.PerWeekday[:], c.want.Weekday) ||
					!reflect.DeepEqual(months, c.want.Month) || r.Totals.Total == 0 {
					t.Errorf("repartition of %s = %+v, want %+v", name, r, c.want)
				}
				for what, pair := range map[string][2]float64{"hourly": {r.Hourly, c.want.Hourly}, "daily": {r.Daily, c.want.Daily}, "monthly": {r.Monthly, c.want.Monthly}} {
					if math.Abs(pair[0]-pair[1]) > 1e-9 {
						t.Errorf("%s average of %s = %v, want %v", what, name, pair[0], pair[1])
					}
				}
			}
			for key, list := range want.Unread {
				parts := strings.Split(key, "/")
				minPriority, limit := -10, 100
				if len(parts) == 3 {
					minPriority, limit = store.PriorityMain, 3
				}
				got := UnreadDates(facts, subs, parts[1], parts[0] == "id", minPriority, limit, time.UTC)
				var wanted []Bucket
				for _, b := range list {
					wanted = append(wanted, Bucket{b.Granularity, b.Unread})
				}
				if !reflect.DeepEqual(got, wanted) || len(wanted) == 0 {
					t.Errorf("unread by %s = %+v, want %+v", key, got, wanted)
				}
			}
		})
	}
}

// What the reference does not show: the days before today, the clock of
// another time zone, and nothing at all.
func TestCounting(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	at := func(text string) int64 {
		moment, err := time.ParseInLocation("2006-01-02 15:04", text, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return moment.Unix()
	}
	subs := Subscriptions{
		Categories: []*store.Category{{ID: 1, Name: "One"}},
		Feeds:      []*store.Feed{{ID: 1, CategoryID: 1, Name: "Main", Priority: 10}, {ID: 2, CategoryID: 1, Name: "Aside", Priority: 0}},
	}
	facts := []store.EntryFact{
		{ID: at("2026-10-06 23:30") * 1e6, FeedID: 1, Published: at("2026-10-06 23:30")},
		{ID: 2, FeedID: 1, Published: at("2026-10-07 00:10"), Read: true},
		{ID: 3, FeedID: 2, Published: at("2026-09-07 00:00"), Favorite: true},
		{ID: 4, FeedID: 2, Published: at("2026-09-06 23:59")},
		// The night the clocks go back in Berlin has 25 hours.
		{ID: 5, FeedID: 1, Published: at("2026-10-25 12:00")},
		{ID: 6, FeedID: 9, Published: at("2026-10-01 12:00")},
	}
	o := NewOverview(facts, subs, time.Date(2026, 10, 7, 9, 0, 0, 0, berlin))
	wantDays := [Days]int{}
	wantDays[Days-1], wantDays[0] = 1, 1
	if o.PerDay != wantDays || o.All != (Totals{5, 4, 1, 1}) || o.Main != (Totals{3, 2, 1, 0}) {
		t.Errorf("overview = %+v", o)
	}
	later := NewOverview(facts, subs, time.Date(2026, 10, 26, 9, 0, 0, 0, berlin))
	if later.PerDay[Days-1] != 1 || later.PerDay[Days-19] != 1 || later.PerDay[Days-20] != 1 {
		t.Errorf("days counted across a change of the clock = %v", later.PerDay)
	}
	r := NewRepartition(facts, 1, berlin)
	if r.PerHour[23] != 1 || r.PerHour[0] != 1 || r.PerHour[12] != 1 || r.PerWeekday[time.Tuesday] != 1 || r.PerWeekday[time.Wednesday] != 1 || r.PerMonth[9] != 3 {
		t.Errorf("repartition in Berlin = %+v", r)
	}
	if got := UnreadDates(facts, subs, ByDay, true, -10, 0, berlin); len(got) != 2 || got[1] != (Bucket{"2026-10-06", 1}) || got[0].Unread != 3 {
		t.Errorf("unread by the day entries were received = %+v", got)
	}
	if empty := NewRepartition(nil, 0, time.UTC); empty.Hourly != 0 || empty.Totals.Total != 0 {
		t.Errorf("repartition of nothing = %+v", empty)
	}
}
