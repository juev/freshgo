# Feed refresh

Status: implemented for selecting, fetching, reading and storing, for cleanup, for auto-read, for filter actions, for the full text of articles, for adding a feed, for the upkeep of icons and for WebSub.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `FreshRSS_feed_Controller::actualizeFeeds`, `keepMaxUnreads`, `FreshRSS_Feed::load`, `loadEntries`, `cleanOldEntries`, `markAsReadUponGone`, `markAsReadMaxUnread`, `FreshRSS_Entry::applyFilterActions`, `FreshRSS_EntryDAO::addEntry`, `updateEntry`, `updateLastSeen`, `commitNewEntries`, `cleanOldEntries`, `app/actualize_script.php` and `cli/purge.php` of FreshRSS at commit `219eaf58`.

## Purpose and scope

`internal/refresh` keeps stored entries in step with the feeds. `freshgo refresh` runs it once, `freshgo serve` on a schedule; `freshgo purge` runs the cleanup alone. Covers plan requirements R3, R4 and R10 and the pipeline side of R5, R6 and R8.

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

Auto-read and cleanup. Settings are read under the names FreshRSS gives them: `mark_when` and `archiving` in the user's settings, attributes of the feed and of the category. A value of another type than expected counts as absent.

- F18. A new entry is stored read when entries are read on arrival: `read_upon_reception` of the feed, else `mark_when.reception` of the user. A changed entry that is unread becomes read by the same rule.
- F19. A new entry is stored read when its title is among the titles of the latest N entries of the feed, by identifier. N is `read_when_same_title_in_feed` of the feed (`false` turns the rule off for the feed), else `mark_when.same_title_in_feed` of the user. Entries handled earlier in the same refresh count as well, changed ones included, so of two new entries with one title the second is read. An entry without a title is compared by its identifier instead, so untitled entries are not repeats of one another. A changed entry is never marked by its title.
- F20. In a category with `read_when_same_title_in_category` or `read_when_same_guid_in_category`, a new entry is stored read when its title, or its identifier, is among those of the latest N entries of all feeds of the category; N is the value of the attribute, and a value that is not a positive number means all entries. Feeds of such a category store their entries one after another within a run, so that each sees what the other brought.
- F21. `EntryAutoRead` is called for every rule of F18–F20 that applies to an unread entry, after `EntryBeforeInsert` and before `EntryBeforeAdd` or `EntryBeforeUpdate`.
- F27. Before any of this, the text of new and changed entries is completed from their pages or cut by the content filter: `fulltext.md`.
- F26. Filter actions follow the rules of F18–F20 for the same entry and label new entries after `EntryBeforeAdd`: `search.md`, Q11–Q14. The rules of the user, the categories and the labels are read once per run, those of a feed when it is refreshed.
- F22. After the entries of a feed are stored, also when the feed answered 304, its old entries are deleted (storage S17). The settings are `archiving` of the feed, else of its category, else of the user, else the defaults of FreshRSS: `keep_period` `P3M`, `keep_max` 200, `keep_min` 50, `keep_favourites` and `keep_labels` true, `keep_unreads` false. `keep_period` is an ISO 8601 duration counted back from the time of the refresh by the calendar of the user's time zone; `false` or 0 turns `keep_period`, `keep_max` or `keep_min` off. Settings with neither a period nor a maximum, an empty list among them, delete nothing. A period that cannot be read is reported in the log and the rule is off.
- F23. Then, with `read_upon_gone` of the feed, else `mark_when.gone` of the user, unread entries the feed listed for the last time more than 10 seconds before this refresh become read.
- F24. Then, when the refresh added or rewrote an entry, deleted one or marked one by F23, the feed keeps at most N unread entries: all but the N newest by identifier become read. N is `keep_max_n_unread` of the feed, else `mark_when.max_n_unread` of the user; a negative or absent value is no limit.
- F28. `Refresher.AddFeed` subscribes a user to an address: the address is trimmed, shown to `CheckURLBeforeAdd`, given `https://` when it names no scheme, and refused when it is not an http(s) URL. A feed read as RSS or Atom is fetched and read first: a document that is not a feed is searched for the first feed it announces with `<link rel="alternate">` of a feed media type or `rel="feed"`, which is then fetched in its place, once; an address that has moved for good is replaced by where it moved. Name, site and description the caller left empty come from the feed. An address the user already has is `ErrAlreadySubscribed`; a feed `FeedBeforeInsert` drops is `ErrRefused`. The feed is stored and its entries are stored from the document already fetched. Feeds of other kinds are stored unchecked and refreshed at once; a failure marks them as failing and is not an error of the call.
- F29. `Refresher.RefreshFeed` refreshes one feed of a user at once, whatever its period and whether or not it is muted.
- F30. After a refresh of a feed that succeeded, a 304 included, the icon of the feed is looked after when the Refresher was given an icon service: `greader-api.md`, A33 and A34. The feed is read again first, for the refresh may have learnt its site.
- F31. With WebSub on, a feed whose hub is trusted is due once in 24 hours or by its own period, whichever is longer; after a poll the topic of the feed is recorded and its hub is asked to push; `Refresher.Push` stores a pushed document through the same steps as F8–F21 and F26–F27, without F22, F23 and without counting as a poll: `websub.md`, W2, W3 and W7–W9.
- F25. `freshgo purge` applies F22 to every feed of every user without fetching anything and prints the number of deleted entries per user.

## Decisions

- **Cleanup runs at every refresh of a feed**, not at one in thirty chosen at random as in FreshRSS: the result is the same sooner, and a refresh is repeatable.
- **Gone entries are found by one rule, also when the feed came back empty.** FreshRSS marks every entry of an empty feed read and stamps the user's modification time; freshgo applies the 10-second rule, which gives the same entries for a feed refreshed less often than that, and leaves `last_user_modified` to the user.
- **Hooks are not called for F23 and F24**, as in FreshRSS: they are single statements over the feed.
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

- New entries are in the table when the cleanup of their refresh counts entries for `keep_max` and `keep_min`; FreshRSS commits them after it and counts them at the next cleanup.
- F20 holds within one process. Two processes refreshing feeds of one category at the same moment can each store the shared entry unread.
- `EntryAutoRead` is not called for a changed entry that is already read; FreshRSS calls it with `upon_reception` whatever the state.
- A changed feed setting does not force a re-read while the server keeps answering 304. FreshRSS has the same limitation through its cache; the subscription editing of step 11 clears the validators.
- With a blank `timezone` the server's zone applies, as in FreshRSS. Moving an installation to a server in another zone shifts dates written without a zone, and with them the identifiers of entries keyed by date.

## Verification scenarios

`internal/refresh`, each on SQLite and, under `make test-integration`, on PostgreSQL.

- F31: the tests of `websub_test.go`, listed in `websub.md`.
- F28: `TestAddFeed` (one request for the document; what the caller chose stands; a second subscription is refused, another user's is not), `TestAddFeedFollowsThePage`, `TestAddFeedOfAnotherKind`, `TestAddFeedRefusals`. F29: `TestRefreshFeed`. F30: `TestRefreshKeepsIcons`.

- R3, F8, F10, F11: the reference installation imported and refreshed from its own corpus through a proxy that keeps the feed addresses → no new and no updated entries, every entry equal to what it was except hash and `last_seen`; a second refresh, now comparing hashes → the same; one item added to `rss.xml` → one new entry for each of the two subscribers, with an identifier above all earlier ones. Covers every feed kind, `atom:id`, RSS `guid`, items without an identifier, non-ASCII and `&` in identifiers, force-https domains. `TestRefreshOfImportedInstallation`.
- F8, F12: `TestNewFeedIsFilledIn`, `TestRefreshAddsOnlyWhatIsNew`.
- R4, F9: `TestChangedEntryIsUpdated`, `TestChangedEntryBecomesUnreadWhenAsked`. F10: `TestImportedEntryIsNotTakenAsChanged`.
- F7: `TestRepeatedGUIDs`. F1–F3: `TestWhichFeedsAreRefreshed`. F5: `TestUnchangedFeed`. F12 redirects: `TestFeedAddressFollowsPermanentRedirect`. F13: `TestFailedFeed`. F6: `TestScrapedFeedKinds`. F14: `TestConcurrentRefreshOfOneFeed`, `TestLockFeedSerializes` in `internal/store`. F16: `TestScheduleRunsUntilCancelled`.
- R10, F22: `TestPurgeMatchesFreshRSS` builds the feeds of `testdata/reference/oracle/purge-cases.json` and expects per group of entries what `cli/purge.php` of FreshRSS 1.30.1 left (`purge.json`): the defaults, fewer entries than `keep_min`, everything listed at the last refresh, period alone, maximum with a tie at the boundary, settings of the feed and of the category, kept starred, labelled and unread entries, settings that delete nothing. `TestRefreshDeletesOldEntries` is the scenario of R10 through a refresh; `TestRetentionIsInherited`, `TestPeriod` in `internal/feed`, `TestPolicySettings`.
- F18: `TestReadUponReception`. F19: `TestReadWhenSameTitleInFeed`. F20: `TestReadWhenSameInCategory`. F21: the three of them. F23: `TestReadUponGone`, with a 304 among the refreshes. F24: `TestKeepMaxUnread`. F25: `TestPurge` in `cmd/freshgo`.
- F15–F17 through the commands: `TestRefresh`, `TestServeRefreshesUntilStopped` in `cmd/freshgo`; settings: `TestRefreshSettings` in `internal/config`.

The reference test sets the users' time zone to UTC, the zone of the server the reference was made on; on a machine in another zone the blank setting would shift undated-zone dates, see above.
