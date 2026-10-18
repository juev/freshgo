# Fever API

Status: implemented.
Sources: GitHub issue 138 and its decisions (an installation setting, off by default; the key stored when the API password is set); the Fever protocol as readers use it (`?api` with `groups`, `feeds`, `favicons`, `items`, `links`, `unread_item_ids`, `saved_item_ids` and `mark`); the path `/api/fever.php` where FreshRSS serves it.

## Purpose and scope

`internal/fever` serves the part of the Fever API that readers such as Reeder and Fiery Feeds use, next to the Google Reader API of `internal/greader` (`greader-api.md`), over the same entries (`store.EntryQuery`, `store.EntrySet`) and the same hooks.

Out of scope: the XML form of the answers (`?api=xml`), inline favicons, hot links (`links`) and sparks, and the key of a user whose API password was set before this API existed or came with an import.

## Requirements

### Addresses and access

- V1. The API answers at `/api/fever.php` and nowhere else; `/api/fever.php` with a query is the API, `/api/fever.phpx` and `/api/fever.php/…` belong to the web interface. A request needs `api` in the query; without it the answer is 400 `Bad Request!`.
- V2. The API is an installation setting, `fever_enabled` of the system settings, off by default and saved on the page of authentication of the administration beside `api_enabled`. It answers only while both are on; otherwise every request is 503 `Service Unavailable!`, as the Google Reader API does with `api_enabled` off.
- V3. A request carries `api_key` in the body (not in the address, where it would reach logs): the md5 hash of `name:password` in hexadecimal, read whatever its case, with the name as the user has it and the API password. The user is the one whose stored key it is. A request without a key, with a key nobody has, or of a user who is disabled or has yet to confirm an address where that is required (`greader-api.md`, A3, A38) is answered with 200 and `{"api_version":3,"auth":0}`. The stored key is compared in constant time.
- V4. The key is stored in `users.fever_key` whenever the API password is set: by the profile, by the administration for a user, when an administrator adds a user, and by the commands `user create` and `user passwd`. Taking the API password away empties it. A user with an empty key cannot log in through Fever: a user who came with an import, or set the API password before this API existed, sets it again.
- V5. Failures are counted like those of a login (`greader-api.md`, A40), in the same guard, per address alone, as Fever sends no name: a request with a key that does not open an account counts as a failure before the key is looked at, one that does clears the count. After five failures in a row, requests from the address are answered 429 with `Retry-After` for 30 seconds, then twice as long with every further failure, up to 15 minutes. A request without a key is not a guess and is not counted.
- V6. One user never reaches the data of another: everything is looked up within the user whose key came.

### Answers

- V7. Every answer for a user has `api_version` 3, `auth` 1 and `last_refreshed_on_time`, the latest `last_update` of the feeds of the user (0 for none). The answer is JSON. Several things may be asked in one request; a change in `mark` is made before anything is read.
- V8. `groups` answers `groups` (`id`, `title`: the categories, the default one included, empty ones too) and `feeds_groups`. `feeds` answers `feeds` (`id`, `favicon_id` 0, `title`, `url`, `site_url`, `is_spark` 0, `last_updated_on_time`) and `feeds_groups`. A feed without a name is called by its address. `feeds_groups` is a list of `group_id` and `feed_ids` (comma-separated), one per category that has a feed. A hidden feed (priority -10 and below) is in neither.
- V9. `favicons` and `links` answer empty lists.
- V10. `items` answers `total_items`, the number of entries in the reading list (V12), and `items`: `id` (the entry identifier), `feed_id`, `title`, `author` (the authors joined by `, `), `html`, `url`, `is_saved`, `is_read` (0 or 1) and `created_on_time` (the date of the entry, else the second it was received). At most 50 are given. With `with_ids` (comma-separated, what cannot be read passed over, only the first 50 looked at) these are the entries that exist, oldest first, of any feed. Else with `since_id` they are the 50 oldest with a greater identifier, oldest first. Else with `max_id` they are the 50 newest with a smaller identifier, newest first; `max_id` of 0 or less leaves the bound out, 1 gives none. Else they are the 50 oldest. `EntryBeforeDisplay` sees every item and may hold it back.
- V11. `unread_item_ids` answers the identifiers of the unread entries of the reading list, `saved_item_ids` those of the starred entries of feeds that are not hidden, as one comma-separated string, oldest first, empty when there are none.
- V12. The reading list is that of the Google Reader API (`greader-api.md`, A13): the entries of feeds of priority 0 and above.

### Changes

- V13. `mark=item` with `id` and `as` `read`, `unread`, `saved` or `unsaved` changes the entry; saved is starred. `EntriesRead` is called, with the identifier, when the state changed, `EntriesFavorite` after a change of the star, as for `edit-tag` of the Google Reader API. An entry that does not exist, or is another user's, changes nothing and is answered like one that does.
- V14. `mark=feed` with `as=read` and `id` of a feed, or `mark=group` with `as=read` and `id` of a category, reads the unread entries of the feed, or of the category's feeds in the reading list, with an identifier at most `before` (Unix seconds; everything without it). Group 0 is every group: the whole reading list. A feed or group the user does not have changes nothing. A change is answered with the base document (V7).
- V15. A `mark` with an `id` that is not a number, an unknown `as`, an unknown kind, `as` other than `read` for a feed or group, or feed 0 is answered with 400 `Bad Request!` and changes nothing.

## Invariants and compatibility

- The Google Reader API and Fever share the entries, the hooks and the guard; neither keeps a session.
- The key is md5 of the name and the password by the definition of the protocol; it is no stronger than the password, and the same key opens the API for anyone who has read it. It is stored next to the bcrypt hash, so a copy of the database gives a guesser the md5 to attack offline: a password for apps of its own, and a long one.

## Decisions

- **An installation setting, off by default**, as agreed in the issue: the key is a weaker secret than the bcrypt hash, so a server that does not need Fever does not keep it answering. The key is stored whether the setting is on or not, so switching it on needs no new passwords.
- **The key is stored at the moment the API password is set**, as no other moment has the password. Users whose password was set earlier have none until they set it again; there is no way round that.
- **Failures are counted per address**, as Fever has no name to count by. The count is kept under a key no user name can have, so it never clashes with the count of a login.
- **`items` without a bound gives the oldest 50**, the same as `since_id=0`, so that a client that pages with `since_id` sees everything. `max_id=0` means no upper bound.
- **Favicons are empty**: icons are served by address (`greader-api.md`, A35), not as the inline images Fever carries.
- **Images are not sent through the media proxy**; text goes as stored, which is already cleaned.
- **Marking a feed or group does not call `EntriesRead`**, as `mark-all-as-read` of the Google Reader API does not.

## Verification scenarios

`internal/fever`, on SQLite and, under `make test-integration`, on PostgreSQL.

- V1–V3, V6: `TestAccess` (wrong, empty and upper-case keys, a key in the address, a user without a key, a disabled user, an unconfirmed address, both switches off). V5: `TestGuessesAreSlowedDown`.
- V7–V9, V12: `TestGroupsAndFeeds`. V10: `TestItemPaging` (since_id, max_id, with_ids, the page size, the fields of an item, another user's entry, a held-back item). V11: `TestUnreadAndSavedIDs`.
- V13, V15: `TestMarkItem`. V14, V15: `TestMarkFeedAndGroup`.
- V2: `TestRoutes` in `cmd/freshgo` (the path), the switch of the form in `TestAdministration` of `internal/web`. V4: `TestSetAPIPasswordHash` in `internal/store`, the profile in `TestProfile` and the administration in `TestAdministration` of `internal/web`, the commands in `TestUsersFromTheCommandLine` of `cmd/freshgo`.

Not run against a real client: the behaviour of Reeder, Fiery Feeds and others is as the protocol is described, not as it was seen.
