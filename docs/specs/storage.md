# Storage

Status: implemented for users, categories, feeds, entries, labels, installation-wide settings and custom feed icons. Tables for fetched icons and WebSub subscriptions are added by the plan steps that first need them (11, 12) and are not described here yet.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; FreshRSS schema in `app/SQL/install.sql.sqlite.php` and `app/SQL/install.sql.pgsql.php` at commit `219eaf58` for the data that import has to carry over.

## Purpose and scope

`internal/store` keeps freshgo data in SQLite or PostgreSQL. This document fixes what the rest of the code and the import from FreshRSS may rely on: the tables, the identifier rules and the error contract.

Out of scope: MySQL/MariaDB, going back to FreshRSS with a freshgo database, search in SQL.

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
- S7. A category, a feed and a label name are unique within a user for their kind. Besides, a label cannot take the name of an existing category: both are addressed as `user/-/label/<name>` in the Google Reader API.
- S8. Errors: a missing row is `ErrNotFound`, a broken uniqueness rule is `ErrConflict`; both are matched with `errors.Is`.
- S9. The schema is upgraded on `Open` by numbered migrations embedded in the binary. Pending migrations apply in one transaction. A database whose schema is newer than the binary is refused.
- S10. Behaviour is the same on SQLite and PostgreSQL.
- S11. Installation-wide values (`settings`: the token salt, the extra force-https domains) are read and replaced by name; an unset name is `ErrNotFound`.
- S12. A feed can have one custom icon, replaced on the next write and removed with the feed.
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
- **A feed keeps a mandatory category**: deleting a category that still has feeds is refused by the foreign key; the caller moves the feeds to category 1 first.
- **SQLite runs with** `foreign_keys`, WAL and immediate transactions, so that a writer waits for the lock at `BEGIN` instead of failing midway.

## Import from FreshRSS

`freshgo import -data <FreshRSS data directory>` (`internal/importer`) fills an empty database from a FreshRSS installation on SQLite or PostgreSQL. FreshRSS 1.29 or later is expected: older schemas lack columns the import reads.

- I1. The destination must have no users; otherwise the import fails with `ErrNotEmpty` and changes nothing.
- I2. The import is one transaction: a failure on any user leaves the destination as it was.
- I3. Users are the directories in `users/` that have a `config.php`, symbolic links to directories included, except the template `_`. A directory without `config.php` is reported and skipped. The user's `config.php` becomes `users.settings` as JSON, except `apiPasswordHash`, which goes to its own column. `salt` of the system `config.php` and the domains of `force-https.txt` go to `settings`. Together with I4 this keeps issued API tokens valid.
- I4. Categories, feeds, labels, entries and label links keep their identifiers. The identifier counters of FreshRSS are carried over, so identifiers of objects deleted before the import are not reused either.
- I5. `category.name`, `feed.url`, `feed.name`, `feed.website`, `feed.description`, `feed.pathEntries`, `entry.title`, `entry.link`, `entry.author`, `entry.tags` and `tag.name` are decoded from the `htmlspecialchars` form once: `&amp;`, `&lt;`, `&gt;`, `&quot;` and the encodings of `'`. Other entities are left alone, as `htmlspecialchars_decode` leaves them. `entry.content` is HTML and `entry.guid` is a key; neither is touched.
- I6. `entry.author` and `entry.tags` are split the way `FreshRSS_Entry::_authors` and `::_tags` read them. `feed.httpAuth` is decoded from base64. An `attributes` value of `[]`, an empty string or NULL becomes `{}`.
- I7. Read and favorite states, dates, `lastSeen`, `lastModified` and `lastUserModified` are copied. `entry.hash` is not.
- I8. A feed with the `customFavicon` attribute gets its icon from `favicons/<crc32b(salt . feed id . user name)>.ico`. Icons fetched from sites and the `PubSubHubbub/` directory are not imported.
- I9. The import reports, without failing: a user without an API password; a feed whose `unicityCriteria` hashes the content (freshgo sanitizes content differently, so the first refresh may add each current entry once more); a missing custom icon file; attributes that are not a JSON object (dropped); entries left in `entrytmp` by an interrupted refresh (not imported).
- I10. MySQL/MariaDB installations are refused.

For PostgreSQL the connection comes from the `db` section of `config.php`; `-source-database-url` replaces it when the database is reachable under another address.

## Verification scenarios

All in `internal/store/store_test.go`, each run on SQLite and, under `make test-integration`, on PostgreSQL (S10).

- S1, S2: two users create a category, a feed and a label → both get the same identifiers; a feed is not found through the other user. `TestIdentifiersArePerUser`, `TestFeedRoundTrip`.
- S3: a feed stored with id 40, then one without → 41; a feed stored with id 7, then one without → 42; id 40 again → `ErrConflict`. `TestIdentifiersArePerUser`.
- S4: a new user has exactly category 1; a feed without a category lands in it. `TestUsers`, `TestFeedRoundTrip`.
- S5: fixed clock → identifiers start at the current microsecond and continue through a stopped and a reversed clock; an explicit identifier from the future is kept and the next one follows it; eight concurrent writers get unique identifiers, consecutive within a batch and above their previous batch. `TestEntryIDs`, `TestEntryIDsConcurrent`.
- S6: a batch whose second entry repeats a `guid` of the feed → `ErrConflict`, entry count unchanged; the same `guid` in another feed or for another user is accepted. `TestDuplicateGUID`.
- S7: a label named like an existing label or category → `ErrConflict`. `TestTags`.
- S8: unknown user, feed, entry → `ErrNotFound`. `TestUsers`, `TestFeedRoundTrip`, `TestEntryRoundTrip`.
- S9: reopening a database keeps its data and schema version; a database with a newer schema version is refused. `TestOpenTwiceKeepsData`, `TestOpenRefusesNewerSchema`.
- S11: `TestSettings`. S12: `TestCustomIcon`. S13: counters raised to 5 and 9 → next category 6, next feed 10; lowering has no effect. `TestRaiseCounters`.
- Field fidelity: every column of a feed, category, entry and label survives a write and a read, including non-ASCII text, markup characters and a NULL hash. `TestFeedRoundTrip`, `TestEntryRoundTrip`, `TestTags`.
- I1: a second import → `ErrNotEmpty`, content unchanged. `TestImportRefusesNonEmptyDatabase`; `TestImport` in `cmd/freshgo`.
- I2: second user's database damaged → error naming the user, no users and no salt in the destination, which then accepts a good installation. `TestImportIsAllOrNothing`.
- I3–I8: the reference installations in `testdata/reference` (two users, every feed kind, read, starred and labelled entries, a custom icon) imported from SQLite and from PostgreSQL into SQLite and PostgreSQL → every row compared with the source tables, plus values known from the corpus. `TestImportFromSQLite`, `TestImportFromPostgres`, `TestSplitAuthors`, `TestSplitTags`, `TestDecodeText`.
- I3: `TestImportFollowsSymlinkedUser`. I9: `TestImportWarnings`. I10: `TestImportRejectsUnsupportedSources`.
