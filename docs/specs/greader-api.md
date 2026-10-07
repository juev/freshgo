# Google Reader API

Status: implemented.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `p/api/greader.php`, `FreshRSS_Entry::toGReader`, `FreshRSS_EntryDAO`, `FreshRSS_Import_Service`, `app/views/helpers/export/opml.phtml`, `lib/favicons.php` and `p/f.php` of FreshRSS at commit `219eaf58`; the answers of FreshRSS 1.30.1 recorded in `testdata/reference/api/`.

## Purpose and scope

`internal/greader` serves the Google Reader API the way FreshRSS does, so that a client set up for a FreshRSS server keeps working after the installation has been imported. `internal/opml` reads and writes subscriptions, `internal/favicon` finds and serves the icons of feeds. Covers plan requirements R2, R7 and R13.

Out of scope: the Fever API, the web interface, uploading a custom icon (imported ones are served), dynamic OPML categories (their address is kept and exported, nothing fetches it).

## Requirements

### Addresses and access

- A1. The API answers at the root of the server (`/accounts/ClientLogin`, `/reader/api/0/…`) and, with the same answers, under `/api/greader.php`, where FreshRSS serves it. `/api/greader.php` alone, without a query, answers `OK`.
- A2. `ClientLogin` takes `Email` and `Passwd` from the body or the query and answers `SID=`, `LSID=null` and `Auth=` lines with `<user>/<sha1(salt . user . apiPasswordHash)>`. The salt and the hash are the imported ones, so a token FreshRSS issued stays valid. A wrong password, an unknown user or a user without an API password is 401; a user name FreshRSS would not accept, or a missing parameter, is 400.
- A3. Every other request carries `Authorization: GoogleLogin auth=<user>/<token>`. No header or a wrong token is 401 with `Google-Bad-Token: true`; a disabled user (`enabled: false` in the settings) is 401; a malformed user name is 400.
- A4. `token` answers the same sha1 padded with `Z` to 57 characters. `edit-tag`, `rename-tag`, `disable-tag` and `mark-all-as-read` check the form field `T` against it and answer 401 for another value; an empty `T` (FeedMe) and `x` (Reeder) pass. `subscription/edit`, `subscription/quickadd` and `subscription/import` check no token, as in FreshRSS.
- A5. Answers carry the CORS headers of FreshRSS (`Access-Control-Allow-Origin: *`, `-Headers: Authorization`, `-Methods: GET, POST`, `-Max-Age: 600`), `X-Content-Type-Options: nosniff` and a `Content-Security-Policy` that forbids everything. `OPTIONS` is answered with 204.
- A6. An endpoint that is not known, or is called without what it needs, answers 400 `Bad Request!`. `tag/list`, `subscription/list` and `unread-count` without `output=json` answer 501. At most 1 MiB of a request body is read.
- A7. One user never reaches the data of another: identifiers are looked up within the user who asks.

### Lists

- A8. `tag/list`: the states `starred`, `reading-list`, `org.freshrss/main` and `org.freshrss/important`, then the categories as `type: folder`, then the labels as `type: tag` with `unread_count`.
- A9. `subscription/list`: every feed that is not hidden with `id` (`feed/<id>`), `title`, its one category, `url`, `htmlUrl`, `iconUrl` and `frss:priority` (`important`, `main`, `category`, `feed`).
- A10. `unread-count`: per category its feeds and then the category itself, then the labels, then `reading-list` with the total; `max` is the total. `newestItemTimestampUsec` is the greatest entry identifier: `"0"` for a feed or category without entries, `""` for a label without entries.
- A11. Order: categories by the bytes of their names; feeds within a category and labels by name as the language of the user orders names, numbers by value. A feed without a name is called by its address without the scheme.
- A12. Titles of entries and authors have `&`, `<`, `>` replaced by their fullwidth forms; names of feeds also `?`, `\`, `/`, `,` and `;`.

### Streams

- A13. Streams: `user/-/state/com.google/reading-list` (feeds of priority `category` and up), `…/starred` (starred entries of feeds that are not hidden), `user/-/state/org.freshrss/main`, `…/important`, `feed/<id>` or `feed/<address>` (any priority), `user/-/label/<name>`: the category of that name (its feeds of priority `category` and up) or, if there is none, the label. A feed, category or label the user does not have is an empty stream.
- A14. `stream/contents/<stream>` lists items; the stream may also be given in the parameter `s` (BazQux), and without any stream the reading list is meant (EasyRSS, FeedMe). The address of a feed and the name of a label are taken from the path as sent, so a slash in them has to be encoded. `stream/items/ids?s=<stream>` lists identifiers and takes `…/read` and `…/unread` as streams too. `stream/items/contents` returns the items of the posted `i` values.
- A15. Parameters of both listings: `n` (20 by default; 0 or less is no limit), `r=o` for oldest first, `c` for the continuation, `it` and `xt` to include and exclude `read`, `unread` or `starred`, `ot` and `nt` in seconds.
- A16. The continuation is the identifier of the last item of a full page. A request with `c` starts at that entry and leaves it out.
- A17. `it` and `xt` are combined bit by bit as in FreshRSS: `xt=read` and `it=unread` give unread entries, `xt=unread` and `it=read` read ones, `it=starred` starred ones; a combination that leaves nothing selected lists everything: `it=starred&xt=read`, `xt=starred` alone, `it=read&xt=read`.
- A18. `ot` keeps entries added, or changed by their feed, at that second or later. `nt` alone keeps those added and last changed at that second or earlier. Given together they widen the result: an entry passes when either accepts it.
- A19. An item identifier is accepted as decimal, as 16 hexadecimal digits, and with the `tag:google.com,2005:reader/item/` prefix; a string of digits not starting with zero is decimal. What cannot be read matches no entry. Lists of identifiers answer in decimal, items with the prefixed hexadecimal form.
- A20. An item has `id`, `crawlTimeMsec`, `timestampUsec`, `published`, `title`, `canonical`, `alternate`, `categories`, `origin` (`streamId`, `htmlUrl`, `title`), `summary.content`, and, when there are any, `enclosure` and `author`. `categories` are the reading list, the category of the feed, `org.freshrss/main` and `/important` or `/hidden` by priority, `read`, `starred`, the labels of the user and the tags the feed gave the entry.
- A21. The text is followed by the thumbnail and the enclosures written out as markup, unless the feed sets `display_enclosures` to false or the text already mentions their address; it is cut to 500 000 bytes on a character boundary. An entry without a title is called by the first 75 characters of its text, or by its identifier; one without a link links to its identifier if that is an address.
- A22. `stream/items/ids` for the client `newsplus` answers one identifier `0` instead of an empty list.

### Changes

- A23. `edit-tag` applies every `a` (add) and then every `r` (remove) to the entries of `i`: `…/read`, `…/starred`, `user/-/label/<name>`. Adding a label creates it when the user has none of that name; a name a category has is left alone. Entries that do not exist are passed over. Marking read stamps only the entries whose state changes, starring all of them.
- A24. `mark-all-as-read` marks the entries of `s` up to the identifier `ts` (everything without it): a feed by number, a category (leaving out its feeds of priority `important`) or label, the reading list and `…/unread` (feeds that are not hidden), `…/starred`, `org.freshrss/main`, `org.freshrss/important`; `…/read` changes nothing. Anything else, a `ts` that is not a number, or a feed given by address is 400.
- A25. `rename-tag` renames the category, or else the label, of `s` to `dest`; `disable-tag` deletes it: a category gives its feeds to the default one, which itself stays; a label comes off its entries. A new name that is taken changes nothing and still answers `OK`. An unknown name is 400.
- A26. `subscription/edit` with `ac=subscribe`, `unsubscribe` or `edit` works on every `s` (`feed/<id>` or `feed/<address>`); `t` are the titles, `a` the category to put the feeds into (created if missing), `r` alone moves them to the default category. Subscribing to an address the user has, or to one that does not answer with a feed, is 400; so is unsubscribing from an unknown feed. Editing an unknown number changes nothing.
- A27. `subscription/quickadd` subscribes to the address in `quickadd` and answers `numResults`, `query`, `streamId`, `streamName`, or `numResults: 0` with `error`.
- A28. A new feed is JSON Feed when its address has `json` as a word, otherwise RSS or Atom. An RSS or Atom feed has to answer with a feed before it is added; when the address answers with a web page, the feed the page announces with `<link rel="alternate">` is added in its place. The entries are fetched before the answer.
- A29. `EntryBeforeDisplay` sees every entry before it is shown and may hold it back; `EntriesRead` and `EntriesFavorite` are told what `edit-tag` changed; `CheckURLBeforeAdd` and `FeedBeforeInsert` see a feed that is being added.

### OPML

- A30. `subscription/export` answers the subscriptions as OPML 2.0 with the `frss:` attributes of FreshRSS: one outline per category, in the order of their `position` and then by name, with the feeds inside by name. Attributes of a feed: `text`, `type`, `xmlUrl`, `htmlUrl`, `description`, `frss:priority`, `frss:unicityCriteria`, `frss:unicityCriteriaForced`, `frss:ttl`, the scraping settings (`frss:xPath…`, `frss:json…`, `frss:xPathToJson`), `frss:filtersActionRead`, `frss:cssFullContent`, `frss:cssFullContentConditions`, `frss:cssContentFilter` and the request settings (`frss:CURLOPT_…`); empty ones are left out.
- A31. `subscription/import` adds the categories and feeds of the posted document and fetches the entries of the new feeds that are not muted. Feeds outside a category go to the default one; a category inside a category stands on its own; the `category` attribute of a top-level feed names its category. A feed the user already has, by address, stays in its category and takes name, site, description, kind and settings from the document, and is unmuted. A document that is not OPML, or of which a feed could not be added, is 400; the feeds that could be added stay.

### Icons

- A32. `iconUrl` is `<base>/favicon/<hash>`, where `<base>` is the configured public address or, without one, the address the request came to. The hash is 16 hexadecimal digits of an HMAC of the place the icon is looked for, keyed with the salt: the same for every feed that looks at the same place. A feed with a custom icon has a hash of its own.
- A33. The icon of a feed is looked for at the image the feed names (`feedIconUrl`), else at its site, else at the root of the host of the feed: the address itself if it is an image, then the first image among the `<link rel="icon">` and `rel="shortcut icon"` of the page, then the same at the root of the site, then `/favicon.ico`. Images up to 1 MiB count.
- A34. A refresh that succeeds looks after the icon: it is fetched when missing and again after 14 days. An icon that was found stays when the site stops answering.
- A35. `GET /favicon/<hash>` answers the stored image with its media type, `Last-Modified` and a cache lifetime of 14 days, and 304 to a conditional request. An unknown hash, or a feed without an icon, gets a built-in icon with a lifetime of 30 minutes. No request to a site is made while answering. An imported custom icon is served as stored.
- A36. Extensions answer under `/api/misc.php/<name>` (or `?ext=<name>`): 400 without a name, 404 for a name no extension registered.
- A37. While `api_enabled` of the system settings is off, every request of the API, `ClientLogin` included, is answered with 503 and `Service Unavailable!`, as in FreshRSS. A category a client names beyond `limits.max_categories` is not created: the feed goes to the default one. `limits.max_feeds` bounds subscriptions (`refresh.md`, F32) and both limits bound an OPML import. Verified by `TestSwitchAndLimits`.

## Invariants and compatibility

- Tokens are compatible with FreshRSS by construction (A2); nothing about a user's session is stored.
- Identifiers of feeds, entries and labels are the imported ones; `feed/<id>` and item identifiers a client remembers keep their meaning.
- The default category is stored under the name FreshRSS shows it by, in the language of the user, see `storage.md`, I11.
- Icons live in the database (`icons`, `custom_icons`), not in files. Their addresses differ from those of FreshRSS; clients read them from `subscription/list`.

## Decisions

Where FreshRSS 1.30.1 does something else, on purpose:

- **A label with spaces around its name is put on the entries.** FreshRSS creates the trimmed label and then looks it up untrimmed, so the entries stay without it.
- **The default category can be renamed** like any other. FreshRSS stores the new name and keeps showing the translated default one; a stream asked by the shown name is then empty for any user whose language is not English.
- **Moving a feed into a category named like a label puts it into the default category** and answers `OK`. FreshRSS answers 500 and moves nothing.
- **`quickadd` answers the name of the feed as text.** FreshRSS answers it HTML-encoded, and for a JSON Feed answers the address, the name not being known before the first refresh.
- **Labels of an item come in the order the labels were created.** FreshRSS leaves the order to the database.
- **`description` of a scraped feed stays empty.** FreshRSS fills it with "RSS feed of <name>" in the language of the user.
- **OPML: `frss:unicityCriteria` with digits is read back**, and **`frss:CURLOPT_FOLLOWLOCATION="false"` is read as false.** FreshRSS writes both and reads neither: its pattern has no digits, and it takes any text for true.
- **`frss:filtersActionRead` carries the filters as they were written**, not in the canonical form FreshRSS prints them in.
- **Content types**: text answers are `text/plain`, JSON answers `application/json`; FreshRSS sends some of both as `text/html`.
- **A page that announces a feed is searched only through `<link>` elements**; SimplePie also guesses from the links of the page.
- **Icons are fetched by the refresh only.** FreshRSS also fetches them while answering a request for one; here an icon that is not known yet gets the built-in one until the next refresh.
- **The hash of an icon is an HMAC-SHA256 cut to 64 bits** instead of CRC32: icons are served without authentication, and the hash is all that keeps one user from asking for the custom icon of another.

Not reproduced from FreshRSS, and why:

- The limits `max_feeds` and `max_categories` and the switch `api_enabled`: freshgo has no such settings.
- Removing saved queries that refer to a deleted feed: saved queries are not interpreted beyond filters yet.

## Verification scenarios

- R2, R7, and A1–A28, A30, A31 as far as the reference installation shows them: `TestReferenceAPI` replays the 292 requests of `testdata/reference/api/cases.json` against freshgo over the imported reference installation, at the root and under `/api/greader.php`, on both engines, and compares status and body with what FreshRSS 1.30.1 answered (`responses.json`). JSON is compared as data, OPML by its outlines. Left out of the comparison: `updated`, `iconUrl`, the order of an item's `categories`, and, for feeds added during the run, identifiers and times of their entries. The departures listed above are not among the cases.
- A7: `TestUsersAreApart`; the last cases of the reference show the second user untouched by the changes of the first.
- A3, A5: `TestAccess`. A13, A20, A24 for a hidden feed: `TestHiddenFeed`. A21: `TestItemFallbacks`, `TestEnclosures`, `TestLongContentIsCut`. A29: `TestHooks`, and `TestAddFeedRefusals` in `internal/refresh`.
- A28: `TestAddFeed`, `TestAddFeedFollowsThePage`, `TestAddFeedOfAnotherKind`, `TestAddFeedRefusals` in `internal/refresh`.
- A30, A31 for the settings the reference feeds do not have: `TestExportAndImportKeepSettings`, `TestImport`, `TestImportRefusesWhatIsNotOPML` in `internal/opml`.
- R13, A32: `TestIconAddresses`, `TestHashes`. A33: `TestIconSearch`. A34: `TestIconIsKept`, `TestRefreshKeepsIcons` in `internal/refresh`. A35: `TestIconOfSite`, `TestCustomIcon`.
- A1, A36: `TestRoutes` and `TestServe` in `cmd/freshgo`.
- The departures: `TestDepartures`.

The tests named `TestHiddenFeed`, `TestItemFallbacks`, `TestEnclosures` and the OPML tests expect what reading the code of FreshRSS says; the reference installation has no hidden feed, no entry without a title and only one kind of enclosure.
