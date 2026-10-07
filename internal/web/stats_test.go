package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// R14: the pages of statistics show the numbers as pictures and as tables.
func TestStatisticsPages(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		now := s.clock()
		*now = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
		s.setting("alice", "timezone", "UTC")
		if a := s.get("/stats"); a.status != http.StatusSeeOther {
			t.Errorf("GET /stats without a login: status %d", a.status)
		}
		s.asAlice()
		body := s.shown("/stats")
		for _, want := range []string{
			`<a href="/stats" aria-current="page">Statistics</a>`, `<a href="/stats" aria-current="page">Overview</a>`,
			`<tr><th scope="row">Feeds of the main stream</th><td>15</td><td>14</td><td>1</td><td>1</td></tr>`,
			`<tr><th scope="row">Every feed</th><td>22</td><td>18</td><td>4</td><td>2</td></tr>`,
			`<tr><th scope="row">Scraped &amp; parsed</th><td>10</td></tr>`,
			`<tr><th scope="row">RSS corpus <span class="muted">Blogs</span></th><td>5</td></tr>`,
			`role="img" aria-label="Entries dated on each of the last 30 days"`, `<title>2026-09-02: 8</title>`,
			`<tr><th scope="row">2026-09-02</th><td>8</td></tr>`, `<a href="/subscriptions/problems">Silent feeds</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /stats: no %q in\n%s", want, body)
			}
		}
		if strings.Contains(body, "style=") {
			t.Error("a chart carries inline styles, which the policy of the pages forbids")
		}
		all := s.shown("/stats/repartition")
		one := s.shown("/stats/repartition?feed=2")
		if !strings.Contains(all, `<tr><th scope="row">All</th><td>22</td>`) || !strings.Contains(one, `<tr><th scope="row">All</th><td>5</td>`) ||
			!strings.Contains(one, `<option value="2" selected>RSS corpus</option>`) || !strings.Contains(all, `<tr><th scope="row">September</th><td>21</td></tr>`) ||
			!strings.Contains(all, "110.00 entries a month") {
			t.Errorf("the pages of the spread over time:\n%s", all)
		}
		if other := s.shown("/stats/repartition?feed=99"); !strings.Contains(other, `<td>22</td>`) {
			t.Error("a feed there is not narrows the spread")
		}
		unread := s.shown("/stats/unread?by=month&field=published")
		if !strings.Contains(unread, `<tr><th scope="row">2026-09</th><td>17</td></tr>`) || !strings.Contains(unread, `<option value="month" selected>`) {
			t.Errorf("GET /stats/unread by month:\n%s", unread)
		}
		if byDay := s.shown("/stats/unread"); !strings.Contains(byDay, `<td>18</td>`) {
			t.Errorf("GET /stats/unread:\n%s", byDay)
		}
	})
}
