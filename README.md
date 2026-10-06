# freshgo

A feed aggregator server in one binary that takes over an existing [FreshRSS](https://freshrss.org) installation. It imports the FreshRSS data, keeps refreshing the same feeds and answers the same Google Reader API, so mobile and desktop clients keep working with the tokens and article identifiers they already have.

freshgo has no web interface yet: you read through a Google Reader API client and administer from the command line.

What it does:

- stores data in SQLite or PostgreSQL;
- imports a FreshRSS installation that ran on SQLite or PostgreSQL (not MySQL/MariaDB);
- refreshes every feed kind of FreshRSS: RSS, Atom, JSON Feed, HTML and XML with XPath, JSON with dot notation, JSON embedded in HTML;
- applies the retention settings, the auto-read rules and the filter actions of FreshRSS, with its search language;
- fetches the full text of articles by CSS selector;
- serves the Google Reader API, OPML import and export, and feed icons;
- subscribes to WebSub hubs;
- has extension points modelled on the FreshRSS hooks, for handlers compiled into the binary. PHP extensions of FreshRSS do not run.

The Fever API is not implemented.

## Build

Needs Go 1.27.

```sh
make build        # bin/freshgo
```

## Moving from FreshRSS

The move is one-way: a freshgo database cannot be taken back to FreshRSS. Keep the FreshRSS data directory until you are satisfied.

1. Stop FreshRSS, or at least its cron, so that the data does not change during the import.

2. Import the data directory, the one with `config.php` and `users/`, into an empty database:

   ```sh
   freshgo import -database-url sqlite:///var/lib/freshgo/freshgo.sqlite -data /path/to/FreshRSS/data
   ```

   For a FreshRSS on PostgreSQL the connection is read from its `config.php`; `-source-database-url` overrides it when the database is reached at another address from where you run the import. The command prints what it carried over for every user and, as warnings, what will behave differently. It is all or nothing: on an error the database stays empty.

3. Start the server:

   ```sh
   freshgo serve -database-url sqlite:///var/lib/freshgo/freshgo.sqlite -listen 0.0.0.0:8080
   ```

4. Put it where FreshRSS was. Clients configured with `https://rss.example.org/api/greader.php` keep working without changes: that path is served as an alias, and the API passwords, the tokens already issued and the identifiers of articles, feeds and labels are the imported ones.

What the import carries over: users with their settings and API passwords, categories, feeds with their settings, articles with read and starred states, labels, custom feed icons, the installation's own `force-https.txt`.

What it does not: web passwords (there is no web interface), icons fetched from sites (fetched again after the first refresh of a feed), WebSub subscriptions (made again at the first refresh, see below), articles left half-stored by an interrupted refresh.

### What behaves differently

- **Dates without a time zone** in feeds are read in the time zone of the user, and an empty `timezone` setting means the zone of the server. Moving to a server in another zone shifts such dates, and, for feeds whose articles are told apart by link and date, may bring current articles in once more.
- **Feeds whose uniqueness criteria include the content** may get each current article once more at the first refresh; the import names these feeds.
- **XPath runs over an HTML5 tree**, not the one libxml builds. An expression such as `//table/tr` stops matching, because HTML5 puts rows inside `tbody`; write `//table//tr`.
- **Filter actions ignore case for all letters**, as FreshRSS does on PostgreSQL. On a FreshRSS that ran on SQLite, only ASCII letters were compared without case, so rules with other letters now match more.
- **Regular expressions in filters are RE2.** Rules with backreferences or lookaround are skipped with a warning in the log; the import lists them.
- **Old articles are cleaned up at every refresh of a feed**, not at a random one in thirty. The rules are the same.
- **Full text and content filters apply to new and changed articles only.** Changing the selector of a feed does not reload the articles already stored.

The full lists are in the Decisions sections of `docs/specs/`.

## Starting without FreshRSS

```sh
export FRESHGO_DATABASE_URL=sqlite:///var/lib/freshgo/freshgo.sqlite

freshgo user create alice                      # asks for the API password
freshgo feed add -user alice -category News https://example.org/
freshgo opml import -user alice subscriptions.opml
freshgo serve
```

`feed add` takes the address of a feed or of a site that announces one. `user create` and `user passwd` ask for the password at a terminal and otherwise read it from the first line of standard input; it must be at least 7 characters long. Changing the password invalidates the tokens clients hold.

## Clients

Give a client the address of the server itself, `https://rss.example.org`, or the path FreshRSS uses, `https://rss.example.org/api/greader.php`, together with the user name and the API password. Both addresses answer alike.

Newsboat, for example:

```
urls-source "freshrss"
freshrss-url "https://rss.example.org/api/greader.php"
freshrss-login "alice"
freshrss-password "…"
```

## Commands

| Command | What it does |
|---|---|
| `serve` | Answers API clients and refreshes the feeds that are due, at start and every `-refresh-interval`. |
| `refresh [-force]` | Refreshes the feeds that are due once, for cron; `-force` takes all of them. |
| `purge` | Deletes old articles by the retention settings without waiting for a refresh. |
| `import -data <dir>` | Imports a FreshRSS installation into an empty database. |
| `user create\|passwd\|delete <name>`, `user list` | Manages users. Deleting a user deletes their feeds and articles. |
| `feed add -user <name> [-category <name>] <address>` | Subscribes a user to a feed and fetches it. |
| `opml import -user <name> [<file>]`, `opml export -user <name>` | Reads subscriptions from a file or standard input; writes them to standard output. |
| `version` | Prints the version. |

`freshgo <command> -h` lists the flags of a command.

## Settings

Every setting is a flag and an environment variable; the flag wins.

| Flag | Variable | Default | Meaning |
|---|---|---|---|
| `-database-url` | `FRESHGO_DATABASE_URL` | `sqlite://freshgo.sqlite` | `sqlite://<path>` or `postgres://<dsn>`. |
| `-listen` | `FRESHGO_LISTEN` | `127.0.0.1:8080` | Address the HTTP server binds to. |
| `-base-url` | `FRESHGO_BASE_URL` | | Public address of the server. Set it behind a reverse proxy: links to feed icons are built from it, and WebSub needs it. |
| `-refresh-interval` | `FRESHGO_REFRESH_INTERVAL` | `10m` | How often `serve` looks for feeds that are due. |
| `-fetch-allowlist` | `FRESHGO_FETCH_ALLOWLIST` | | Internal destinations feeds may be fetched from: `host:port`, a CIDR range, or `*`, separated by commas. Without it, requests to private and loopback addresses are refused. |
| `-websub` | `FRESHGO_WEBSUB` | off | Subscribe to the WebSub hubs feeds announce. |

### WebSub

With `-websub` and a `-base-url` that hubs can reach, a feed that announces a hub is subscribed to at its next refresh, and new articles arrive when the hub pushes them; such a feed is then polled once a day. The path `/websub/` must be reachable from outside without authentication. Pushes are accepted only with a valid signature. Without a public address the server logs one warning and polls all feeds as usual.

### Paths the server answers

`/accounts/` and `/reader/` (Google Reader API), `/api/greader.php` (the same API at the FreshRSS path), `/favicon/` (feed icons), `/websub/` (when WebSub is on), `/api/misc.php` (extensions).

## Development

```sh
make test               # unit tests, SQLite only
make test-integration   # the same tests on SQLite and on a PostgreSQL started in Docker
make lint
```

Behaviour is checked against a real FreshRSS 1.30.1: `testdata/reference/` holds what it produced from a fixed corpus of feeds and what it answered to a list of API requests; `testdata/reference/README.md` says how to regenerate it. The contracts are in `docs/specs/`.

## License

AGPL-3.0, as FreshRSS, whose behaviour this code follows.
