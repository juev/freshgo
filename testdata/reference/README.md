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

`sqlite/` and `pgsql/` are generated and committed, so tests need no Docker to read them.

## Regenerating

```sh
testdata/reference/generate.sh          # both engines
testdata/reference/generate.sh sqlite   # one of them
```

Needs Docker and Go. The script starts `freshrss/freshrss:1.30.1`, a web server with the corpus and, for `pgsql`, PostgreSQL; installs FreshRSS, creates alice and bob, imports their OPML, refreshes the feeds, runs the scenario and copies the result out.

Entry identifiers, salts and password hashes change on every run. Tests therefore read expected values from the generated files or find entries by title; after regenerating, only counts written into tests (`internal/importer/importer_test.go`, `cmd/freshgo/main_test.go`) need a look, and only if the corpus or the scenario changed.

## What the corpus covers

- `atom.xml`: `atom:id` identifiers, including one with `&` and non-ASCII characters, one on a force-https domain and one with surrounding spaces; several authors; HTML, XHTML and text content.
- `rss.xml`: `guid` with and without `isPermaLink`, markup characters in a guid, a subdomain of a force-https domain, a domain from the installation's own `force-https.txt` (`example.net`), an enclosure and a media thumbnail.
- `rss-noid.xml`: items without `guid`, keyed by link and date.
- `page.html` (HTML + XPath), `data.xml` (XML + XPath), `feed.json` (JSON Feed), `api.json` (JSON dot notation), `embedded.html` (HTML + XPath + JSON).

The users overlap on purpose: both have feeds 1–3 and category 2, pointing at different things.
