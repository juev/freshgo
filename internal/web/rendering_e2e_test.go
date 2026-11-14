//go:build e2e

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/juev/freshgo/internal/store"
)

// A direct response may arrive in pieces even though the server buffered
// its HTML. Hold it after one complete article to observe that boundary.
func TestE2EInitialListAppearsTogether(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.login("alice")
		release := make(chan struct{})
		held := release
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" || r.Header.Get("X-Prepare") != "" {
				s.h.ServeHTTP(w, r)
				return
			}
			recorder := httptest.NewRecorder()
			s.h.ServeHTTP(recorder, r)
			for key, values := range recorder.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(recorder.Code)
			body := recorder.Body.String()
			cut := strings.Index(body, "</article>") + len("</article>")
			if cut < len("</article>") {
				t.Error("stream fixture has no article")
				return
			}
			_, _ = w.Write([]byte(body[:cut]))
			w.(http.Flusher).Flush()
			<-held
			_, _ = w.Write([]byte(body[cut:]))
		}))
		defer server.Close()
		defer func() {
			if release != nil {
				close(release)
			}
		}()
		b.base = server.URL
		b.run(command(page.Navigate, page.NavigateParams{URL: server.URL + "/"}))
		b.eventually("the first article parsed, while the rest is still in transit", func() bool {
			return read[bool](b, `Boolean(document.querySelector('.entries > article'))`)
		})
		if !read[bool](b, `getComputedStyle(document.querySelector('.entries')).visibility === 'hidden'`) {
			t.Fatal("a partial initial list is visible before the page is ready")
		}
		close(release)
		release = nil
		b.until("the whole initial list visible", `document.querySelectorAll('.entries > article').length === 18 && getComputedStyle(document.querySelector('.entries')).visibility === 'visible'`)
	})
}

func TestE2EInitialListFallback(t *testing.T) {
	for _, mode := range []string{"no-js", "missing-app"} {
		t.Run(mode, func(t *testing.T) {
			imported(t, Options{}, func(t *testing.T, s *site) {
				b := browse(t, s)
				b.login("alice")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mode == "missing-app" && r.URL.Path == "/static/app.js" {
						http.Error(w, "controlled missing script", http.StatusNotFound)
						return
					}
					s.h.ServeHTTP(w, r)
				}))
				defer server.Close()
				if mode == "no-js" {
					b.run(command(emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: true}))
				}
				b.base = server.URL
				b.open("/")
				if !read[bool](b, `document.querySelectorAll('.entries > article').length === 18 && getComputedStyle(document.querySelector('.entries')).visibility === 'visible' && !document.documentElement.classList.contains('preparing-reader')`) {
					t.Fatal("script failure or no-JS left the list hidden")
				}
			})
		})
	}
}

// The first unread navigation can use a page prepared while another page
// is shown, but only after the real server has checked its exact contents.
func TestE2EPreparedUnread(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		b := browse(t, s)
		b.run(command(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: `
			window.requests = [];
			const fetchPage = window.fetch.bind(window);
			window.fetch = async (url, options = {}) => {
				const response = await fetchPage(url, options);
				const headers = new Headers(options.headers);
				window.requests.push({url: String(url), prepare: headers.get('X-Prepare'), conditional: headers.has('If-None-Match'), status: response.status});
				return response;
			};`}))
		b.login("alice")
		b.open("/about")
		b.until("an unread page prepared without navigating", `requests.some(r => r.prepare === 'true' && r.status === 200) && location.pathname === '/about'`)
		b.press("g", "u")
		b.until("the validated unread page", `location.pathname === '/' && document.querySelector('.entries[data-state="unread"]')`)
		if !read[bool](b, `requests.some(r => r.conditional && r.status === 304)`) {
			t.Fatalf("prepared page was not revalidated: %s", b.text(`JSON.stringify(requests)`))
		}
		b.accessible("prepared unread page")
	})
}

func TestE2EPreparationFallback(t *testing.T) {
	for _, mode := range []string{"failed", "pending", "changed"} {
		t.Run(mode, func(t *testing.T) {
			imported(t, Options{}, func(t *testing.T, s *site) {
				b := browse(t, s)
				b.run(command(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: `
					window.preparationStarted = false; window.preparationFinished = false; window.conditionalStatus = 0;
					const nativeFetch = window.fetch.bind(window);
					window.fetch = async (url, options = {}) => {
						const headers = new Headers(options.headers);
						if (headers.has('X-Prepare')) {
							window.preparationStarted = true;
							if ('` + mode + `' === 'failed') throw new Error('controlled speculative failure');
							if ('` + mode + `' === 'pending') await new Promise(resolve => options.signal.addEventListener('abort', resolve, {once:true}));
						}
						const response = await nativeFetch(url, options);
						if (headers.has('X-Prepare')) response.clone().arrayBuffer().then(() => window.preparationFinished = true);
						if (headers.has('If-None-Match')) window.conditionalStatus = response.status;
						return response;
					};`}))
				b.login("alice")
				b.open("/about")
				b.until("the speculative request started", `window.preparationStarted`)
				if mode == "changed" {
					b.until("the speculative body downloaded", `window.preparationFinished`)
					s.setting("alice", "darkMode", "dark")
				}
				b.press("g", "u")
				b.until("unread through the normal fallback", `location.pathname === '/' && document.querySelector('.entries[data-state="unread"]')`)
				if mode == "changed" && !read[bool](b, `conditionalStatus === 200 && document.documentElement.dataset.theme === 'dark'`) {
					t.Fatal("changed page did not replace prepared HTML")
				}
			})
		})
	}
}

// A following page is prepared off-document and committed once. This pins
// the DOM insertion boundary, not an assumption about one paint per append.
func TestE2EArticleBatch(t *testing.T) {
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "posts_per_page", 5)
		s.setting("alice", "auto_load_more", false)
		s.setting("alice", "mark_when", map[string]any{"article": false})
		ids := s.stored("alice", store.Listing{Set: mainStream()})
		b := browse(t, s)
		b.login("alice")
		b.open("/?state=all")
		for _, id := range ids[:5] {
			b.press("n")
			b.until("the next row", current(id, false))
		}
		b.run(chromedp.Evaluate[chromedp.Void](`window.batches = [];
			new MutationObserver(records => { for (const r of records) { const n = [...r.addedNodes].filter(n => n.matches?.('article.entry')).length; if (n) batches.push(n); } }).observe(document.querySelector('.entries'), {childList: true})`))
		b.press("n")
		b.until("the first row of the new page", current(ids[5], false))
		if got := b.text(`JSON.stringify(batches)`); got != "[5]" {
			t.Fatalf("article insertions %s, want one batch [5]", got)
		}
		if !read[bool](b, `document.querySelectorAll('.entries > article').length === 10 && new Set([...document.querySelectorAll('.entries > article')].map(a => a.id)).size === 10`) {
			t.Fatal("batch lost or duplicated a row")
		}
	})
}

// Opt-in browser measurement, not a wall-clock assertion in CI. No remote
// content is fetched; each row carries roughly 6 KB of article HTML.
func TestE2EUnreadRenderingMeasurement(t *testing.T) {
	if os.Getenv("FRESHGO_MEASURE_RENDERING") == "" {
		t.Skip("set FRESHGO_MEASURE_RENDERING=1")
	}
	imported(t, Options{}, func(t *testing.T, s *site) {
		s.setting("alice", "posts_per_page", 100)
		s.setting("alice", "auto_load_more", false)
		s.setting("alice", "mark_when", map[string]any{"article": false})
		feed := &store.Feed{UserID: s.user("alice").ID, URL: "https://example.org/render", Name: "Rendering", Priority: priorityMain}
		if err := s.db.CreateFeed(context.Background(), feed); err != nil {
			t.Fatal(err)
		}
		rows := make([]*store.Entry, 250)
		for i := range rows {
			rows[i] = &store.Entry{FeedID: feed.ID, GUID: fmt.Sprintf("render-%d", i), Title: fmt.Sprintf("Rendering %d", i), Content: strings.Repeat(`<p>Today a new version was released. <a href="https://example.org/article">Read more</a> about the changes and the next steps.</p>`, 45), Published: 1_800_000_000 + int64(i)}
		}
		if err := s.db.InsertEntries(context.Background(), s.user("alice").ID, rows); err != nil {
			t.Fatal(err)
		}
		b := browse(t, s)
		b.run(command(page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: `
			window.preparedDownloaded = false;
			const nativeFetch = window.fetch.bind(window);
			window.fetch = async (url, options = {}) => {
				const response = await nativeFetch(url, options);
				if (new Headers(options.headers).has('X-Prepare')) response.clone().arrayBuffer().then(() => window.preparedDownloaded = true);
				return response;
			};`}))
		b.login("alice")
		if os.Getenv("FRESHGO_MEASURE_NETWORK") != "" {
			// This command is supported by Chrome 145 as well as the current
			// protocol; the generated by-rule signature tracks a newer Chrome.
			b.run(command(network.Enable, network.EnableParams{}), command(cdp.Command[map[string]any, cdp.Empty]{Method: "Network.emulateNetworkConditions"}, map[string]any{
				"offline": false, "latency": 40, "downloadThroughput": 1_000_000, "uploadThroughput": -1,
			}))
		}
		for i := 0; i < 5; i++ {
			b.open("/?state=unread")
			t.Logf("direct %d: %s", i, b.text(`JSON.stringify((n => ({ttfb: n.responseStart-n.requestStart, transfer: n.responseEnd-n.responseStart, dom: n.domContentLoadedEventEnd-n.responseEnd, load: n.loadEventEnd-n.startTime, bytes: n.decodedBodySize, rows: document.querySelectorAll('.entries > article').length}))(performance.getEntriesByType('navigation')[0]))`))
			b.open("/about")
			if os.Getenv("FRESHGO_MEASURE_PREPARED") != "" {
				b.until("the prepared unread body downloaded", `window.preparedDownloaded`)
			} else {
				// The original code has no preparation to wait for. The same
				// idle interval is outside the measured navigation in both arms.
				b.run(chromedp.Sleep(1500 * time.Millisecond))
			}
			b.run(chromedp.Evaluate[chromedp.Void](`window.measure = {}; document.addEventListener('htmx:before:request', () => window.measure.start = performance.now(), {once:true}); document.addEventListener('htmx:after:request', () => window.measure.received = performance.now(), {once:true}); document.addEventListener('htmx:after:swap', () => { window.measure.swapped = performance.now(); requestAnimationFrame(() => requestAnimationFrame(() => window.measure.painted = performance.now())); }, {once:true})`))
			b.press("g", "u")
			b.until("unread painted", `window.measure.painted`)
			t.Logf("in-place %d: %s", i, b.text(`JSON.stringify({request: measure.received-measure.start, swap: measure.swapped-measure.received, paint: measure.painted-measure.start, rows: document.querySelectorAll('.entries > article').length})`))
			for range 100 {
				b.press("n")
			}
			b.run(chromedp.Evaluate[chromedp.Void](`window.batchMeasure = { start: performance.now(), counts: [] };
				new MutationObserver(records => { for (const r of records) { const count = [...r.addedNodes].filter(n => n.matches?.('article.entry')).length; if (count) batchMeasure.counts.push(count); } if (batchMeasure.counts.length) { batchMeasure.inserted = performance.now(); requestAnimationFrame(() => requestAnimationFrame(() => batchMeasure.painted = performance.now())); } }).observe(document.querySelector('.entries'), {childList: true})`))
			b.press("n")
			b.until("the batch painted", `window.batchMeasure.painted`)
			t.Logf("batch %d: %s", i, b.text(`JSON.stringify({paint: batchMeasure.painted-batchMeasure.start, insertions: batchMeasure.counts.length, rows: batchMeasure.counts.reduce((a,b)=>a+b,0)})`))
		}
	})
}
