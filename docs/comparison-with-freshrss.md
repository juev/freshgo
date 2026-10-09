# freshgo and FreshRSS measured side by side

Both servers ran on one machine on 2026-10-09 with the same subscriptions. This page records what each of them cost in CPU time, memory and disk space, and how fast each answered. Every section says how its numbers were taken, so the measurement can be repeated after changes.

## Summary

| | FreshRSS 1.30.1 | freshgo |
|---|---|---|
| CPU time in a day | 109 s | 95 s |
| Memory that cannot be reclaimed, average / highest | 116 / 178 MiB | 42 / 64 MiB |
| Container image | 384 MB | 38.3 MB |
| Data on disk | 189 MiB | 49 MiB |
| Written to disk in a day | 1.2 GiB | 0.54 GiB |
| `stream/contents`, 50 articles | 4.5 ms | 3.3–3.7 ms |
| Main page, 20 unread articles | 7.6–8.2 ms | 5.3–5.8 ms |

freshgo takes a third of the memory, a quarter of the disk space and a tenth of the image, and writes half as much to the disk. It takes the same CPU time to keep the feeds fresh. It answers faster on nine of the ten requests measured and level on one.

CPU time, memory and disk are those of freshgo v0.2.3. The times of answers are those of v0.2.7: v0.2.3 was quicker on two of the ten requests, slower on five and about level on three, and what was changed since is listed under "Time of answers".

## Setup

- Machine: Intel Xeon E3-1245 v6 (4 cores, 8 threads), 32 GB of memory, two SSDs in RAID 1 with ext4, Linux 6.12, Docker 29.8.1. Other services run on it; its load average was below 1.
- FreshRSS 1.30.1 from the official image `freshrss/freshrss` (Apache with PHP 8.4), database in SQLite, cron every 20 minutes.
- freshgo v0.2.3 from `ghcr.io/juev/freshgo`, database in SQLite, `-refresh-interval` at its default of 10 minutes, WebSub on.
- Data: one user, 84 feeds in 8 categories, about 6000 articles. The database of freshgo was imported from this FreshRSS two days earlier, and both have refreshed the same feeds since. Every feed is due once an hour in both.
- Neither container has a limit on CPU or memory.

Numbers of the running servers come from two places. cAdvisor and Prometheus hold the history of both containers, sampled every 15 seconds. The files of the control group of a container (`cpu.stat`, `memory.stat`, `io.stat`) give exact counters since its start.

## CPU time

| | FreshRSS | freshgo |
|---|---|---|
| In 24 hours | 108.6 s | 95.0 s |
| Share of one core, average over 24 hours | 0.13 % | 0.11 % |
| An hour in which all feeds are refreshed | 3.3–5.6 s | 4.2–4.7 s |
| An hour in which no feed is due | 1.1–1.4 s | 0.8–1.3 s |
| Highest average over 5 minutes | 1.7 % of a core | 1.4 % of a core |

Source: `container_cpu_usage_seconds_total` of cAdvisor, by hour over 48 hours.

The two cost the same. Subtracting the idle hour, a pass over 84 feeds takes freshgo about 3.5 s of CPU time, or 40 ms a feed.

In these 48 hours freshgo refreshed feeds 3264 times in 287 runs of its scheduler; 114 of the attempts failed. FreshRSS ran its cron job 144 times and made 3087 requests for feeds, of which 1771 were answered `304 Not Modified`.

The container of freshgo was replaced 12 times in the 48 hours by new versions, so its figures include 12 starts. The use of the web interfaces by the one reader is in both figures and was not separated.

## Memory

| | FreshRSS | freshgo |
|---|---|---|
| Anonymous memory, average | 67 MiB | 42 MiB |
| Anonymous memory, highest | 129 MiB | 64 MiB |
| Shared memory (the opcode cache of PHP) | 49 MiB | 0 |
| Anonymous and shared together, average / highest | 116 / 178 MiB | 42 / 64 MiB |
| Working set as cAdvisor counts it, average / highest | 179 / 253 MiB | 63 / 86 MiB |
| Processes | 11 of Apache, cron | 1 with 14 threads |

Source: `container_memory_rss` and `container_memory_working_set_bytes` over 48 hours, `memory.stat` for the shared memory. For freshgo the figures are those of the container that lived longest, 22 hours; the highest working set of any of the 12 was 118 MiB.

Anonymous and shared memory cannot be given back without swapping. The working set adds the page cache that was used recently, which the kernel reclaims when memory is short. FreshRSS keeps more of it because it reads its feed cache from files.

Under the load of the next section, on a fresh container, the anonymous memory of freshgo went from 27 to 36 MiB and that of FreshRSS from 11 to 50 MiB, as Apache started more processes.

## Disk

| | FreshRSS | freshgo |
|---|---|---|
| Container image | 384 MB | 38.3 MB |
| Data directory | 189 MiB | 49 MiB |
| of that, the database | 61 MiB | 49 MiB |
| of that, free pages in the database | 13 MiB | under 0.1 MiB |
| of that, cached copies of feeds | 126 MiB | none |
| The table of articles alone | 46.6 MiB | 47.2 MiB |
| Written in a day | 1.2 GiB | 0.54 GiB |

Source: `docker images`, `du`, the `dbstat` table and `PRAGMA freelist_count` of SQLite, `io.stat` of the control group.

The articles take the same room in both. The difference in the data directory is the cache FreshRSS keeps of every feed document it fetched; freshgo keeps only the `ETag` and `Last-Modified` of a feed. A database imported anew from this FreshRSS is 47.3 MiB.

The bytes written are `container_fs_writes_bytes_total` of cAdvisor for the device of the array, over the 24 hours to the evening of 2026-10-09: 1203 MiB for FreshRSS and 553 MiB for freshgo. Over 48 hours they are 2473 MiB and 1038 MiB. The day of freshgo was not a usual one: seven containers ran in turn as new versions came, one of which built new indexes on the table of articles and another wrote 1178 texts anew, so a day of normal work writes less than this.

## Time of answers

The running servers hold different articles by now, each with identifiers of its own, so their answers cannot be compared. For this measurement the data of FreshRSS was copied, a second FreshRSS was started on the copy, and a second freshgo on a database imported from the same copy. Both ran from the images of the running servers, on a Docker network without a way out, so neither refreshed anything. The client was a Python script on the same machine with one connection kept open: 5 requests to warm up, then 100 in a row. It ran twice.

The figures are those of freshgo v0.2.7, taken on 2026-10-09 on a copy of 6026 articles, 59 of them unread. The pages of FreshRSS were asked for with `auth_type` set to `none` in the copy, those of freshgo through a session.

The answers were checked before the times were taken: the lists of identifiers were the same byte for byte, `stream/contents` returned the same articles in the same order, and of 200 article texts 147 were identical. The import cleans the texts by the rules of freshgo since v0.2.6, which writes some of them differently with the same meaning; with v0.2.3, which stored them as FreshRSS has them, 199 of 200 were identical.

| Request | Size | FreshRSS, median | freshgo, median | CPU time a request: FreshRSS | freshgo |
|---|---|---|---|---|---|
| `subscription/list` | 22 KB | 2.3 ms | 1.6 ms | 2.6 ms | 1.7–1.8 ms |
| `unread-count` | 7 KB | 9.3–9.4 ms | 3.0–3.1 ms | 9.6–9.8 ms | 3.7–3.8 ms |
| `tag/list` | 0.5 KB | 1.2 ms | 1.2 ms | 1.2 ms | 1.1–1.3 ms |
| `stream/contents`, 20 articles | 80 KB | 2.8–3.0 ms | 1.8–2.1 ms | 3.1–3.4 ms | 2.3–2.6 ms |
| `stream/contents`, 50 articles | 294 KB | 4.5 ms | 3.3–3.7 ms | 4.8 ms | 4.3–4.7 ms |
| `stream/contents`, 200 articles | 1.53 MB | 13.4–13.6 ms | 11.3–11.7 ms | 13.9–14.1 ms | 16.3–17.6 ms |
| `stream/items/ids`, 1000 newest | 25 KB | 4.4–5.3 ms | 1.9–2.0 ms | 4.8–5.9 ms | 1.9 ms |
| `stream/items/ids`, unread (59 of 6026) | 1.5 KB | 1.4 ms | 0.7 ms | 1.4–1.5 ms | 0.8 ms |
| Main page, 20 unread articles | 232 KB / 122 KB | 7.6–8.2 ms | 5.3–5.8 ms | 8.2–9.2 ms | 7.3–7.6 ms |
| Main page, 20 articles of all | 234 KB / 127 KB | 7.4–7.5 ms | 5.5–5.6 ms | 7.8–8.1 ms | 7.6–7.7 ms |

A range gives the two runs; one number stands where they agreed to 0.1 ms. The CPU time is that of the whole container divided by the number of requests.

With 8 connections at once, 320 requests in all, two runs:

| Request | FreshRSS | freshgo |
|---|---|---|
| `stream/contents`, 50 articles | 544 and 825 requests a second | 1125 and 1118 requests a second |
| Main page, 20 unread articles | 452 and 519 requests a second | 614 and 664 requests a second |

What stands out:

- **freshgo answers faster on nine of the ten requests** and is level on `tag/list`. The largest gaps are `unread-count` and the lists of identifiers, two to three times.
- **`stream/contents`**, where a client spends its time when it synchronises, is 1.2 to 1.7 times faster in freshgo, the more so the shorter the answer.
- **The CPU time of `stream/contents` of 200 articles** is still higher in freshgo, 16.3–17.6 ms against 13.9–14.1 ms, though the answer comes sooner: the Go runtime works on several threads, and the garbage collector runs beside the request.
- **With 8 connections** freshgo serves 1.4 to 2 times the requests of `stream/contents`, and its two runs agree; those of FreshRSS differ by half.
- The stand ran with the default of `media_proxy`, `http-only`. With `all`, where every image of a text is led through the server, `stream/contents` costs freshgo more: on the running server 50 articles of 984 KB took 10.1 ms.

All of these are a few milliseconds for one reader. They would matter with many users or with clients that fetch thousands of articles at once.

`ClientLogin` was measured with v0.2.3 only: 24.8 ms in FreshRSS and 27.8 ms in freshgo, the cost of bcrypt in both.

### Against v0.2.3

The first measurement, on a copy of 6023 articles with 56 unread, had freshgo slower where a client spends its time:

| Request | FreshRSS then | freshgo v0.2.3 | freshgo v0.2.7 |
|---|---|---|---|
| `subscription/list` | 2.3 ms | 2.1 ms | 1.6 ms |
| `unread-count` | 9.4 ms | 3.8 ms | 3.0–3.1 ms |
| `tag/list` | 1.3 ms | 2.5 ms | 1.2 ms |
| `stream/contents`, 20 articles | 2.9 ms | 3.6 ms | 1.8–2.1 ms |
| `stream/contents`, 50 articles | 4.6 ms | 7.3 ms | 3.3–3.7 ms |
| `stream/contents`, 200 articles | 13.8–18.4 ms | 25.7 ms | 11.3–11.7 ms |
| `stream/items/ids`, 1000 newest | 5.4 ms | 2.5 ms | 1.9–2.0 ms |
| `stream/items/ids`, unread | 1.5 ms | 9.7 ms | 0.7 ms |
| Main page, 20 unread articles | 8.8 ms | 10.2 ms | 5.3–5.8 ms |
| `stream/contents`, 50 articles, 8 connections | 694 requests a second | 426 requests a second | 1118–1125 requests a second |
| Main page, 8 connections | 403 requests a second | 335 requests a second | 614–664 requests a second |

What was changed between the two, each with its own measurement in the issue it closed:

- The lists of unread and starred entries are read from an index that holds the identifier (#56); so are the lists bounded by time (#59). The unread list opened every row of the user for one flag.
- `stream/contents` allocates less than half the memory for an answer (#56) and sorts the feeds only where it lists them (#57).
- The addresses of images are replaced without building a document of the text, and a text with nothing to replace is not parsed at all (#58, #71).
- The heap may grow to three times what is in use before a collection, `GOGC=200`, where the runtime of Go starts at 100 (#59).
- A page shows the text of an entry as it is stored; every writer cleans what it stores, and the texts stored before were cleaned once (#73).
- A page counts the unread entries feed by feed and no longer reads the index over every entry (#75).

## Import

`freshgo import` moved the copy (84 feeds, 6023 articles, 43 MB of article text) into an empty SQLite database in 1.1 s, the start of the container included.

## What was not measured

- A forced refresh of all feeds at once. The time of a refresh is mostly the time of the sites that are asked, and the two servers were not made to ask them at the same moment.
- PostgreSQL as the database of either.
- More than one user, and more than 6000 articles.
- The time of a page in a browser: scripts, styles and images were not fetched.
- CPU time and memory of v0.2.7 in normal work. The figures of those sections are of v0.2.3; `GOGC=200` lets the heap grow further, by about 8 MB in a benchmark.
