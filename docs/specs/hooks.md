# Extension points

Status: implemented.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `lib/Minz/HookType.php` and `Minz_ExtensionManager::callHook` of FreshRSS at commit `219eaf58`.

## Purpose and scope

`internal/hooks` is the registry of extension points modelled on `Minz_HookType`: which hooks exist, what a handler gets and returns, and in what order handlers run. Covers plan requirement R8.

Out of scope: a runtime for extensions. Handlers are Go functions compiled into the binary and registered in `newHooks` of `cmd/freshgo`; freshgo ships none. Hooks of the web interface (menus, navigation, `js_vars`, view modes, favicon buttons) belong to the plan of the web interface.

## Requirements

- H1. A handler is registered with a priority. Handlers of one hook run by ascending priority value, and in registration order within one value, as in FreshRSS.
- H2. A hook without handlers changes nothing: the caller gets its value back and goes on.
- H3. Chain hooks (`OneToOne` in FreshRSS): each handler gets what the previous one returned. A handler that returns `false` drops the value; the remaining handlers are not called and the caller abandons the operation for that value. FreshRSS expresses the same with a `null` result.
- H4. Event hooks (`PassArguments`): handlers are called with the argument in turn and may change what it points to. A handler that returns `true` ends the call. In FreshRSS the first result other than `null` does.
- H5. Signal hooks (`NoneToNone`): every handler is called.
- H6. Entries and feeds that pass through hooks are `store.Entry` and `store.Feed`; both survive a round trip through JSON unchanged, so that an out-of-process runtime can be put behind a handler later.
- H7. Handlers are registered before the first call and are then called from several goroutines at once: the refresh handles feeds of one user in parallel. A handler guards the state it shares.

## Hooks

| Hook | Kind | Argument | Called |
|---|---|---|---|
| `CheckURLBeforeAdd` | chain | feed address | when a feed is added by its address, before the address is checked and requested; not for the feeds of an OPML import |
| `FeedBeforeInsert` | chain | feed | before a feed is stored, also for every feed of an OPML import, one the user already has included |
| `FeedsListBeforeActualize` | chain | feeds of a user, longest without an attempt first | once per user and run |
| `FeedBeforeActualize` | chain | feed | for every feed of the list, due or not; a dropped feed is skipped, changes apply to this refresh only |
| `FetchBefore` | event | feed and request | before the feed is requested |
| `ParseAfter` | event | feed and parsed document, or the error text | after identifiers are assigned; also when the fetch or the parsing failed; not for a 304 |
| `EntryBeforeInsert` | chain | entry | for every new and every changed entry |
| `EntryBeforeAdd` | chain | entry | for a new entry, after `EntryBeforeInsert` |
| `EntryBeforeUpdate` | chain | entry | for a changed entry, after `EntryBeforeInsert` |
| `EntryBeforeDisplay` | chain | entry | for every entry the API is about to show; a dropped entry is left out of the answer |
| `EntryAutoRead` | event | entry and reason | when a rule marks an arriving entry read, once per rule that applies: `upon_reception`, `same_title_in_feed` (also for a title repeated in the category), `same_guid_in_category`, and `filter` for a filter action |
| `EntryAutoUnread` | event | entry and reason | when a changed entry is made unread, reason `updated_article` |
| `EntriesRead`, `EntriesFavorite` | event | user, entry ids, new state | after `edit-tag` of the API has changed the state: `EntriesRead` with all the identifiers given when at least one entry changed, `EntriesFavorite` always. Not for `mark-all-as-read`, as in FreshRSS |
| `Init` | signal | none | when a command that refreshes or serves starts |
| `UserMaintenance` | signal | user | before the feeds of a user are refreshed |

## Decisions

- **`FetchBefore` and `ParseAfter` replace `simplepie_before_init` and `simplepie_after_init`**: there is no SimplePie object to hand out. `FetchBefore` gets the request and may change its headers and parameters; `ParseAfter` gets the items with identifiers assigned and may change their exported fields.
- **A dropped entry is offered again on the next refresh**, since nothing records it. FreshRSS behaves the same.
- **The `NoneToString` signature is not implemented.** All its hooks belong to the web interface; the type arrives with the first of them.
- **`api_misc` is `Registry.HandleAPI`**: an extension registers an `http.Handler` under its name and gets the requests to `/api/misc.php/<name>`. FreshRSS calls the first handler of the one extension it has enabled for the request, which comes to the same.
- **Hooks run before the transaction** that stores a feed, so a slow handler does not hold a database lock. What the user changes in an entry while handlers run is kept, see `refresh.md`, F9.

## Verification scenarios

- H1, H3: three handlers added out of priority order → called by priority, each sees the previous result; a dropping handler stops the chain. `TestChainPassesResultOnInPriorityOrder`, `TestChainDropStopsTheRest`.
- H2: `TestEmptyRegistryChangesNothing`; every other test of `internal/refresh` runs with an empty registry.
- `EntryAutoRead`: `TestReadUponReception`, `TestReadWhenSameTitleInFeed`, `TestReadWhenSameInCategory` in `internal/refresh`.
- H4: `TestEventStopsAtTheHandlerThatDealtWithIt`, `TestEventHandlerMayChangeTheEntry`. H5: `TestSignalCallsEveryHandler`. H6: `TestEntitiesSurviveJSON`.
- `EntryBeforeDisplay`, `EntriesRead`, `EntriesFavorite`: `TestHooks` in `internal/greader`. `CheckURLBeforeAdd`, `FeedBeforeInsert`: `TestAddFeedRefusals` in `internal/refresh`, `TestImport` in `internal/opml`. Extension endpoints: `TestRoutes` in `cmd/freshgo`.
- In the pipeline (`internal/refresh`): an entry dropped by `EntryBeforeInsert` or `EntryBeforeAdd` is not stored; a title changed by one handler reaches the next and the database; a feed dropped by `FeedBeforeActualize` is not requested; a header set in `FetchBefore` reaches the server; tags added in `ParseAfter` are stored; a changed entry passes `EntryBeforeUpdate`. `TestHooksInThePipeline`. `EntryAutoUnread`: `TestChangedEntryBecomesUnreadWhenAsked`. `UserMaintenance` and `FeedsListBeforeActualize`: `TestWhichFeedsAreRefreshed`. `ParseAfter` on failures: `TestFailedFeed`.
