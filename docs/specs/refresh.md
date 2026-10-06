# Feed refresh

Status: implemented for selecting, fetching, reading and storing. Cleanup and auto-read (plan step 8), filter actions (9), full text (10), icons (11) and WebSub (12) attach to this pipeline later and are not described here.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `FreshRSS_feed_Controller::actualizeFeeds`, `FreshRSS_Feed::load`, `loadEntries`, `FreshRSS_EntryDAO::addEntry`, `updateEntry`, `updateLastSeen`, `commitNewEntries` and `app/actualize_script.php` of FreshRSS at commit `219eaf58`.

## Purpose and scope

`internal/refresh` keeps stored entries in step with the feeds. `freshgo refresh` runs it once, `freshgo serve` on a schedule. Covers plan requirements R3 and R4 and the pipeline side of R5, R6 and R8.

## Requirements

- F1. A run goes through the users in name order and skips a user whose settings have `enabled` false.
- F2. A feed is refreshed when it is not muted (`ttl` not negative) and more than its period has passed since `last_update`. The period is `ttl`, or the user's `ttl_default` (3600 s when unset) for `ttl` 0. A failure does not move `last_update`, so a failing feed is tried on every run. With `-force` the period is ignored; muted feeds stay out.
- F3. Feeds are taken in the order of their last attempt, the greater of `last_update` and `error`, oldest first. Up to 8 feeds of a user are handled at once; the fetch client limits requests per host on top of that.
- F4. The request carries the feed's settings (`fetch.FeedParams`), the `Accept` header of its kind and the validators stored at the last successful fetch. A trailing `#force_feed` is not part of the requested address.
- F5. A 304 answer: entries the feed listed at its previous refresh (`last_seen` not older than the feed's `last_update`) get `last_seen` of this refresh; nothing else about entries changes. The feed counts as refreshed.
- F6. A fetched document is read by the feed's kind: 10, 15, 25, 30 and 35 through `internal/scrape`, everything else as RSS or Atom. Dates without a zone are read in the user's `timezone`, or the server's when it is blank.
- F7. Entry identifiers come from `Feed.AssignGUIDs` with the feed's `unicityCriteria` (legacy `hasBadGuids` means `link`) and `unicityCriteriaForced`. When the criteria degrade, the new value is stored in the feed's attributes and `hasBadGuids` is removed. Of several items with one identifier the one nearest to the end of the document is kept.
- F8. An item whose identifier the feed does not have yet becomes a new entry: unread, not starred, `last_seen` of this refresh, the date of the item or the time of the refresh when the item has none. New entries of a feed get identifiers in ascending order of date, entries of one date in document order from the end, and all of them above every identifier the user already has.
- F9. An item whose identifier exists and whose hash differs from the stored one rewrites title, authors, content, link, date, tags, attributes and hash of that entry and sets `last_modified`. The identifier and the starred state stay. The read state stays unless `mark_updated_article_unread` is true, in the feed's attributes or else in the user's settings; then the entry becomes unread. A read or starred state the user changes while the feed is being processed is kept.
- F10. A stored entry without a hash (imported) is not compared: it keeps its content and gets the hash of the current item. Changes are noticed from the next refresh on.
- F11. Every item of the document sets `last_seen` of its entry to the time of the refresh.
- F12. After a successful fetch the feed gets `last_update` of this refresh, `error` 0 and the validators of the answer. Its address follows an unbroken chain of 301 and 308 redirects, credentials removed. An empty name is filled from the feed title, a website that is empty or equal to the feed address from the feed link, a blank description from the feed description.
- F13. A failure of fetching or reading (network, HTTP status, a pause asked with `Retry-After`, a document that is not a feed, unusable scraping settings, no items found by a scraper) sets `error` to the time of the attempt and leaves everything else. HTTP 410 also mutes the feed: `ttl` becomes negative, for `ttl` 0 the negated user default.
- F14. All writes of one feed's refresh happen in one transaction. Two refreshes of the same feed, from one or several processes, do not interleave and do not produce duplicates or errors.
- F15. One failing feed does not stop the run. `freshgo refresh` prints the counts per user and exits with 0 unless the run itself failed (database, interruption).
- F16. `freshgo serve` runs a refresh at start and then every `-refresh-interval` (`$FRESHGO_REFRESH_INTERVAL`, 10 minutes by default) until it is told to stop.
- F17. Feeds on loopback, private and link-local addresses are refused unless listed in `-fetch-allowlist` (`$FRESHGO_FETCH_ALLOWLIST`).

## Decisions

- **The hash is freshgo's own**: SHA-256 over link, title, authors, content, tags and attributes as read from the feed, before any hook changes the entry. The date is left out because it can be the time of the refresh. FreshRSS's md5 depends on libxml output and is not reproduced.
- **Validators live in `feeds.http_etag` and `feeds.http_last_modified`.** FreshRSS keeps a hash of the body (`SimplePieHash`) and an on-disk cache instead; freshgo relies on the 304 answer alone and compares entry hashes when a server does not support conditional requests.
- **Identifier order is per feed**, not per run as in `commitNewEntries`: each feed is stored in its own transaction so that feeds can be refreshed in parallel.
- **An entry without a title is stored with an empty title.** FreshRSS stores its guid as the title and treats the two being equal as "no title"; import converts that form.
- **Field length limits of FreshRSS (title 8192 bytes, author 1024, link 16383 and ASCII only) are not applied.** They come from MySQL column sizes. The 767-byte limit of the guid stays, as it defines the key.
- **The scheduler is the cron of FreshRSS built in**: the same selection rule on a fixed interval, no per-feed timers. A failing feed is retried every interval, as with FreshRSS under cron.
- **`Retry-After` pauses are kept in memory.** `serve` honours them between runs; separate `freshgo refresh` invocations do not remember them.
- **Users inactive for long are not skipped**: freshgo does not track user activity yet (`max_inactivity` in FreshRSS is unlimited by default).
- **An invalid identifier count does not mark the feed as failing.** FreshRSS sets the error in memory only and overwrites it in the same refresh; freshgo logs a warning.

## Known differences from FreshRSS

- A changed feed setting does not force a re-read while the server keeps answering 304. FreshRSS has the same limitation through its cache; the subscription editing of step 11 clears the validators.
- With a blank `timezone` the server's zone applies, as in FreshRSS. Moving an installation to a server in another zone shifts dates written without a zone, and with them the identifiers of entries keyed by date.

## Verification scenarios

`internal/refresh`, each on SQLite and, under `make test-integration`, on PostgreSQL.

- R3, F8, F10, F11: the reference installation imported and refreshed from its own corpus through a proxy that keeps the feed addresses → no new and no updated entries, every entry equal to what it was except hash and `last_seen`; a second refresh, now comparing hashes → the same; one item added to `rss.xml` → one new entry for each of the two subscribers, with an identifier above all earlier ones. Covers every feed kind, `atom:id`, RSS `guid`, items without an identifier, non-ASCII and `&` in identifiers, force-https domains. `TestRefreshOfImportedInstallation`.
- F8, F12: `TestNewFeedIsFilledIn`, `TestRefreshAddsOnlyWhatIsNew`.
- R4, F9: `TestChangedEntryIsUpdated`, `TestChangedEntryBecomesUnreadWhenAsked`. F10: `TestImportedEntryIsNotTakenAsChanged`.
- F7: `TestRepeatedGUIDs`. F1–F3: `TestWhichFeedsAreRefreshed`. F5: `TestUnchangedFeed`. F12 redirects: `TestFeedAddressFollowsPermanentRedirect`. F13: `TestFailedFeed`. F6: `TestScrapedFeedKinds`. F14: `TestConcurrentRefreshOfOneFeed`, `TestLockFeedSerializes` in `internal/store`. F16: `TestScheduleRunsUntilCancelled`.
- F15–F17 through the commands: `TestRefresh`, `TestServeRefreshesUntilStopped` in `cmd/freshgo`; settings: `TestRefreshSettings` in `internal/config`.

The reference test sets the users' time zone to UTC, the zone of the server the reference was made on; on a machine in another zone the blank setting would shift undated-zone dates, see above.
