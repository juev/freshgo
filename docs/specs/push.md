# Web Push

Status: implemented.
Sources: GitHub issue 138 and its decisions (Web Push as a service worker, VAPID keys from the settings, a push when a refresh brings new entries); RFC 8030 (Web Push), RFC 8291 (message encryption), RFC 8292 (VAPID).

## Purpose and scope

`internal/push` tells the users who asked for it, through the push service of their browser, that unread entries came to their reading list. The interface (`web.md`, U110) subscribes a browser; `internal/refresh` calls the notifier after it stores entries (`refresh.md`, F40); the library `github.com/SherClockHolmes/webpush-go` encrypts the message and signs the request.

Out of scope: a message for an entry the user changed, one for each entry, an action on the notification, a choice of feeds or categories, a quiet period, a notification of a user who is not logged in anywhere (a browser is subscribed from a logged-in profile only), and anything offline: the service worker shows messages and reads nothing.

## Requirements

### Settings

- P1. Web Push is on when `-vapid-public-key`, `-vapid-private-key` and `-vapid-subject` (`FRESHGO_VAPID_PUBLIC_KEY`, `FRESHGO_VAPID_PRIVATE_KEY`, `FRESHGO_VAPID_SUBJECT`, the same names in the file of `-config`) are all set; some of the three set and others not stops the command. The keys are base64url, a P-256 key pair as `freshgo vapid-keys` prints it: the private key is 32 bytes, the public key the uncompressed point of the private one, 65 bytes, and keys that are not a pair are refused. The subject is `mailto:` with an address, or an https address, the two forms RFC 8292 gives the push service to contact the operator at. The help of a command does not show the private key.
- P2. `freshgo vapid-keys` prints a new pair on two lines, `vapid-public-key: …` and `vapid-private-key: …`, which are lines of a configuration file and, with `-` before the name and `=` for `: `, flags. Every run prints another pair. It takes no arguments, writes nothing and needs no database. It is in the list of commands and in the scripts of completion, as are the three flags.

### Subscribing

- P3. The interface offers push only while it is on (P1): the part of the profile, the worker and the routes are there only then (`web.md`, U110).
- P4. A subscription is a row of `push_subscriptions` (`storage.md`): `endpoint` (unique), `p256dh`, `auth`, the user (`ON DELETE CASCADE`) and the time. The endpoint names a browser, so subscribing from a browser again replaces its keys, and subscribing from it as another user moves it to that user: the row of the user who left it is gone. A user takes back only their own rows.
- P5. A subscription is accepted when the endpoint is an https address of at most 1024 characters without a password, `p256dh` is 65 bytes and `auth` 16 bytes of base64url, and the user has fewer than 10 browsers or this one among them (a message goes to each).

### Sending

- P6. When `Refresher.Notify` is called for a user (`refresh.md`, F40) who has subscriptions, the notifier looks for the unread entries of the reading list of the user (`greader-api.md`, A13: feeds of priority 0 and above, as the digest does) whose identifier, the time the entry was added, is from the start of the refresh on. With none, nothing is sent: entries a rule marked read, entries of hidden feeds, and a refresh that stored nothing new are not news.
- P7. With some, every browser of the user gets one message, whatever the number of feeds and entries of the pass. It is JSON: `title`, the title of the installation, and `body`, the number of entries and the title of the newest, in the language of the user (`language`, then that of the installation) in the forms of the language for the number; the title of the entry is on one line, 100 characters at most, a placeholder when it has none. The service worker shows both and takes the user to the interface on a click.
- P8. The message is encrypted to the browser (RFC 8291, `Content-Encoding: aes128gcm`) and sent as a `POST` to its endpoint, with `Authorization: vapid t=<JWT>, k=<public key>` (RFC 8292; the claims are the origin of the endpoint, an expiry ahead and the subject), `TTL` of a day, `Urgency: normal` and `Topic: new-entries`, so that a push service keeps one message for a browser that is off.
- P9. The request goes through the fetch client of the installation, so the address of the browser is held to the rules of `-fetch-allowlist` and the proxy of the installation: an endpoint that names an internal address is not requested, and the subscription stays. Redirects are not followed. The log names the host of the endpoint, never the endpoint, which is the browser's secret.
- P10. The sending does not hold the refresh back or fail it. `Notify` reads the subscriptions and the entries, starts a goroutine for each browser and returns; each send has 10 seconds. A failure is logged and goes no further. An answer of 404 or 410 means the push service has forgotten the browser: its subscription is deleted. Any other answer of 300 or above is logged and the subscription stays. `freshgo serve` and `freshgo refresh` wait for the messages on their way before they close the database.

## Decisions

- **The refresher calls a function; the registry of hooks is not used.** The hooks of `hooks.md` are those of FreshRSS and cross a boundary where extensions are compiled in. The notifier is part of the server and wants the time the refresh of a user began, which no hook carries. The field is `Refresher.Notify`, like `Icons` and `WebSub`. It is called from `Run` after each user, so that a user gets one message for a pass, and from the push of a hub, which is not part of a run.
- **News is what the user would see.** The notifier asks the store for the unread entries of the reading list since the start of the refresh, as the digest does, rather than counting `Stats.NewEntries`: an entry a rule marked read, or of a feed with a priority below 0, was stored but is not unread news. A feed subscribed just now brings its backlog, and so one message that counts it.
- **One message per pass, not per entry.** The count and the newest title are what a notification has room for. `Topic` lets the push service replace a message the browser has not got yet.
- **The subject is passed without `mailto:`** because the library writes the prefix itself unless the subject is an https address.
- **A copy of the message per browser.** The library pads the encrypted record in the spare capacity of the slice it is given; browsers sharing one slice raced and sent each other's padding.
- **The library** is `webpush-go` v1.4.0 (Go, MIT; the last release 2025-01-02, the repository pushed to in 2026-04, no archive): it does the encryption of RFC 8291 and the VAPID signing of RFC 8292 and takes a client for the request, which is how the fetch client's rules (P9) apply. Writing the cryptography here was not worth it.
- **No Service-Worker-Allowed header**: the worker is served at `<public path>/sw.js`, and its scope, its directory, is the whole interface.
- **Messages of a hub push count.** A feed whose hub delivers is polled once a day; without this, the users of such feeds would hear of nothing but at the daily poll.
- **Browsers need HTTPS.** Service workers and push work in browsers on https addresses and on `localhost` only; nothing in freshgo changes that.

## Verification scenarios

`internal/push`, on SQLite and, under `make test-integration`, on PostgreSQL; the push service is an `httptest` server that records the requests, and the browser of the test makes its own keys and decrypts what it was sent.

- P6–P8: `TestRefreshNotifiesEachBrowserOnce` (a refresh with three new entries sends one message to each of the two browsers of a user and to the one of another, none for a user without browsers; the body is decrypted and read, in Russian for the user who chose it; `Content-Encoding`, `TTL`, `Topic`, `Authorization: vapid` with the public key and claims for the origin, the subject and an expiry; a second refresh with nothing new sends none; one new entry is said in the singular with its title), `TestRefreshDoesNotNotifyOfHiddenEntries` (a feed of priority −10, entries a rule marked read).
- P9: `TestInternalEndpointIsNotRequested`, and in `TestPushServiceAnswers` that the log has no endpoint.
- P10: `TestPushServiceAnswers` (410 and 404 delete the subscription, 500 does not, the other browsers are sent theirs), `TestSlowPushServiceDoesNotHoldRefreshBack` (the run returns while the push service holds its answer).
- F40: `TestNotifyHearsOfNewEntries` in `internal/refresh`.
- P1: `TestVAPIDSettings`, `TestHelpKeepsVAPIDPrivateKey` in `internal/config`.
- P2: `TestVapidKeys` (read back as a file of `-config` and as flags, and validated), `TestCompletionKnowsWebPush` in `cmd/freshgo`.
- P3–P5: `TestPushIsOfferedWithKeys`, `TestPushIsNotOfferedWithoutKeys`, `TestPushSubscribeAndUnsubscribe` (per user), `TestPushRefusesWhatIsNotASubscription` (the bound of ten included), `TestPushRoutesAreProtected` (login, requests from another site) in `internal/web`; `TestPushSubscriptions` (the endpoint is unique, a browser moves between users, deleted with the user) in `internal/store`.
- Not run in a test: the page script and the service worker in a real browser, and a real push service. Both were checked by reading only.
