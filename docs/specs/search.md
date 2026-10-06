# Search language and filter actions

Status: implemented for reading a query, matching an entry in memory, which is what filter actions need, and telling a database which entries cannot match, which is what searching the stored entries needs (`storage.md`, S33).
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `FreshRSS_BooleanSearch`, `FreshRSS_Search`, `FreshRSS_Entry::matches`, `FreshRSS_FilterAction`, `FreshRSS_FilterActionsTrait`, `FreshRSS_DatabaseDAO::strilike` and `lib/lib_date.php` of FreshRSS at commit `219eaf58`.

## Purpose and scope

`internal/search` reads the search language of FreshRSS and tells whether an entry matches a query. Filter actions, the rules that mark arriving entries read, star them or label them, are written in it; `internal/refresh` applies them. The search of the web interface is the same matching run over the stored entries. Covers requirement R11 of the plan of the core and, with `storage.md`, R6 of the plan of the web interface.

## Requirements

- Q1. A query is read into the same structure FreshRSS builds from it: parenthesized parts joined by AND, OR, AND NOT and OR NOT, alternatives separated by `OR`, and in each alternative the operators `e:`, `f:`, `c:`, `L:`, `labels:`, `intitle:`, `intext:`, `author:`, `inurl:`, `#tag`, `date:`, `pubdate:`, `mdate:`, `userdate:`, their negations with `-` or `!`, quoted strings, regular expressions `/…/` with the flags `i` and `m`, and free words. The parser is a port of the one of FreshRSS and keeps its results for malformed input too: unbalanced parentheses and quotes, stray operators, empty values.
- Q2. `search:name` and `S:1,2` are replaced by the saved searches of the user they name, by name or by position in the list, before anything else is read. A reference that names nothing is dropped.
- Q3. A query longer than 16384 bytes or with parentheses nested deeper than 32 is refused (`ErrTooLong`, `ErrTooDeep`), the limits of FreshRSS.
- Q4. Dates are ISO 8601 intervals as `parseDateInterval` reads them: a date or part of one (`2014`, `2014-03`, `2014-03-15T12`), two of them (`2014-02/2014-04`, `2014-02/04`), an open end (`2014-03/`, `/2014-03`), and durations (`P1W`, `2014-03/P1W`, `P1W/2014-05-25T23:59:59`), counted from the current time. A date without a zone is in the user's time zone.
- Q5. An entry matches a query as `FreshRSS_Entry::matches` decides. An alternative matches when all its demands hold; alternatives are tried in turn, parenthesized parts are combined left to right by their operators. A query without demands matches every entry.
- Q6. Plain strings are looked for without regard to case, for letters of every alphabet; accents count. `intitle:` looks in the title, `author:` in the authors, `intext:` in the content, `inurl:` in the link, a free word in the title and the content. `#tag` asks for a tag of the feed's that equals the word; `+` in it stands for a space. The content is HTML: a string is looked for in its HTML-encoded form (`<` as `&lt;`).
- Q7. `date:` compares with the time the entry was added, taken from its identifier; `pubdate:` with the date of the entry; `mdate:` with the time the feed last changed it; `userdate:` with the time the user last changed it. `e:`, `f:` and `c:` compare identifiers of the entry, its feed and its category.
- Q8. `L:` and `labels:` are read and, for a filter, do not count in matching, as in FreshRSS: filters run before an entry has labels.
- Q15. A query read with `Options.Labels` asks for labels the way FreshRSS searches its database (`sqlBooleanSearch`): `L:1,2` wants a label with one of the identifiers, `L:*` any label, `labels:a,b` a label with one of the names, compared exactly; every such operator of an alternative has to hold, so `L:1 L:2` wants both labels. Negated, the entry must have none of the labels of the list, and `-L:*` no label at all.
- Q16. `Query.Condition` gives a test on what a database keeps apart from the texts of an entry — identifiers of the entry, its feed and its category, labels, the four times — that every entry the query matches passes. It is as narrow as the query allows: an alternative contributes its operators `e:`, `f:`, `c:`, `L:`, `labels:` and dates and nothing for its texts; parts are joined by their operators. A negated part counts only when it has no texts, since then the test is exact; a negated part with texts, which the test would only over-approximate, is left out (`AND NOT`) or makes the test pass everything (`OR NOT`). A query whose test would have more than 500 conditions gets one that passes everything: SQLite refuses an expression nested deeper than 1000, and `Match` decides anyway. `Query.UsesLabels` tells whether matching needs the labels of the entries.
- Q9. A rule is an entry of a `filters` list: `{"search": …, "actions": […]}`. `ParseRules` returns the rules it could read and a problem for each it could not; entries of another shape are passed over.
- Q10. A regular expression Go's `regexp` does not accept, such as one with a backreference or a lookahead, makes the whole query unusable (`ErrRegexp`). The refresh reports the rule in its log and goes on without it; the import lists such rules as warnings.

Filter actions in the refresh (`internal/refresh`, see `refresh.md`):

- Q11. For a new entry, after `EntryBeforeInsert` and the auto-read rules, the rules of the user (`filters` of the settings), then of the category, then of the feed are tried in the order they are stored. A matching rule with `read` marks an unread entry read and calls `EntryAutoRead` with the reason `filter`; with `star` it stars the entry.
- Q12. A changed entry goes through the same rules, but is never starred by them.
- Q13. After `EntryBeforeAdd`, a new entry gets every label one of whose rules with the action `label` matches it. A changed entry gets none.
- Q14. An entry that is not stored yet is matched under the identifier it would get at the time of the refresh.

## Decisions

- **The parser is ported expression by expression**, not redesigned: what a filter of an existing installation means is defined by what FreshRSS made of it. The lookbehind assertions of the PCRE expressions are written out as an explicit character before the match.
- **Case is ignored the way PostgreSQL's `ILIKE` does it, on both engines.** FreshRSS matches filters the way its database compares strings: ASCII letters only on SQLite, every letter on PostgreSQL, and without accents on MySQL. freshgo behaves the same on SQLite and PostgreSQL (storage S10), and a rule such as `intitle:реклама` has to find «Реклама».
- **Regular expressions are Go's `regexp` (RE2)**: matching time is linear in the text, which matters for expressions run over every arriving entry. Expressions it lacks are refused rather than approximated.
- **Title, authors, tags and link are matched as plain text.** FreshRSS matches them in their HTML-encoded stored form.
- **A search of the database is not a second implementation of the language.** FreshRSS turns the whole query into SQL (`sqlBooleanSearch`), per engine. freshgo hands the database only the test of Q16 and lets `Match` decide over the rows that pass: filters and searches then agree by construction, on both engines, on case, on regular expressions and on every quirk of the parser. The price is reading the texts of the entries the test lets through; `storage.md` has the measurements.
- **A query is not written back.** FreshRSS can print a parsed query in a canonical form (`__toString`); freshgo keeps rules as they are stored, and nothing in this step needs the rewriting.

## Known differences from FreshRSS

- On SQLite FreshRSS ignores case for ASCII letters only: `CAFÉ` does not find «Café» there and does here.
- Looking in the HTML-encoded title, FreshRSS finds `amp` in «AT&T» and does not find `"quoted"` in a title with quotation marks. freshgo does the opposite in both cases.
- A regular expression PHP accepts and Go does not disables its rule; in FreshRSS it works. One that neither accepts matches nothing in FreshRSS and disables the rule here.
- A regular expression is matched as UTF-8 text; PHP matches bytes unless the expression says otherwise.
- `-date:2014-03`, like the other negated intervals with both ends, matches nothing: FreshRSS asks an entry to be before the start and after the end at once. freshgo keeps this.
- `P1M/2014-04`, a duration before a partial date, is read by PHP's `strtotime` as a time of the current day; freshgo takes the date as unreadable and counts the duration from the current time.

## Verification scenarios

- Q1–Q8: `TestAgainstFreshRSS` reads the 457 queries of `testdata/reference/oracle/search-cases.json`, those of `tests/app/Models/SearchTest.php` and `BooleanSearchTest.php` of FreshRSS and more for dates, negations, parentheses, saved searches and malformed input, and compares the structure and the matching entries, of seven, with what FreshRSS 1.30.1 gave (`search.json`). The differences above are listed in the test and checked to stay differences.
- Q3: `TestLimits`. Q9, Q10: `TestParseRules`.
- Q8, Q15: `TestMatchLabels`. Q16: `TestCondition` for the shape of the test; that it never rules out an entry the query matches is checked against the database by `TestSearchListsWhatMatches` in `internal/store`.
- R11, Q11–Q13: `TestFilterActions` in `internal/refresh`: rules of the user, the category, the feed and a label, a saved search, an unusable rule reported and skipped, changed entries. Q14 and the user's time zone: `TestFilterByDate`.
- Q10 in the import: `TestImportWarnings` in `internal/importer`.
