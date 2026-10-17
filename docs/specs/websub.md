# WebSub

Status: implemented.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `p/api/pshb.php`, `FreshRSS_Feed::pubSubHubbubPrepare`, `pubSubHubbubSubscribe`, `pubSubHubbubEnabled`, `pubSubHubbubError` and `FreshRSS_feed_Controller::actualizeFeeds` of FreshRSS at commit `219eaf58`; the WebSub recommendation of the W3C for the callback and the signature.

## Purpose and scope

`internal/websub` subscribes to the hubs feeds announce and takes the documents the hubs push, so that new entries arrive without waiting for the next poll. Covers plan requirement R14.

Out of scope: subscriptions of an imported FreshRSS (its `PubSubHubbub/` directory is not imported; they are made anew), unsubscribing at the hub of our own accord, following a hub that redirects to another.

## Requirements

- W1. WebSub works when it is switched on (`-websub`, `$FRESHGO_WEBSUB`) and the public address of the server (`-base-url`) can be reached by a hub: an http(s) URL whose host is not `localhost`, not a name without a dot, and not a loopback, private or link-local address. Otherwise the server says so once at start, subscribes to nothing, does not answer under `/websub/`, and polls every feed as without WebSub.
- W2. A feed read as RSS or Atom that names a hub (`rel="hub"`) and itself (`rel="self"`) has that own address recorded as its topic, and the hub next to it, after every poll that brings its document, whether WebSub is on or not; a feed that stops naming them loses both. The links are those of the document and those of the `Link` headers of the answer, which overrule the document; an address of a header may be relative to the address of the feed. Neither the topic nor the hub keeps a user name or password in the address.
- W3. After such a poll, and at the end of every refresh for the feeds and topics recorded with them, muted feeds left out, the hub is asked to push the topic with `hub.mode=subscribe`, `hub.topic`, `hub.callback` (`<base URL>/websub/<key>`), `hub.secret` and `hub.verify=sync`:
  - the first time a feed announces a topic; key and secret are 32 random bytes each, and the subscription belongs to that feed alone: another feed that announces the same topic gets a subscription of its own, with its own key and secret;
  - when less than 23 hours of the lease are left, but no sooner than an hour after the last request to a hub in good standing;
  - 23 hours after the last request when the hub has failed, has not confirmed, has not pushed yet, or the feed now names another topic or hub. A new topic or hub gets a new key and secret, and the old callback key stops working.
  Any 2xx answer of the hub counts as accepted. Another answer marks the subscription as failing.
  So a subscription is made in the first refresh after WebSub is switched on, and renewed in time, for a feed that is not due or answers that nothing has changed.
- W4. `GET /websub/<key>` is the hub asking whether a change is wanted. For `hub.mode=subscribe` the lease is recorded (`hub.lease_seconds` over 60, otherwise no end) and `hub.challenge` is echoed. `hub.mode=unsubscribe` is confirmed, and the subscription deleted, only when the feed it belongs to is gone or muted. `hub.mode=denied` marks the subscription as failing. An unknown key, a `hub.topic` that is not the topic of the key, or a subscription whose feed is gone or muted is 404; an unknown mode is 400. Addresses are compared without regard to `http` or `https`.
- W5. `POST /websub/<key>` is a push. Its body, at most 3 MiB, has to be signed: `X-Hub-Signature: <method>=<hexadecimal HMAC of the body under the secret>` with `sha1`, `sha256`, `sha384` or `sha512`. Without a valid signature the answer is 403 and nothing changes. An unknown key is 410. An empty body is 422; a larger one 413.
- W6. The address the pushed document gives as its own, or the one with `rel="self"` in a `Link` header, which overrules it, has to be the topic; otherwise the answer is 422 and nothing changes.
- W7. A push is stored into the feed its subscription belongs to, as a poll would store it: new entries are added, changed ones rewritten, with the same extension points, rules and full-text retrieval. A feed that is muted, gone, or belongs to a disabled user takes nothing. Since a push lists only what is new, entries it does not list are not taken for gone and nothing is cleaned up; the feed does not count as polled; a failure mark of the feed is cleared. The answer is `Done: 1`. When the feed took nothing because it is gone or muted, the answer is 410 and the subscription is deleted.
- W8. A hub is trusted from its first push that was stored until it fails or its lease runs out. A feed whose hub is trusted is polled once in 24 hours, or by its own period if that is longer; a forced refresh polls it anyway.
- W9. When a poll finds a new entry in a feed whose hub is trusted, the hub has let it slip: the subscription is marked as failing and the feed is polled by its own period again, until the next push.

## Invariants and compatibility

- Subscriptions are in the database (`websub_subscriptions`), one per feed; the topic of a feed is a column of the feed. See `storage.md`, S26.
- The migration to per-feed subscriptions drops the per-topic rows; a subscription is made again at the next refresh of its feed, with a new key and secret.
- The callback address and the mandatory signature differ from FreshRSS (`/api/pshb.php?k=<key>`, no secret), so a hub that still pushes to the address of a FreshRSS installation gets no answer from freshgo; it is subscribed to anew at the first poll.

## Decisions

- **The topic of a feed is stored with the feed.** FreshRSS finds the feeds of a push by comparing the topic with their address, which loses every feed whose own address differs from the address it is subscribed by.
- **A subscription belongs to one feed.** Its key and secret are that feed's alone, and a push for it is stored into that feed and no other. A hub of a topic that several feeds announce therefore gets a subscription request from each of them, with a different callback and secret; a compromised or malicious feed can no longer receive another feed's secret, nor push entries into its readers.
- **A push never moves a feed to another address.** FreshRSS replaces the address of the feed by the pushed `rel="self"`.
- **A push triggers no cleanup of old entries**; the daily poll does it. FreshRSS runs the cleanup after one push in thirty.
- **A renewal is not repeated within an hour, and a failing hub is left alone for 23 hours even when the lease is running out.** FreshRSS asks on every refresh once less than 23 hours are left.
- **A feed that names another topic or hub is subscribed to there** after the usual pause, with a new key and secret. FreshRSS refuses to subscribe while the stored hub differs.
- **A push without a valid signature is answered with 403**, although the recommendation allows a 2xx: a hub that sees its pushes refused stops sooner.
- **Subscribing does not wait for the document of the feed.** FreshRSS subscribes and renews only when a refresh reads the feed, so a feed that answers 304 keeps its hub unasked.
- **Nothing is unsubscribed actively.** When the feed of a subscription is gone or muted, the next push is answered with 410, which tells the hub to stop.

## Verification scenarios

`internal/refresh/websub_test.go`, with a hub played by a local server and by direct calls of the callback; each on SQLite and, under `make test-integration`, on PostgreSQL.

- R14, W2, W3, W4: `TestWebSubSubscribes` — the request the hub gets, the stored subscription and topic, the confirmation and its refusals, no second request while the lease is long.
- R14, W5, W7: `TestWebSubPush` — a push stored for the feed of its subscription without a request to the feed, another feed of the same topic untouched, entries it does not list left unread under `read_upon_gone`, the time of the last poll unchanged, a muted feed and a disabled user passed over, SHA-1 and SHA-256 signatures.
- R14, W5, W6: `TestWebSubRefusesPushes` — no signature, a wrong secret, a signature of another document, an unknown method, an unknown key (410), another feed in the document or in the `Link` header (422), an empty and an oversized body; the entries stay as they were.
- R14, W3: `TestWebSubRenews` — a renewal under the same key with 22 hours left, one request to a failing hub and the next a day later. `TestWebSubHubChange` — a feed that names another hub gets a new key and secret there, and the old key and secret no longer work.
- W3: `TestWebSubSwitchedOn` — feeds read with WebSub off are subscribed in the first refresh with it on, although they answer 304, once, and not for a muted feed. `TestWebSubRenewsWithoutADocument` — a renewal for a feed that is not polled.
- W2: `TestWebSubLinkHeaders` — a hub named in headers alone, relative to the feed, and an own address of a header that overrules the document. `TestWebSubWithoutCredentials` — no password of the address of the feed in the topic or the hub.
- W8, W9: `TestWebSubPollsLessOften`. W7, the end of a subscription: `TestWebSubEndsWithItsReaders`.
- R14, W1, W2: `TestWebSubOff`, which also has the topic and the hub recorded; `TestRoutes` in `cmd/freshgo` for the address without WebSub.
- Storage: `TestWebSubSubscriptions` and `TestMigrationWebSubHub` in `internal/store`.

No real hub took part: the request for a subscription and the push are those of the recommendation as read, not as observed.
