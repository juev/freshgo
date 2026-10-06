# Entry GUID and feed parsing

Status: implemented for RSS 0.9x/2.0, RSS 1.0 (RDF) and Atom 0.3/1.0 in `internal/feed` and `internal/sanitize`, and for the scraped feed kinds in `internal/scrape`, which produces an RSS document that goes through the same code.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; SimplePie as shipped with FreshRSS at commit `219eaf58` (`Item.php`, `SimplePie.php`, `Sanitize.php`, `Parser.php`, `Parse/Date.php`), `FreshRSS_Feed::decideEntryGuid`, `loadGuids`, `loadEntries`, `loadHtmlXpath`, `loadJson` and `FreshRSS_dotNotation_Util`; the behaviour of FreshRSS 1.30.1 recorded in `testdata/reference/oracle`.

## Purpose and scope

How a feed document becomes items, and how the `guid` of an item is derived so that it equals the value FreshRSS stored for the same item. This is what plan requirement R3 rests on: a refresh after import creates no duplicates.

Out of scope: fetching (`internal/fetch`), storing and change detection (plan step 7), full-text retrieval (step 10).

## Requirements

- G1. The GUID of an item is the first non-empty of: `atom:id` (Atom 1.0, then 0.3), RSS `guid`, `dc:identifier` (1.1, then 1.0), `rdf:about` of the item. An Atom element inside an RSS item counts.
- G2. The identifier is transformed in this order: surrounding whitespace removed; `&`, `<`, `>` and `"` HTML-encoded (the single quote is not); `http://` replaced with `https://` when the host is on the force-https list; bytes below 32 and above 127 removed; whitespace that this exposed at the ends removed; cut to 767 bytes. An identifier that comes out as `0` counts as absent, as PHP takes it.
- G3. The force-https list is the 22 domains FreshRSS ships plus the `force-https.txt` of the installation, which import carries over (`settings`). A listed domain covers its subdomains. Matching is case-sensitive and looks at the text after `http://` up to the first `/`, `?` or `#`, without credentials and port. A port number above 65535 makes the URL one PHP cannot parse: it is left alone.
- G4. An item without an identifier gets `sha1(link . date)`; without a link, `sha1(link . date . title)` if it has a title, else `sha1(link . date . title . content)`. The parts are in the form of G6. An item with nothing at all has an empty GUID.
- G5. `feed.attributes.unicityCriteria` selects another key: `link`, or `sha1:` over the parts named in it (`link`, `published`, `title`, `content`); legacy `hasBadGuids` means `link`. A criteria that yields nothing for an item falls back to G1 and G4.
- G6. The parts are what SimplePie hands to FreshRSS: the link absolute, normalized, with the force-https replacement applied and HTML-encoded; the date as a decimal Unix time, empty when the feed has none or it cannot be read; the title and the content sanitized.
- G7. When more than `round(0.05 × items)` items of a document have an empty or a repeated GUID and the criteria are not forced (`unicityCriteriaForced`), the criteria degrade: the identifier or `link` to `sha1:link_published`, that to `sha1:link_published_title`; other criteria stay. The new criteria are stored with the feed. Bad GUIDs that remain mark the feed as failing.
- G8. Links are normalized as the SimplePie IRI class does: scheme and host in lower case, no default port, dot segments removed, escapes of unreserved characters decoded and the rest in upper case, characters that may not appear in a URL escaped. The link of an item is its first alternate link: Atom `link` without `rel` or with `rel="alternate"`, RSS `link`, RSS `guid` unless `isPermaLink` is `false`; then the link of its first enclosure. Relative links resolve against `xml:base`, then the feed's link, then its `rel="self"` link, then the address the feed was fetched from.
- G9. The date is the first of `atom:published`, `pubDate`, `dc:date`, `atom:updated`, Atom 0.3 `issued`, `created`, `modified`. Formats: W3C/ISO 8601 with a zone, RFC 2822 with two-digit years, comments and named zones, RFC 850, asctime, month names in the languages SimplePie knows — these are SimplePie's own patterns and read a date without a zone as UTC. Anything else SimplePie leaves to PHP `strtotime`: a date, optionally a time, optionally a zone, in the common textual and numeric layouts; without a zone such a date is in the user's time zone (`Options.Location`). A date that reads as the Unix epoch is no date.
- G10. The text of an element is treated by its kind: Atom `type` (`text`, `html`, `xhtml`), HTML for RSS `description` and `content:encoded`, and for RSS titles "HTML if it contains an entity or a closing tag". The content is `atom:content`, `content:encoded`, else the summary or description. Relative URLs in it resolve against `xml:base`, then the item's link.
- G11. HTML is reduced to the allowlist of elements and attributes FreshRSS configures (`FreshRSS_SimplePieCustom`): unknown elements are replaced by their content, `script`, `style`, `svg` and `template` are dropped with it, `id` and `class` become `data-sanitized-id` and `data-sanitized-class`, comments go, `javascript:` URLs get an `unsafe:` prefix, `audio`, `video` and `iframe` get fixed attributes.
- G12. Authors are the item's (`atom:author`, RSS `author`, `dc:creator`, `itunes:author`), else those of its `atom:source`, else the feed's; an author is known by name, or by e-mail without one. Tags are the category labels, split on commas, without repeats.
- G13. Enclosures come from `media:group/media:content`, `media:content`, Atom `link rel="enclosure"` and RSS `enclosure`, one representation per `media:group`; only http(s) URLs are kept. The thumbnail is the item's first `media:thumbnail`. `Item.Attributes` gives both in the JSON shape of `entry.attributes`, values HTML-encoded as FreshRSS stores them.
- G14. Items are returned from the end of the document to its beginning, the order in which FreshRSS stores them.
- G15. The character encoding is taken from the byte order mark, then the HTTP `Content-Type`, then the XML declaration; a document that declares nothing and is not UTF-8 is read as windows-1252.

## Scraped feeds

`scrape.RSS` turns the document of a feed of kind 10 (HTML + XPath), 15 (XML + XPath), 25 (JSON Feed), 30 (JSON + dot notation) or 35 (HTML + XPath + JSON) into RSS 2.0 by the feed's settings (`feed.attributes.xpath`, `json_dotnotation`, `xPathToJson`); `feed.Parse` does the rest.

- X1. XPath kinds: `item` selects the items; `itemTitle`, `itemUri`, `itemAuthor`, `itemTimestamp`, `itemThumbnail`, `itemUid` are evaluated at each item as `normalize-space(<expression>)`. `itemContent` that selects nodes gives their markup, one node per line; a string result is taken as it is, `true` gives `1`, a number nothing. `itemCategories` gives one tag per selected node, or one tag for a string result. The base of every link and of relative URLs in the content is the href of the page's `base` element; an HTML page without one has its own address for a base. `noscript` is read as markup, not as text.
- X2. JSON kinds: a path is keys joined with dots, `list[0]` for `list.0`; `''`, `.` and `$` are the document; a quoted string is a literal; `&` joins values. A key that exists as written wins over reading it as a path. Values are turned into text as PHP does: `true` is `1`, `false` and `null` are empty, a float has 14 significant digits. The items may be a list or the members of an object, in document order. An item without a link is skipped.
- X3. JSON Feed uses a fixed set of paths. `content_html` is the content even when absent: `content_text` alone gives an empty content. Authors and attachments are not read — as in FreshRSS, where the code that collects attachments has no effect.
- X4. For kind 35 `xPathToJson` finds the JSON: a string result is the JSON text; selected nodes give a list of what each holds, those that are not JSON objects or lists left out.
- X5. An item without `itemUid` (or with one that PHP takes for empty: `0`) gets `urn:sha1:` + `sha1(title . content . link)` over the scraped values. An item with no title, no content and no link is dropped.
- X6. The date is read by `itemTimeFormat`, a PHP date format, when it fits, else the way PHP `strtotime` would (G9). With the format `U` a value longer than 10 characters loses its last 3: milliseconds. A date of 0 or 1 is no date.
- X7. An author string is split on semicolons when it has one or has a character that HTML-encoding turns into an entity, else on commas.
- X8. The thumbnail is also listed as an enclosure of medium `image`.
- X10. The items of one page may not add up to more than 16 times its size plus 1 MiB: item expressions that select nested elements with themselves for content would otherwise multiply the page.
- X9. No items found, or settings without the item path, is an error, as it is a failed refresh in FreshRSS. An XPath expression the library cannot compile or evaluate, including one that makes it panic, is an error too.

## Invariants and compatibility

- `guid` stays in the FreshRSS form: an opaque ASCII key, HTML-encoded, never decoded or shown (`storage.md`).
- Criteria that hash the content (`sha1:*content*`) cannot be relied on to match FreshRSS for every item: the hash is taken over sanitized HTML, and the two sanitizers do not agree on every input (see below). Import warns about such feeds.
- `Item.Title` is empty for an item without a title. FreshRSS shows the GUID instead; whether to store it that way is decided where entries are written (plan step 7).

## Decisions

- **Own reader instead of gofeed** (user decision of 2026-10-06). gofeed drops the `type` of Atom `title` and `summary`, without which text cannot be told from HTML, and the order in which SimplePie looks through namespaces has to be rebuilt on top of it anyway. The document is read by `encoding/xml` into a tree of elements, and each value is taken from the tree by SimplePie's rules.
- **HTML is parsed by `golang.org/x/net/html`**, an HTML5 parser, where FreshRSS uses libxml. Known differences in the output: table rows get a `tbody`; `track` and `wbr` are void elements; content after a stray `</div>` is kept.
- **Atom content of a type that is neither text nor markup is sanitized as HTML.** For `type="text/html"`, `application/xml`, `image/svg+xml` or a base64-encoded media type SimplePie returns the content untouched, and FreshRSS stores it with its scripts. Treated as an upstream security bug. A GUID criteria that hashes the content differs for such items.
- **PHP `strtotime` is approximated, not reproduced.** The layouts cover every date shape in `oracle/feeds/dates.xml`; a shape outside them reads as "no date" here and may be a date for FreshRSS, which matters only for items keyed by link and date.
- **XPath is evaluated by `github.com/antchfx/xpath` over an HTML5 tree**, where FreshRSS uses libxml. Expressions that depend on how broken HTML is repaired can select differently: the HTML5 parser puts table rows into a `tbody`, so `//table/tr` matches nothing while `//table//tr` works on both. The library lacks `id()`, `lang()` and the namespace axis. Hypothesis, not checked: XML documents with a default namespace are matched by local name here and not at all by libxml.
- **The `urn:sha1:` key of a scraped item is hashed over markup written by freshgo**, which follows libxml in what the oracle covers: void elements, quotes in text and in attribute values, comments, XML elements. Where the two still serialize differently (the reviewer's examples: a newline at the start of `pre`, `\r\n`, attribute names in mixed case such as `viewBox`, CDATA sections and processing instructions in XML), an item without `itemUid` whose content is markup gets another key and appears once more after import.
- **A date format that names no time gives midnight.** PHP fills the fields a format does not name from the current time unless it has `!` or `|`, so a date-only format gives the time of the refresh. A date that does not exist (month 13) is no date here; PHP rolls it over. Formats with zone names, the day of the year or trailing data (`T`, `e`, `z`, `+`) are not applied, and the value is read without the format.
- **The text of a removed element is not encoded twice.** FreshRSS turns `<font>a & b</font>` into `a &amp;amp; b`; freshgo keeps `a &amp; b`. Treated as an upstream bug.
- **More tolerant of broken documents than FreshRSS**: text before the first tag and control characters are dropped. FreshRSS rejects such a feed.
- **Dates relative to now** (`yesterday`), which PHP `strtotime` accepts, are not dates here.
- **Punycode conversion of enclosure hosts is not done**; a non-ASCII host is percent-encoded like the rest of a URL, which is what FreshRSS does for item links (`links.xml`). For enclosures the difference is a hypothesis, not checked.

## Verification scenarios

- G1–G15 as a whole: each of 16 feed documents is parsed by freshgo and compared with what FreshRSS 1.30.1 makes of it — feed title and links, criteria after degradation, and for every item the GUID, title, link, date, authors, tags, content and attributes. The documents are the corpus and `testdata/reference/oracle/feeds` (RSS 1.0, Atom 0.3, Atom construct types and `xml:base`, Media RSS, mixed namespaces, repeated GUIDs, windows-1251, undeclared entities, and 54 dates, 53 identifiers and 63 links in `dates.xml`, `ids.xml`, `links.xml`); the expected values are generated by `testdata/reference/oracle/generate.sh`. `TestParseAgainstFreshRSS`.
- G1–G3, G8, G10 with a feed address: the RSS and Atom feeds of both users of the reference installation → every stored entry is found by the computed GUID, with the stored title, link, date and content. `TestParseAgainstReferenceDatabase`.
- G2: `TestSafeASCII`; a 1000-byte identifier → 767 bytes, `TestGUIDFallback`.
- G3: listed domain, subdomain, port, credentials, another case, a look-alike domain, comments in the list, nested entries. `TestHTTPSDomains`, `TestHTTPSDomainsNesting`.
- G4, G5: every criteria value and every fallback. `TestGUIDCriteria`, `TestGUIDFallback`.
- G7: one repeat in twenty tolerated, two degrade, forced criteria stay, two steps of degradation, criteria that never change. `TestAssignGUIDsDegrades`; `dup-guids.xml`, `dup-links.xml`, `dup-tolerated.xml` in the oracle.
- G8: relative links with and without a feed link. `TestParseResolvesAgainstFeedURL`.
- G9: 37 date strings with the values SimplePie returns for them. `TestParseDate`.
- G11: 46 fragments compared with the FreshRSS sanitizer, the differences listed with their reason; script-carrying inputs. `TestHTMLAgainstFreshRSS`, `TestHTMLIsSafe`, `TestHTMLKeepsAttributeValuesInert`, `TestXHTML`, `TestAbsolutize`, `TestAllowedScheme` in `internal/sanitize`.
- G9, time zone: `TestParseDatesInLocation`.
- X1–X9: 28 pages-and-settings cases scraped by freshgo and compared with what FreshRSS 1.30.1 stores for them (`oracle/scrape-cases.json`, `oracle/pages`, `scrape.json`): typed XPath results, attribute nodes, `base` and its absence, `noscript`, markup content hashed into the key, missing fields, dot-notation literals and concatenation, items as object members, number formatting, JSON Feed dates, embedded JSON as nodes and as a string. `TestRSSAgainstFreshRSS` in `internal/scrape`.
- X1–X8 after a real refresh: the feeds of kinds 10, 15, 25, 30 and 35 of both users of the reference installation → every stored entry is reproduced. `TestRSSAgainstReferenceDatabase`.
- X2: `TestLookup`, `TestDecodeJSONKeepsOrder`. X6: 33 format-and-value pairs with the values PHP returns, `TestParsePHPDate`; `TestTimestamp`. X7: `TestSplitAuthors`. X10: `TestRSSBoundsNestedItems`. X9: `TestRSSErrors`; 45 odd expressions in every setting, `TestXPathNeverPanics`.
- G15: `TestParseEncodings`. Tolerance: `TestParseIsLenient`. Not a feed: `TestParseRejectsOtherDocuments`.
- Content of any Atom type is sanitized: `TestParseSanitizesContentOfAnyType`. Parsing time is linear in the size of the document: `TestParseLargeDocuments`.
