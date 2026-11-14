# Unread navigation and article batches (#210)

## What was measured

The user's report was a first unread-page opening taking a little over one
second. That timing was not independently reproduced on the deployed instance.

This experiment uses the real web handler, SQLite, and Chrome for Testing
145.0.7632.6. The imported reference installation is extended with a main-stream
feed of 250 generated unread articles, each containing about 6 KB of HTML.
There are 100 entries per page; automatic pagination and marking on open are
disabled. No external article resources are fetched.

Five direct navigations, five in-place unread navigations and five next-page
loads were measured per version. The network is deliberately throttled through
CDP to 40 ms latency and 1,000,000 bytes/s download throughput. This is a
controlled network experiment, not a measurement of a reader's real connection.
The baseline is commit `f4354a0bbea77d03eeeca47f3640c82403a8ccf7`, supplied
through Go's overlay mechanism to the same measurement test.

The direct navigations retain the authenticated session and warmed assets from
login; these are **not fresh-browser cold starts**. In-place measurements start
when htmx issues its navigation request, after an idle interval on About. For
the prepared version, speculative downloading finishes before that interval
ends; its cost is outside the navigation interval, not eliminated. The new
version's final measurements ran alongside the repository's race/e2e checks on
a shared machine, so small timing differences are not isolated CPU effects.

"Frame-ready" below means two requestAnimationFrame callbacks after the swap or
append. This is a consistent browser-readiness proxy, not observed GPU paint or
on-device latency. Resource Timing separates the document's request/transfer
from DOMContentLoaded work; a MutationObserver counts article insertion records.

## Results (medians of five samples, milliseconds)

| Operation | Baseline | Prepared/batched |
| --- | ---: | ---: |
| Direct document: request to first response byte | 35.0 | 46.6 |
| Direct document: response transfer | 908.5 | 903.2 |
| Direct document: response end to DOMContentLoaded end | 9.5 | 8.6 |
| Direct document: navigation to load end | 960.4 | 972.9 |
| In-place unread: request to complete HTML available | 976.8 | 49.3 |
| In-place unread: HTML available to swap complete | 118.0 | 123.2 |
| In-place unread: request to frame-ready | 1108.4 | 185.0 |
| Next 100 articles: request/insert to frame-ready | 1030.8 | 1045.9 |
| Article DOM insertions for that next page | 100 | 1 |

In this controlled experiment, prepared navigation reached 185 ms median
(including server validation), below 250 ms and an 83.3% reduction in this sample.
This is not a general latency guarantee.

The direct page still transfers about 898 KB (898,332 bytes before, 898,388
after). **There is no demonstrated direct-load speedup.** Its presentation now
waits until the initial list is parsed and initialized, rather than exposing
partial rows as the response arrives. A controlled HTTP test holds the response
after the first article and checks that boundary explicitly.

**There is no demonstrated next-page latency improvement.** That path is still
network-bound in this experiment. The change is one detached, prepared DOM batch
instead of 100 article insertions. The original synchronous loop did not prove
that Chrome painted 100 times, and these measurements make no such claim.

## Behaviour and limits

- One speculative unread response, in memory only: at most 2 MiB downloaded,
  5 s request deadline and 15 s reuse eligibility. No speculation on a hidden
  page, an unread stream, an anonymous page, or with data saving enabled.
- The server authenticates and renders the requested page again before returning
  304. Its byte-exact HTML hash includes current rows, counts, preferences,
  controls, identity and notices. The original mark-all cutoff is preserved
  for at most 30 s so an otherwise identical representation can match.
- Server query/render work is **not** cached. Preparation adds at most one
  bounded request per eligible mounted page, and shifts its transfer earlier.
  Missed SSE events cannot authorize a stale response.
- Preparation leaves one-shot notices alone. A real navigation consumes them
  and gets a fresh normal response when its HTML differs. Failed or unfinished
  preparation does not delay the normal navigation. Back/forward fetches fresh.
- Existing incoming-entry behaviour is unchanged: no automatic insertion of
  unseen new entries under a reader. Pagination preserves the existing nodes,
  open articles, ordering and duplicate prevention.
- The early external script reveals the initial list at DOMContentLoaded, not
  after images or subsequent pages. No-JS and a missing main script leave a
  visible, usable ordinary page.

## Reproduce

With Chrome on PATH, run the focused browser tests and the opt-in measurement:

```sh
go test -tags e2e -count=1 -run '^TestE2E(InitialList|PreparedUnread|PreparationFallback|ArticleBatch)' -v ./internal/web
FRESHGO_MEASURE_RENDERING=1 FRESHGO_MEASURE_NETWORK=1 FRESHGO_MEASURE_PREPARED=1 \
  go test -tags e2e -count=1 -run '^TestE2EUnreadRenderingMeasurement$' -v ./internal/web
```

For the baseline comparison, map `internal/web/reader.go`,
`internal/web/render.go`, `internal/web/static/app.js`,
`internal/web/static/app.css` and `internal/web/templates/layout.html` to copies
from the stated baseline using `go test -overlay <mapping.json>`, and omit
`FRESHGO_MEASURE_PREPARED`. The baseline has no speculative response to wait for.
Keep the same fixture, browser and throttling. Aggregate the emitted per-sample
JSON numerically rather than comparing a single fastest run.
