# Full text of articles

Status: implemented.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `FreshRSS_Entry::loadCompleteContent`, `getContentByParsing`, `originalContent` and `FreshRSS_http_Util::httpGet` of FreshRSS at commit `219eaf58`.

## Purpose and scope

For a feed that carries summaries, freshgo takes the text of each article from its web page: the elements a CSS selector picks. `internal/fulltext` reads a page; `internal/refresh` decides for which entries and where the text goes. Covers plan requirement R12.

## Requirements

- T1. A feed with a non-blank `pathEntries` (a CSS selector, or several separated by commas) has the pages of its entries read. The page of an entry is the one at its link, requested with the feed's own request settings (credentials, proxy, cookies, headers, user agent, time limit) and under the limits and the address rules of every request of freshgo.
- T2. Pages are read for new entries and for entries the feed has changed, never for an entry that is stored and unchanged. An entry without a link is left as it is.
- T3. With `path_entries_conditions`, a list of queries in the search language, a page is read only for an entry that matches at least one of them. Blank conditions do not count. A condition that cannot be used (`search.md`, Q10) is reported in the log and matches nothing.
- T4. A page that sends on with `<meta http-equiv="refresh">` is followed, up to four times in a row; the target is resolved against the address of the page.
- T5. The text is the HTML of every element the selector matches, in document order, an element inside another matched one included a second time. It is cleaned by the sanitizer of feed content; relative addresses are resolved against the `<base href>` of the page, else against the address the page finally came from.
- T6. With `path_entries_filter`, a CSS selector, a picked element that matches it is left out, and so is every matching element inside the others, both before the cleaning and after it, so that the filter may name what the page has and what the cleaned markup has (`data-sanitized-class`).
- T7. The text is stored between the markers `<!-- FULLCONTENT start //-->` and `<!-- FULLCONTENT end //-->`. By `content_action` of the feed it replaces the text of the feed (`replace`, also when the attribute is absent), which then goes to `original_content` in the attributes of the entry, or stands before it (`prepend`) or after it (`append`).
- T8. Whatever goes wrong, an unusable selector, a failed request, an empty page, no element matched, leaves the text of the feed, is reported in the log and does not fail the refresh of the feed.
- T9. A feed without `pathEntries` and with `path_entries_filter` has the matching elements cut out of the text the feed gives. When something was cut, the text as it came goes to `original_content`.
- T10. The change hash of an entry covers the text the feed gave, not the text of the page: an entry is not taken as changed because its stored text is the page's.

## Decisions

- **CSS selectors are applied by `goquery`** (`cascadia`), not translated to XPath as FreshRSS does with `phpgt/cssxpath`. The result is compared with FreshRSS for the kinds of selectors listed under verification.
- **Pages are read before anything else looks at the entry**: hooks, auto-read rules and filter actions see the completed text, for new and for changed entries alike. FreshRSS does so for new entries.
- **Pages are not cached.** FreshRSS keeps a fetched page on disk for the cache duration; here a page is read at most once per new or changed entry.
- **A relative `<base href>` is resolved against the address of the page.**
- **The request settings of the feed go with the request for the page whatever host the page is on**, as in FreshRSS: credentials, cookies and headers, and the POST body of a feed that is fetched with POST. Cookies for a site whose feed lives on another host are what the setting is used for; the other side of it is that a feed decides, by its links, where the credentials set for it are sent.

## Known differences from FreshRSS

- When the filter removes something from the cleaned markup, FreshRSS 1.30.1 returns the content inside a `<body>` element; freshgo returns the content alone. Seen in the reference (`filter by attribute`).
- For a changed entry FreshRSS takes the text it has stored, the previous full text, as the text of the feed: with `replace` it lands in `original_content`, with `append` the new text is added to the old. freshgo uses what the feed gives now. Read from the code, not run.
- The four refreshes of T4 are not reduced by HTTP redirects on the way, which FreshRSS counts together. Read from the code, not run.

## Verification scenarios

- T5, T6, T4: `TestAgainstFreshRSS` in `internal/fulltext` reads the pages of `testdata/reference/oracle/articles/` with the 29 cases of `fulltext-cases.json` and expects what `getContentByParsing` of FreshRSS 1.30.1 returned (`fulltext.json`): descendant, child and sibling combinators, classes, identifiers, attribute selectors (`=`, `~=`, `|=`, `^=`, `$=`, `*=`, presence), `:first-child`, `:last-child`, `:nth-child`, `:nth-of-type`, `:not`, `*`, lists of selectors, nested matches, a filter, a `<base>` element, a meta refresh.
- R12, T1, T2, T7, T10: `TestFullText` in `internal/refresh`, for each `content_action` and with a filter: the stored content, `original_content`, no request and no change on a refresh of the unchanged feed, a new request when the entry changes.
- T3, T4, T8, T2: `TestFullTextConditionsAndFailures`: conditions, among them one that cannot be used, alone and next to a usable one; a page that is gone, one without the element, one that sends on, an entry without a link.
- T9: `TestContentFilterWithoutFullText`.
