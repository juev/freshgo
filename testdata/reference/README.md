# Reference data

What a real FreshRSS produces from a fixed set of feeds. Tests compare freshgo against it instead of against our reading of the FreshRSS code.

| Path | Content |
|---|---|
| `corpus/` | The feeds, one per feed kind. Served as `http://feeds.freshgo.test/<file>`. |
| `alice.opml.xml`, `bob.opml.xml` | Subscriptions of the two users, with the scraping settings. |
| `scenario/` | Go program that marks entries read, starred and labelled through the Google Reader API. |
| `set-custom-icon.php`, `custom-icon.png` | Give feed 2 of alice a custom icon. |
| `sqlite/data/` | Data directory of the FreshRSS that ran on SQLite. |
| `pgsql/data/`, `pgsql/dump.sql` | Data directory and database dump of the FreshRSS that ran on PostgreSQL. |
| `oracle/` | What FreshRSS code makes of single inputs, without an installation: see below. |

`sqlite/` and `pgsql/` are generated and committed, so tests need no Docker to read them.

## Regenerating

```sh
testdata/reference/generate.sh          # both engines
testdata/reference/generate.sh sqlite   # one of them
```

Needs Docker and Go. The script starts `freshrss/freshrss:1.30.1`, a web server with the corpus and, for `pgsql`, PostgreSQL; installs FreshRSS, creates alice and bob, imports their OPML, refreshes the feeds, runs the scenario and copies the result out.

Entry identifiers, salts and password hashes change on every run. Tests therefore read expected values from the generated files or find entries by title; after regenerating, only counts written into tests (`internal/importer/importer_test.go`, `cmd/freshgo/main_test.go`) need a look, and only if the corpus or the scenario changed.

## The oracle

`oracle/generate.sh` runs PHP from the same image over inputs kept next to it and writes the results down:

- `feed.php` parses the RSS and Atom files of the corpus and of `oracle/feeds/` the way a refresh does → `feeds.json`: feed title and links, the uniqueness criteria after degradation, and every entry as it would be stored.
- `sanitize.php` runs the HTML sanitizer over `sanitize-cases.json` → `sanitize.json`.
- `scrape.php` scrapes the pages of `oracle/pages/` with the settings of `scrape-cases.json`, fetching them from a web server inside the container as a refresh does → `scrape.json`.
- `purge.php` fills the database of a throwaway installation with the feeds of `purge-cases.json`, `cli/purge.php` cleans it up → `purge.json`: how many entries of every group are left. A group is entries alike in everything the cleanup looks at; `age` is how long ago, in seconds, the feed last listed them.
- `search.php` reads the queries of `search-cases.json` as filters are read and matches them against the entries listed there → `search.json`: the structure FreshRSS built from each query and the entries it matches. Bounds of dates relative to the current time are left out (`volatile`).
- `force-https.txt` is the installation's own force-https list for these runs.

A new case is a file in `oracle/feeds/` or an entry in `sanitize-cases.json`, `scrape-cases.json`, `purge-cases.json` or `search-cases.json`, then `oracle/generate.sh`. Unlike the installations above, the output is stable from run to run, so `git diff` after regenerating shows exactly what the new case added.

The documents are parsed without an address, so relative links stay unresolved against the feed URL here; the installations cover that.

## What the corpus covers

- `atom.xml`: `atom:id` identifiers, including one with `&` and non-ASCII characters, one on a force-https domain and one with surrounding spaces; several authors; HTML, XHTML and text content.
- `rss.xml`: `guid` with and without `isPermaLink`, markup characters in a guid, a subdomain of a force-https domain, a domain from the installation's own `force-https.txt` (`example.net`), an enclosure and a media thumbnail.
- `rss-noid.xml`: items without `guid`, keyed by link and date.
- `page.html` (HTML + XPath), `data.xml` (XML + XPath), `feed.json` (JSON Feed), `api.json` (JSON dot notation), `embedded.html` (HTML + XPath + JSON).

The users overlap on purpose: both have feeds 1–3 and category 2, pointing at different things.
