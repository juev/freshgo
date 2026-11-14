//go:build e2e

package web

import (
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// A fieldset's intrinsic minimum must not widen a phone's layout viewport.
// Check the form itself, not just the document: hiding overflow is not a fix.
func TestE2ENarrowFeedForm(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.login("alice")
		for _, width := range []int64{360, 390, 430} {
			b.run(command(emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: width, Height: 740, DeviceScaleFactor: 1, Mobile: true}))
			b.open("/subscriptions/feeds/3")
			for _, look := range looks {
				for _, theme := range []string{"light", "dark"} {
					where := fmt.Sprintf("%dpx, %s, %s", width, look, theme)
					b.run(chromedp.Evaluate[chromedp.Void](`document.documentElement.dataset.look = '` + look + `'; document.documentElement.dataset.theme = '` + theme + `'`))
					if got := b.text(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`); got != "true" {
						t.Errorf("%s: feed form scrolls sideways: %s", where, b.text(`JSON.stringify({viewport: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth})`))
					}
					if got := b.text(`JSON.stringify([...document.querySelectorAll('.form fieldset, .form input, .form select, .form textarea, .form button')].filter(e => {
						if (!e.getClientRects().length) return false;
						const r = e.getBoundingClientRect(), form = e.closest('form').getBoundingClientRect();
						return r.width <= 0 || r.left < form.left - 1 || r.right > form.right + 1 || ['hidden', 'clip'].includes(getComputedStyle(e).overflowX) && e.tagName === 'FIELDSET';
					}).map(e => ({tag: e.tagName, id: e.id, width: e.getBoundingClientRect().width, right: e.getBoundingClientRect().right})))`); got != "[]" {
						t.Errorf("%s: form controls escape their column or are clipped: %s", where, got)
					}
				}
			}
			b.tabTo("#icon")
			b.press("\t")
			b.tabTo("#http_user")
		}
		b.accessible("the feed form, narrow")
	})
}
