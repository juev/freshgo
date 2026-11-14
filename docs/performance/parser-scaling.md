# Parser correctness and scaling guard

Issue: #212. Reproduction baseline:
`e4a8c26a23a7c3a5336761984dfa404eef39c607`.

## Why the old oracle failed

`TestParseLargeDocuments` checked three large documents against an absolute
20-second **wall-clock** budget per document. That mixes parser work with time
waiting for a shared CPU. It neither measures an input-size growth curve nor
separates scheduling delay from algorithmic complexity.

A bounded Linux reproduction built the original feed tests with `-race`, then
pinned the test and three finite CPU competitors to the same allowed logical
CPU. The original test failed at its intended assertion: the 8,800,070-byte
text-chunk document took 28.318 seconds. The whole invocation took 52.077 seconds
of wall time but used 13.043 seconds of CPU. Competitors were terminated and
reaped in `finally`; no host-wide CPU settings were changed.

This demonstrates the budget's load sensitivity, not a parser regression or
proof of the exact scheduling of the earlier historical failure.

## Retained correctness

The same document shapes and sizes remain in `TestParseLargeDocuments`:

- 200,000 empty items;
- 200,000 text chunks separated by child elements;
- 50,000 media items.

The test now checks item counts, the complete concatenated text, and media
URLs at both ends of the parsed list, rather than merely accepting a nil error.
No production parser code, fixture size, package timeout or CI discovery rule
changes. The large-document test is not skipped or quarantined.

## Separate growth oracle

`TestParseScalesLinearly` starts this package's own test executable with an
exact helper-test selector. Every measurement gets its own process, hence its
own allocator counters and CPU accounting, even in a parallel full-suite run.
It needs no separately built application or external benchmark tool.

For each shape, compare 256 with 2,048 units. Media additionally compares
1,024 with 8,192 siblings: repeated channel-default scans can otherwise be
hidden by the substantial linear cost of reading each enclosure. Small and
large workers perform 32 and 4 measured parses respectively, after a warmup.
Only these bounded size/repetition pairs are accepted by the helper.

Two independently checked metrics are normalized by the parse count:

- **CPU:** `ProcessState.UserTime() + SystemTime()` for the complete worker,
  including startup, warmup, GC and test overhead; not just the parsing loop.
  Time waiting for a contended CPU is excluded.
- **Allocated bytes:** `runtime.MemStats.TotalAlloc` growth around the repeated
  correctness-checked parses, after a forced GC. This is cumulative allocation,
  not peak live memory or allocation count.

Either metric exceeding **24x for 8x input** fails with the measured growth.
That allows substantial runtime/GC variation and n-log-n work, while being
below quadratic growth's 64x. A two-minute child-process deadline is only a
hung-worker safety net, not a replacement per-document performance budget.
Missing, duplicate or invalid helper measurements also fail closed.

## Executed sensitivity controls

Bounded Go overlays changed production behavior only; the final measurement
sizes, assertions and 24x threshold stayed fixed during the controls. No large
200,000-unit fixture was run against a quadratic mutation.

| Controlled regression | Intended assertion observed |
| --- | --- |
| Repeated immutable string concatenation instead of the text buffer | Allocated-byte growth 53.83x |
| Recompute channel authors for every item | CPU growth 40.17x |
| Recompute channel media defaults for every item | CPU growth 33.57x |

The first media probe at 256/2,048 **survived** (CPU 22.03x). It was not counted
as a successful sensitivity check. The supplemental larger sibling range was
added, retaining the original range and threshold, and then rejected that
regression. Correct production passed both ranges.

The final correctness and scaling selection also passed with `-race` under
three competitors on one logical CPU. The text fixture took 30.01 seconds
without a false performance failure. CPU growth was 6.86x (items), 12.27x
(chunks), 10.17x and 11.86x (media); allocation growth was 7.47x, 7.45x,
7.94x and 8.09x. The complete selection took 97.327 seconds of wall time and
22.145 seconds of test-process-tree CPU. These are single controlled runs,
not a speedup estimate: the before/after selections differ and other shared
machine load was not held identical.

## Limits and rerunning

Run the normal package or full race suite; the guard is enabled by default:

```sh
go test -race -count=1 -run '^TestParse(LargeDocuments|ScalesLinearly)$' -v ./internal/feed
go test -race -count=1 ./...
```

This finite-input guard is not a proof of asymptotic linearity, an absolute
throughput/SLA check, or an exhaustive detector of every parser regression.
A uniformly slower linear implementation can pass. Very late quadratic
crossovers, CPU-frequency changes, GC/race overhead and platform accounting
can affect the result. The process-wide CPU boundary and small batch sizes
are intentional, conservative qualifications; unsupported/zero CPU accounting
fails instead of silently disabling coverage. Native execution here was on
Linux; no new Windows/macOS runtime claim is made.

CPU and allocation checks complement one another: optimized copying can hide
quadratic byte churn from a CPU-only sample, while rescanning cached defaults
can waste CPU without allocating proportionately more bytes. Neither check
replaces the retained large-document correctness assertions.
