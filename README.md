# freshgo

A feed aggregator server in one binary that takes over an existing [FreshRSS](https://freshrss.org) installation. It imports the FreshRSS data, keeps refreshing the same feeds and answers the same Google Reader API, so mobile and desktop clients keep working with the tokens and article identifiers they already have. Users log in to its web interface with the web passwords they had in FreshRSS.

## Features

- stores data in SQLite or PostgreSQL;
- imports a FreshRSS installation that ran on SQLite or PostgreSQL (not MySQL/MariaDB);
- refreshes every feed kind of FreshRSS: RSS, Atom, JSON Feed, HTML and XML with XPath, JSON with dot notation, JSON embedded in HTML;
- applies the retention settings, the auto-read rules and the filter actions of FreshRSS, with its search language;
- fetches the full text of articles by CSS selector;
- serves the Google Reader API, OPML import and export, and feed icons;
- has a web interface of its own, in English and Russian, in which everything can be done from the keyboard and without JavaScript;
- subscribes to WebSub hubs;
- has extension points modelled on the FreshRSS hooks, for handlers compiled into the binary, which can also add links to the menu and a block to the login page. PHP extensions of FreshRSS do not run;
- runs as one static binary or from a container image of about 25 MB, for amd64 and arm64.

The Fever API is not implemented.

## Running on your own server

### With Docker

Every release is published as `ghcr.io/juev/freshgo`, tagged with its version (`1.2.3`), with the minor version (`1.2`) and with `latest`. The image holds the binary and the root certificates, nothing else: there is no shell in it. The server runs as user 65532, listens on port 8080 and keeps a SQLite database in the volume `/data`.

```sh
docker volume create freshgo
docker run --rm -it -v freshgo:/data ghcr.io/juev/freshgo user create alice    # asks for the password
docker run -d --name freshgo --restart unless-stopped \
  -v freshgo:/data -p 127.0.0.1:8080:8080 ghcr.io/juev/freshgo
```

The first user is the administrator. The interface is now at `http://127.0.0.1:8080`; the settings are the variables of the table below, given with `-e`. If you mount a directory of the host instead of a volume, give it to user 65532 first: `chown 65532:65532 /srv/freshgo`.

The other commands run in the same container:

```sh
docker exec freshgo /freshgo user list
docker exec -i freshgo /freshgo opml import -user alice < subscriptions.opml
```

### Behind a reverse proxy

Serve it over HTTPS from a reverse proxy and tell the server its public address. This `compose.yaml` puts [Caddy](https://caddyserver.com) in front, which gets the certificate for the name by itself:

```yaml
services:
  freshgo:
    image: ghcr.io/juev/freshgo
    restart: unless-stopped
    environment:
      FRESHGO_BASE_URL: https://rss.example.org
      FRESHGO_TRUSTED_PROXIES: 172.30.0.0/24
    volumes:
      - freshgo:/data

  caddy:
    image: caddy:2
    restart: unless-stopped
    command: caddy reverse-proxy --from rss.example.org --to freshgo:8080
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - caddy:/data

volumes:
  freshgo:
  caddy:

networks:
  default:
    ipam:
      config:
        - subnet: 172.30.0.0/24
```

`FRESHGO_TRUSTED_PROXIES` names the network of the two containers. The proxy connects from an address of that network, not from the loopback address the default expects, and without the setting the server would count the failed logins of all visitors against the proxy.

```sh
docker compose run --rm freshgo user create alice
docker compose up -d
```

### With PostgreSQL

Set `FRESHGO_DATABASE_URL` to `postgres://user:password@host:5432/freshgo`. The server creates its tables at the first start; the volume `/data` is then not used.

### Updates and backups

To update, pull the new image and start the container again: `docker compose pull && docker compose up -d`. The server brings the database to its version when it starts.

To back up a SQLite installation, stop the container and copy the volume: next to `freshgo.sqlite` lie the files `-wal` and `-shm`, and a copy taken from a running server may miss what they hold.

### Without Docker

The binary needs nothing beside it. Every [release](https://github.com/juev/freshgo/releases) has archives for Linux and macOS, amd64 and arm64, with a file of checksums. Or build it with Go 1.27:

```sh
make build        # bin/freshgo
```

Run `freshgo serve` under the service manager of the system, with the settings in its environment; "Starting without FreshRSS" below shows the commands.

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

   With the image, mount the data directory, which user 65532 must be able to read, and import into the volume the server will use:

   ```sh
   docker run --rm -v freshgo:/data -v /path/to/FreshRSS/data:/import:ro ghcr.io/juev/freshgo import -data /import
   ```

4. Put it where FreshRSS was. Clients configured with `https://rss.example.org/api/greader.php` keep working without changes: that path is served as an alias, and the API passwords, the tokens already issued and the identifiers of articles, feeds and labels are the imported ones.

What the import carries over: users with their settings, web and API passwords, the settings of the installation (its title when it has one of its own, how users log in, the default user, anonymous reading, limits, the terms of use, the proxy all feeds went through), categories, feeds with their settings, articles with read and starred states, labels, custom feed icons, the installation's own `force-https.txt`.

What it does not: the theme and the other settings of the FreshRSS pages that the interface of freshgo has no counterpart for, icons fetched from sites (fetched again after the first refresh of a feed), WebSub subscriptions (made again at the first refresh, see below), articles left half-stored by an interrupted refresh.

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

freshgo user create alice                      # asks for the password
freshgo feed add -user alice -category News https://example.org/
freshgo opml import -user alice subscriptions.opml
freshgo serve
```

`feed add` takes the address of a feed or of a site that announces one. `user create` and `user passwd` ask for the password at a terminal and otherwise read it from the first line of standard input; it must be at least 7 characters long. The password is for the web interface and for API clients alike. The first user of an installation is its administrator; `user create -admin` makes further ones. Changing the password invalidates the tokens clients hold and ends the user's logins.

## Web interface

Open the address of the server in a browser and sign in. Pages are rendered by the server: every action is a link or a form and works without JavaScript; the script adds keys and actions that do not reload the page. On a phone the menu and the controls of a stream (search, state, order) fold behind buttons, a tap on a row opens its entry and closes the one that was open, and a bar at the foot of the screen opens the next and the previous entry, marks read and stars. The browser of a phone or a desktop can install freshgo as an application ("Add to Home Screen", "Install"); that takes HTTPS, and nothing is read offline.

The reading screen has the tree of categories, feeds, saved queries and labels on the left and the entries on the right, laid out after Google Reader: an entry is one row with its star, feed, title and the beginning of its text, and opens in place as a card. "List" and "Expanded" above the entries switch between rows and open entries. The pages come in two looks, "Reader classic" and "Reader 2011", each with light and dark colours; the choice is on the page "Display" of the settings. The search field takes the search language of FreshRSS. The other sections are subscriptions (feeds with all their settings, categories, labels, import and export), statistics, settings, and administration for administrators (users, the installation, how users log in, the log).

### Keys

| Key | Action |
|---|---|
| `j`, `k` | open the next, the previous entry |
| `h` | open the next unread entry |
| `n`, `p` | move to the next, the previous entry without opening it |
| `o` or `Enter` | open or close the entry |
| `Space` | page through the entry, then go to the next one |
| `v` | open the original in a new tab |
| `m` | mark read or unread |
| `s` | star |
| `l` | labels |
| `S` | share |
| `A` | mark everything shown as read |
| `r` | refresh the feeds |
| `/` | search |
| `J`, `K`, `U` | next, previous, next unread stream of the tree |
| `t` | show or hide the tree |
| `g u`, `g a`, `g s` | everything: unread, all, starred entries |
| `g f`, `g k` | go to subscriptions, keys |
| `+` | add a feed |
| `:` or `Ctrl+K` | palette of commands: finds any stream, feed, category, label, saved query, page and action by a few letters |
| `?` | help with the keys in force |
| `Esc` | close a dialog |

Keys are changed on the page "Keys" of the settings, where one checkbox switches off all keys pressed alone. A user imported from FreshRSS keeps the keys they changed there; the keys FreshRSS gave by default make way for those above. Keys do nothing while the focus is in a field.

### How users log in

An administrator chooses on the page "Signing in":

- **by password** (the default), with the form of the server;
- **by the reverse proxy**, which names the user in the header `Remote-User` or `X-WebAuth-User`. The header counts only on requests from the addresses of `-trusted-proxies`, so the proxy must remove a header of that name sent by the browser. With automatic registration on, a name nobody has yet becomes a user;
- **without a login**: everybody is the default user.

Anonymous reading, a separate switch, lets visitors who are not logged in read what the default user reads and change nothing.

Behind a reverse proxy set `-base-url` to the public address. If the address has a path, such as `https://example.org/rss`, the proxy has to strip that path from requests; links are written with it. The server counts failed logins per address and takes the address of the browser from `X-Forwarded-For` only when the connection comes from a trusted proxy.

A saved query can be handed out without a login at `/shared/<token>.rss`, `.atom`, `.html`, `.opml` or `.json`; the addresses FreshRSS gave for shared queries and for the feed of a user (`/api/query.php`, `/i/?a=rss`) keep answering.

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
| `serve` | Serves the web interface, answers API clients and refreshes the feeds that are due, at start and every `-refresh-interval`. |
| `refresh [-force]` | Refreshes the feeds that are due once, for cron; `-force` takes all of them. |
| `purge` | Deletes old articles by the retention settings without waiting for a refresh. |
| `import -data <dir>` | Imports a FreshRSS installation into an empty database. |
| `user create [-admin]\|passwd\|delete <name>`, `user list` | Manages users. Deleting a user deletes their feeds and articles. |
| `feed add -user <name> [-category <name>] <address>` | Subscribes a user to a feed and fetches it. |
| `opml import -user <name> [<file>]`, `opml export -user <name>` | Reads subscriptions from a file or standard input; writes them to standard output. |
| `version` | Prints the version. |

`freshgo <command> -h` lists the flags of a command.

## Settings

Every setting is a flag and an environment variable; the flag wins. The help of a command shows a value of the environment as the default of its flag, except for the three that carry passwords: `-database-url`, `-smtp-url` and `-oidc-client-secret`.

| Flag | Variable | Default | Meaning |
|---|---|---|---|
| `-database-url` | `FRESHGO_DATABASE_URL` | `sqlite://freshgo.sqlite` | `sqlite://<path>` or `postgres://<dsn>`. |
| `-listen` | `FRESHGO_LISTEN` | `127.0.0.1:8080` | Address the HTTP server binds to. |
| `-base-url` | `FRESHGO_BASE_URL` | | Public address of the server. Set it behind a reverse proxy: links to feed icons are built from it, and WebSub needs it. |
| `-refresh-interval` | `FRESHGO_REFRESH_INTERVAL` | `10m` | How often `serve` looks for feeds that are due. |
| `-fetch-allowlist` | `FRESHGO_FETCH_ALLOWLIST` | | Internal destinations feeds may be fetched from: `host:port`, a CIDR range, or `*`, separated by commas. Without it, requests to private and loopback addresses are refused. |
| `-websub` | `FRESHGO_WEBSUB` | off | Subscribe to the WebSub hubs feeds announce. |
| `-trusted-proxies` | `FRESHGO_TRUSTED_PROXIES` | `127.0.0.0/8,::1/128` | Reverse proxies whose word is taken for who the user is and for the address of the browser: addresses or CIDR ranges, separated by commas. |
| `-oidc-client-secret` | `FRESHGO_OIDC_CLIENT_SECRET` | | Secret of the client the installation is at its OpenID Connect provider; see "Signing in through a provider". |
| `-smtp-url` | `FRESHGO_SMTP_URL` | | SMTP server for the letters that confirm e-mail addresses: `smtp[s]://user:password@host:port?from=address`. Without it, confirmation cannot be required. |

### Signing in through a provider

Next to the login by password, users can sign in through an OpenID Connect provider such as [Pocket ID](https://pocket-id.org):

1. At the provider, register a client (a confidential one, with a secret) whose callback address is `<public address of the server>/oidc/callback`, and choose there who may sign in. The page "Authentication" of the administration shows the address.
2. Start the server with the secret of the client in `FRESHGO_OIDC_CLIENT_SECRET` and with `-base-url`.
3. On the page "Authentication", enter the address of the provider (the issuer: the address its `/.well-known/openid-configuration` lies under) and the client ID.

The login page then has a link to the provider. The user is the one the provider names in the claim `preferred_username`, which has to be a user name here. A name nobody has gets a user when "Create a user for a name the reverse proxy or the provider vouches for" is on, and is refused otherwise. Administrators are made in freshgo, not at the provider. The API passwords of apps are not touched.

### Images of articles

The server can hand out the images of articles from its own address, so that the sites they lie on see the server and not the reader. The setting is on the page "System" of the administration:

- "those served over http" (the default): only the images a page served over `https` would not show at all;
- "all": every image;
- "none": the addresses stay as the feeds have them.

It holds for the web interface and for apps that read through the Google Reader API; for apps, start the server with `-base-url` so that the addresses lead to where the apps reach it. The addresses are signed: the server fetches only what it has put into an article itself. Images are passed through, not stored, and the browser is told to keep them for three days. Audio and video are not handed out.

### Proxy

An administrator sets one proxy for all feeds on the page "System" of the administration: `http://`, `https://`, `socks5://` or `socks5h://`, with `user:password@` when it asks for them. Feeds, the pages of their articles and icons are then fetched through it, from the next request on. A feed keeps a proxy of its own, and can be set to go through none. The proxy of the environment (`HTTP_PROXY`) is not used.

### WebSub

With `-websub` and a `-base-url` that hubs can reach, a feed that announces a hub, in its document or in the `Link` headers of the answer, is subscribed to at the next refresh, and new articles arrive when the hub pushes them; such a feed is then polled once a day. The path `/websub/` must be reachable from outside without authentication. Pushes are accepted only with a valid signature. Without a public address the server logs one warning and polls all feeds as usual.

### Paths the server answers

`/accounts/` and `/reader/` (Google Reader API), `/api/greader.php` (the same API at the FreshRSS path), `/favicon/` (feed icons), `/websub/` (when WebSub is on), `/api/misc.php` (extensions). Every other path belongs to the web interface.

## Development

```sh
make test               # unit tests, SQLite only
make test-integration   # the same tests on SQLite and on a PostgreSQL started in Docker
make lint
make test-e2e           # needs Chrome: the interface in a headless browser, keyboard only, with accessibility checks
```

GitHub Actions runs the lint, the integration tests, the browser tests and a trial build of the release on every push to `main` and on every pull request (`.github/workflows/ci.yml`). A release is a tag:

```sh
git tag v1.2.3
git push origin v1.2.3
```

[GoReleaser](https://goreleaser.com) then builds the binaries, publishes the GitHub release with the archives and a changelog made from the commit messages, and pushes the image for both platforms to `ghcr.io` (`.goreleaser.yaml`, `.github/workflows/release.yml`). `goreleaser release --snapshot --clean` does the same locally without publishing anything.

Behaviour is checked against a real FreshRSS 1.30.1: `testdata/reference/` holds what it produced from a fixed corpus of feeds and what it answered to a list of API requests; `testdata/reference/README.md` says how to regenerate it. The contracts are in `docs/specs/`.

## License

AGPL-3.0, as FreshRSS, whose behaviour this code follows.
