# Storage

Status: implemented for users, categories, feeds, entries, labels, installation-wide settings, feed icons and WebSub subscriptions.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; FreshRSS schema in `app/SQL/install.sql.sqlite.php` and `app/SQL/install.sql.pgsql.php` at commit `219eaf58` for the data that import has to carry over.

## Purpose and scope

`internal/store` keeps freshgo data in SQLite or PostgreSQL. This document fixes what the rest of the code and the import from FreshRSS may rely on: the tables, the identifier rules and the error contract.

Out of scope: MySQL/MariaDB, going back to FreshRSS with a freshgo database, the search language as SQL (the database gets only the part of a query that needs no texts, see S33).

## Requirements

- S1. One database holds all users. Every row of a category, feed, entry or label belongs to a user, and one user's data is not reachable through another user's identifier.
- S2. Identifiers of categories, feeds, labels and entries are unique within a user, not across the database. Two users can both have feed 1 and category 1. Import relies on this to keep FreshRSS identifiers unchanged.
- S3. A caller may give an identifier explicitly (import); it is stored as given. Otherwise the store issues one. Issued identifiers of categories, feeds and labels grow by one and are never reused, including after an explicit identifier was stored: the next issued one is greater than every identifier seen so far.
- S4. Every user has category 1, named `Uncategorized` on creation. A feed created without a category goes there.
- S5. An entry identifier is a Unix time in microseconds, strictly increasing within a user:
  - a new entry gets the current time, or the previous identifier plus one when the clock has not moved past it, including a clock that went backwards;
  - entries of one batch get consecutive identifiers in the order given;
  - an explicit identifier is kept, and later issued identifiers are greater than it;
  - batches written for one user commit in identifier order: after a client has seen identifier N, no smaller identifier appears.
- S6. `guid` is unique within a feed. Inserting a batch with a `guid` the feed already has fails with `ErrConflict` and adds nothing from the batch.
- S7. A category, a feed and a label name are unique within a user for their kind. Besides, a label cannot take the name of an existing category, nor a category the name of an existing label, on creation and on renaming: both are addressed as `user/-/label/<name>` in the Google Reader API.
- S8. Errors: a missing row is `ErrNotFound`, a broken uniqueness rule is `ErrConflict`; both are matched with `errors.Is`.
- S9. The schema is upgraded on `Open` by numbered migrations embedded in the binary. Pending migrations apply in one transaction. A database whose schema is newer than the binary is refused.
- S10. Behaviour is the same on SQLite and PostgreSQL.
- S11. Installation-wide values (`settings`: the token salt, the extra force-https domains) are read and replaced by name; an unset name is `ErrNotFound`.
- S12. A feed can have one custom icon, replaced on the next write and removed with the feed. It is stored with the hash it is served by and the time it was set, and is read by that hash; an empty hash matches nothing.
- S27. A user name is what FreshRSS accepts (`ValidUserName`): up to 39 characters of `0-9 a-z A-Z _ . @ -`, not starting with `.`, `@` or `-`, a lone `_` excluded. API tokens start with the name. The store does not enforce it: callers that take a name from outside check it.
- S28. `SetAPIPasswordHash` replaces the API password hash of a user, `DeleteUser` removes a user with their categories, feeds, entries, labels, label links, custom icons and identifier counters; both give `ErrNotFound` for an unknown user. Other users are untouched, and a new user of the same name starts empty.
- S29. What an administrator sets for the whole installation (`System`: title, language, default user, the way users log in, anonymous access, switches of the API and of e-mail validation, limits) is one JSON value in `settings`, read over the defaults: a setting that is not stored has the default of FreshRSS, except that the API is on.
- S30. A login to the web interface (`sessions`) is kept by the SHA-256 of its secret, with the user, the times it was created, last used and last authenticated, when it ends, and whether it outlives the browser. A session is not found from the second it ends; the sessions of a user are deleted with the user and can be ended all at once, one excepted.
- S31. `UpdateUserSettings` rewrites the settings of a user through a function over the members of the JSON object; changes of one user made at once are applied one after another, a change that fails stores nothing. `CreateUser` makes the first user of an installation its default user.
- S32. `ListPage` lists the entries of a user the way a reader pages through them (`Listing`): narrowed by an `EntrySet` and by the read and the starred state, each alone or as "unread or starred", sorted by the time they were added, by their date, by title, by the name of their feed, or shuffled, in either direction, entries equal in the order going by identifier. Titles and names are compared byte by byte on both engines. A page has at most `Limit` entries and comes with the place the next one starts at, empty when nothing follows; a place is opaque, belongs to the order it came from, and anything else given as one is `ErrCursor`. Pages follow each other by the values of the last entry shown, not by its position: entries that arrive or go between two pages make no entry appear twice and hide none. A shuffle is picked by `Seed`: the same seed lists the same entries in the same order, page after page.
- S33. A listing with a search (`Listing.Search`, a query of `internal/search`) has exactly the entries of the listing that `search.Query.Match` accepts, with the category of the feed and the labels of the entry known to it. The database leaves out the entries `Query.Condition` rules out (`search.md`, Q16); the rest is read in the order of the listing and matched until the page is full.
- S34. `UnreadFavorites` counts the starred entries the user has not read.
- S14. A feed keeps the HTTP validators of its last fetched copy (`http_etag`, `http_last_modified`). A negative `ttl` is a muted feed whose period is the absolute value.
- S15. A feed and an entry can be replaced as a whole, except their identifiers and, for an entry, its feed and guid; a missing row is `ErrNotFound`. `LockFeed` inside a transaction makes other transactions that lock or update the same feed wait until it ends.
- S16. The state of entries (identifier, hash, read, starred, time of the user's last change) is read by guid for a feed, and `last_seen` is set by guid or for all entries seen since a given time; both work for any number of guids.
- S17. Old entries of a feed are deleted by the rules of `FreshRSS_EntryDAO::cleanOldEntries` (`DeleteOldEntries`): an entry goes when the feed last listed it before a given time, or when at least a given number of entries were listed after it, entries listed at the same moment as the first one over that number included. Starred, labelled and unread entries and the given number of most recently listed ones stay when asked; the entries with the greatest `last_seen` of the feed always stay.
- S18. Unread entries of a feed become read in bulk: those last listed before a given time (`MarkUnseenEntriesRead`), and all but the given number of newest by identifier (`KeepNewestUnread`). Guid and title of the newest entries of a feed or of a category are listed with an optional limit (`LatestFeedEntries`, `LatestCategoryEntries`).
- S19. Entries are listed, whole or by identifier only, in identifier order, either direction (`ListEntries`, `ListEntryIDs`). A listing is narrowed by where the entries come from (`EntrySet`: a feed, the feeds of a category, a label, a range of feed priorities, starred only; the conditions add up), by the read and the starred state, by time (the rules of the Google Reader API of FreshRSS, see `greader-api.md`, A18), by an identifier to start at, itself included, and by a limit. `EntriesByIDs` returns the entries among any number of identifiers, each once.
- S20. The user's changes of state are stamped in `last_user_modified`: `SetEntriesRead` changes and stamps only the entries whose state differs and returns their number, `SetEntriesFavorite` stamps every entry given, `MarkSetRead` marks read the unread entries of an `EntrySet` up to an identifier.
- S21. Counts are computed on demand: unread entries and the greatest entry identifier per feed (`FeedCounts`) and per label (`LabelCounts`); a feed or label without entries is absent. `EntryLabels` gives the names of the labels of entries in the order the labels were created.
- S22. A label is renamed (`ErrNotFound` if missing, `ErrConflict` for a taken name), deleted together with its links, put on and taken off any number of entries; entries that do not exist are passed over.
- S23. Deleting a category moves its feeds to the default one; the default category itself is not deleted. Deleting a feed removes its entries, their label links and its custom icon; a missing feed is `ErrNotFound`.
- S24. An icon found at a site is kept by the hash of the place it was looked for (`icons`): the place, the image and its media type, when the image last changed and when the place was last asked. A row without an image records an attempt that found nothing.
- S25. `Salt` returns the installation's secret and creates a random one on first use; an imported salt is returned as it is.
- S26. A subscription to a WebSub hub is kept per topic (`websub_subscriptions`): hub, callback key, secret, the times the lease was asked for and ends, and whether the hub is failing. It is read by topic and by key, replaced as a whole, listed and deleted; a key belongs to one subscription (`ErrConflict`). A feed carries the topic it announces (`websub_topic`), and `FeedsByTopic` returns the feeds of all users that announce a topic. See `websub.md`.
- S13. The counters of S3 can be raised explicitly and never move back, so that import carries over counters that are ahead of the largest identifier still in use.

## Invariants and compatibility

- Text is stored as plain UTF-8. FreshRSS keeps most text HTML-encoded; import decodes it once.
- `entries.guid` is the exception: an opaque ASCII key kept in the form FreshRSS computes it (HTML-encoded, up to 767 bytes), never decoded. See `guid.md`.
- `entries.hash` is NULL until freshgo has computed its own change hash. NULL means "fill in on the next refresh without treating the entry as modified"; the FreshRSS hash is not imported.
- Times are Unix seconds, zero for "never". `feeds.error` and `categories.error` are the time of the last failure, as in FreshRSS.
- `attributes` (categories, feeds, entries, labels) and `users.settings` are JSON objects stored as text with FreshRSS key names. `entries.authors` and `entries.tags` are JSON arrays of strings instead of the `;A; B` and `#a #b` strings of FreshRSS.
- `feeds.http_auth` is `user:password` in clear text (FreshRSS stores it base64-encoded, which is not protection either).
- FreshRSS columns that are not carried over: `feed.cache_nbEntries`, `feed.cache_nbUnreads` (counted on demand) and the whole `entrytmp` table.
- The schema is persisted data: once a release is tagged, a change needs a new migration file, not an edit of an existing one.

## Decisions

- **Composite primary keys `(user_id, id)`** follow from S2. A database-wide sequence would renumber imported data.
- **Counters in the `sequences` table**, one row per user and kind, instead of `MAX(id) + 1`: a deleted feed must not hand its identifier, which an API client may still hold, to a new feed. FreshRSS gets the same from `AUTOINCREMENT`/`SERIAL`.
- **Entry identifiers come from the same table**, updated in the transaction that inserts the entries. The update locks the user's row until commit, which gives the commit ordering of S5 on PostgreSQL; SQLite has a single writer anyway.
- **One implementation for both engines** rather than an interface with two: queries use `?` placeholders (rewritten to `$n` for PostgreSQL) and SQL both engines accept. Engine differences are confined to the connection setup, error mapping and the migration files under `internal/store/migrations/<engine>/`. A consumer that needs a narrower dependency declares its own interface.
- **JSON is stored as `TEXT`** on both engines, not `jsonb`: nothing queries inside it in SQL, and one column type keeps the code identical.
- **A feed keeps a mandatory category**: `DeleteCategory` moves the feeds to category 1 in the transaction that deletes the category, and the foreign key refuses a deletion that would leave a feed without one.
- **Icons of sites are shared and never deleted**: the row belongs to a place, not to a feed, and several users may look at the same place. Custom icons hang off their feed instead and go with it.
- **Indexes follow the pages of the web interface** (migration `0006`): unread counts by feed are answered from an index that holds the identifier too, and listing by date from an index on the date. Sorting by title, by feed name and shuffling have none and sort the listing; so does every search in an order other than by identifier.
- **A search reads rows, it does not page by offset or in portions.** One statement streams the listing in order and is abandoned when the page is full, so a search that matches often reads little and one that matches rarely reads the listing once, sorted once.
- **SQLite runs with** `foreign_keys`, WAL and immediate transactions, so that a writer waits for the lock at `BEGIN` instead of failing midway.

## Import from FreshRSS

`freshgo import -data <FreshRSS data directory>` (`internal/importer`) fills an empty database from a FreshRSS installation on SQLite or PostgreSQL. FreshRSS 1.29 or later is expected: older schemas lack columns the import reads.

- I1. The destination must have no users; otherwise the import fails with `ErrNotEmpty` and changes nothing.
- I2. The import is one transaction: a failure on any user leaves the destination as it was.
- I3. Users are the directories in `users/` that have a `config.php`, symbolic links to directories included, except the template `_`. A directory without `config.php` is reported and skipped. The user's `config.php` becomes `users.settings` as JSON, except `apiPasswordHash`, which goes to its own column. `salt` of the system `config.php` and the domains of `force-https.txt` go to `settings`. So do the system settings freshgo has a use for (S29), each with the default of FreshRSS when `config.php` does not set it; a value of the wrong type is reported and keeps its default. Together with I4 this keeps issued API tokens valid.
- I4. Categories, feeds, labels, entries and label links keep their identifiers. The identifier counters of FreshRSS are carried over, so identifiers of objects deleted before the import are not reused either.
- I5. `category.name`, `feed.url`, `feed.name`, `feed.website`, `feed.description`, `feed.pathEntries`, `entry.title`, `entry.link`, `entry.author`, `entry.tags` and `tag.name` are decoded from the `htmlspecialchars` form once: `&amp;`, `&lt;`, `&gt;`, `&quot;` and the encodings of `'`. Other entities are left alone, as `htmlspecialchars_decode` leaves them. `entry.content` is HTML and `entry.guid` is a key; neither is touched. A title equal to the guid is how FreshRSS stores an entry without a title; it is imported as an empty title.
- I6. `entry.author` and `entry.tags` are split the way `FreshRSS_Entry::_authors` and `::_tags` read them. `feed.httpAuth` is decoded from base64 and then from the `htmlspecialchars` form, which is the order FreshRSS applies when it uses the credentials. An `attributes` value of `[]`, an empty string or NULL becomes `{}`.
- I7. Read and favorite states, dates, `lastSeen`, `lastModified` and `lastUserModified` are copied. `entry.hash` is not.
- I8. A feed with the `customFavicon` attribute gets its icon from `favicons/<crc32b(salt . feed id . user name)>.ico`, stored under the hash freshgo serves it by (`favicon.CustomHash`) with the time of the file. Icons fetched from sites and the `PubSubHubbub/` directory are not imported.
- I9. The import reports, without failing: a user without an API password; a feed whose `unicityCriteria` hashes the content (freshgo sanitizes content differently, so the first refresh may add each current entry once more); a missing custom icon file; attributes that are not a JSON object (dropped); entries left in `entrytmp` by an interrupted refresh (not imported).
- I10. MySQL/MariaDB installations are refused.
- I11. The default category gets the name FreshRSS shows it by, which is the translation of "Uncategorized" into the language of the user (`language` in the user's `config.php`) whatever name the database holds; an unknown language means English. API clients know the category by that name. If another category of the user already has it, the stored name is kept and the import reports that.

For PostgreSQL the connection comes from the `db` section of `config.php`; `-source-database-url` replaces it when the database is reachable under another address.

## Verification scenarios

In `internal/store`, each run on SQLite and, under `make test-integration`, on PostgreSQL (S10).

- S1, S2: two users create a category, a feed and a label → both get the same identifiers; a feed is not found through the other user. `TestIdentifiersArePerUser`, `TestFeedRoundTrip`.
- S3: a feed stored with id 40, then one without → 41; a feed stored with id 7, then one without → 42; id 40 again → `ErrConflict`. `TestIdentifiersArePerUser`.
- S4: a new user has exactly category 1; a feed without a category lands in it. `TestUsers`, `TestFeedRoundTrip`.
- S5: fixed clock → identifiers start at the current microsecond and continue through a stopped and a reversed clock; an explicit identifier from the future is kept and the next one follows it; eight concurrent writers get unique identifiers, consecutive within a batch and above their previous batch. `TestEntryIDs`, `TestEntryIDsConcurrent`.
- S6: a batch whose second entry repeats a `guid` of the feed → `ErrConflict`, entry count unchanged; the same `guid` in another feed or for another user is accepted. `TestDuplicateGUID`.
- S7: a label named like an existing label or category → `ErrConflict`. `TestTags`. A category created or renamed to the name of a label, a label renamed to the name of a category or label → `ErrConflict`. `TestCategoriesAndLabelsShareNames`, `TestLabels`.
- S19–S23, in `internal/store/streams_test.go` over two users with the same identifiers: `TestListEntries` (every kind of set, state, time bound, direction, start and limit), `TestEntriesByIDs` (unknown and repeated identifiers, 1200 at once, label names), `TestMarkEntries`, `TestMarkSetRead`, `TestCounts`, `TestLabels`, `TestDeleteCategoryAndFeed`; each checks that the other user is untouched.
- S32: `TestListPage` (every order and direction, sets and states, pages of 1, 2, 3, 6 and 7 of six entries, the number of pages), `TestListPageShuffled`, `TestListPageWhileEntriesArrive`, `TestListPageRefusesForeignPlace`; the other user has an entry under the same identifier. S34: `TestUnreadFavorites`.
- S33: `TestSearchListsWhatMatches` runs the 457 queries of `testdata/reference/oracle/search-cases.json` and 43 more about labels, identifiers and dates over the seven entries of the reference, with four labels, and compares what `ListPage` returns, whole, two a page and sorted by title, with what `Match` picks among all the entries; the other user has the same entries with every label.
- S33, speed: `make bench` (`BenchmarkSearchRareMatch`) fills a database with 100 000 entries of about 2 KB of content and fails when the first page of a search only the oldest entry matches takes more than 2 s. Measured on 2026-10-06 on an Apple M4 Pro: by identifier 0.6 s on SQLite and 0.8 s on PostgreSQL; sorted by title 1.2–1.6 s and 0.8–0.9 s; among the unread, a third of the entries, 0.3 s on both. Without a search the first page by date takes under 1 ms, the counts of the tree 16 ms and 8 ms.
- S8: unknown user, feed, entry → `ErrNotFound`. `TestUsers`, `TestFeedRoundTrip`, `TestEntryRoundTrip`.
- S9: reopening a database keeps its data and schema version; a database with a newer schema version is refused. `TestOpenTwiceKeepsData`, `TestOpenRefusesNewerSchema`.
- S14, S15: `TestFeedRoundTrip`, `TestUpdateFeed`, `TestUpdateEntry`, `TestLockFeedSerializes`. S16: `TestEntryStatesAndLastSeen`.
- S17: `TestCleanupTouchesOneFeed`; the rules are compared with FreshRSS by `TestPurgeMatchesFreshRSS` in `internal/refresh`. S18: `TestAutoReadStatements`.
- S26: `TestWebSubSubscriptions`.
- S29: `TestSystem`. S30: `TestSessions`. S31: `TestUpdateUserSettings`, `TestFirstUserIsTheDefault`.
- S27: `TestValidUserName`. S28: `TestSetAPIPasswordHash`, `TestDeleteUser`; from the command line to an API client, `TestUsersFromTheCommandLine` in `cmd/freshgo`.
- S11: `TestSettings`. S12: `TestCustomIcon`. S24: `TestIcons`. S25: `TestSalt`. S13: counters raised to 5 and 9 → next category 6, next feed 10; lowering has no effect. `TestRaiseCounters`.
- Field fidelity: every column of a feed, category, entry and label survives a write and a read, including non-ASCII text, markup characters and a NULL hash. `TestFeedRoundTrip`, `TestEntryRoundTrip`, `TestTags`.
- I1: a second import → `ErrNotEmpty`, content unchanged. `TestImportRefusesNonEmptyDatabase`; `TestImport` in `cmd/freshgo`.
- I2: second user's database damaged → error naming the user, no users and no salt in the destination, which then accepts a good installation. `TestImportIsAllOrNothing`.
- I3–I8: the reference installations in `testdata/reference` (two users, every feed kind, read, starred and labelled entries, a custom icon) imported from SQLite and from PostgreSQL into SQLite and PostgreSQL → every row compared with the source tables, plus values known from the corpus. `TestImportFromSQLite`, `TestImportFromPostgres`, `TestSplitAuthors`, `TestSplitTags`, `TestDecodeText`.
- I5, untitled entries: `TestImportUntitledEntry`.
- I3: `TestImportFollowsSymlinkedUser`, `TestImportSystemSettings`, `TestReadSystemSettings`. I9: `TestImportWarnings`. I10: `TestImportRejectsUnsupportedSources`. I11: `TestImportNamesDefaultCategoryInTheLanguageOfTheUser`.
