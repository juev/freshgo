# WebSub

Status: implemented.
Sources: user request of 2026-10-06 and the decisions recorded in `plan.md`; `p/api/pshb.php`, `FreshRSS_Feed::pubSubHubbubPrepare`, `pubSubHubbubSubscribe`, `pubSubHubbubEnabled`, `pubSubHubbubError` and `FreshRSS_feed_Controller::actualizeFeeds` of FreshRSS at commit `219eaf58`; the WebSub recommendation of the W3C for the callback and the signature.

## Purpose and scope

`internal/websub` subscribes to the hubs feeds announce and takes the documents the hubs push, so that new entries arrive without waiting for the next poll. Covers plan requirement R14.

Out of scope: subscriptions of an imported FreshRSS (its `PubSubHubbub/` directory is not imported; they are made anew), unsubscribing at the hub of our own accord, following a hub that redirects to another.

## Requirements

- W1. WebSub works when it is switched on (`-websub`, `$FRESHGO_WEBSUB`) and the public address of the server (`-base-url`) can be reached by a hub: an http(s) URL whose host is not `localhost`, not a name without a dot, and not a loopback, private or link-local address. Otherwise the server says so once at start, subscribes to nothing, does not answer under `/websub/`, and polls every feed as without WebSub.
- W2. A feed read as RSS or Atom that names a hub (`rel="hub"`) and itself (`rel="self"`) has that own address recorded as its topic after every successful poll; a feed that stops naming them loses the topic.
- W3. After such a poll the hub is asked to push the topic with `hub.mode=subscribe`, `hub.topic`, `hub.callback` (`<base URL>/websub/<key>`), `hub.secret` and `hub.verify=sync`:
  - the first time any feed announces the topic; key and secret are 32 random bytes each, and one subscription serves every feed and user that announces the topic;
  - when less than 23 hours of the lease are left, but no sooner than an hour after the last request to a hub in good standing;
  - 23 hours after the last request when the hub has failed, has not confirmed, has not pushed yet, or the feed now names another hub.
  Any 2xx answer of the hub counts as accepted. Another answer marks the subscription as failing.
- W4. `GET /websub/<key>` is the hub asking whether a change is wanted. For `hub.mode=subscribe` the lease is recorded (`hub.lease_seconds` over 60, otherwise no end) and `hub.challenge` is echoed. `hub.mode=unsubscribe` is confirmed, and the subscription deleted, only when no feed announces the topic any more. `hub.mode=denied` marks the subscription as failing. An unknown key, a `hub.topic` that is not the topic of the key, or a subscription no feed needs is 404; an unknown mode is 400. Addresses are compared without regard to `http` or `https`.
- W5. `POST /websub/<key>` is a push. Its body, at most 3 MiB, has to be signed: `X-Hub-Signature: <method>=<hexadecimal HMAC of the body under the secret>` with `sha1`, `sha256`, `sha384` or `sha512`. Without a valid signature the answer is 403 and nothing changes. An unknown key is 410. An empty body is 422; a larger one 413.
- W6. The address the pushed document gives as its own, or the one with `rel="self"` in a `Link` header, which overrules it, has to be the topic; otherwise the answer is 422 and nothing changes.
- W7. A push is stored into every feed that announces the topic, for every user, as a poll would store it: new entries are added, changed ones rewritten, with the same extension points, rules and full-text retrieval. Muted feeds and feeds of disabled users are passed over. Since a push lists only what is new, entries it does not list are not taken for gone and nothing is cleaned up; the feed does not count as polled; a failure mark of the feed is cleared. The answer is `Done: <number of feeds>`. When no feed took the document, the answer is 410 and the subscription is deleted.
- W8. A hub is trusted from its first push that was stored until it fails or its lease runs out. A feed whose hub is trusted is polled once in 24 hours, or by its own period if that is longer; a forced refresh polls it anyway.
- W9. When a poll finds a new entry in a feed whose hub is trusted, the hub has let it slip: the subscription is marked as failing and the feed is polled by its own period again, until the next push.

## Invariants and compatibility

- Subscriptions are in the database (`websub_subscriptions`), one per topic; the topic of a feed is a column of the feed. See `storage.md`, S26.
- The callback address and the mandatory signature differ from FreshRSS (`/api/pshb.php?k=<key>`, no secret), so a hub that still pushes to the address of a FreshRSS installation gets no answer from freshgo; it is subscribed to anew at the first poll.

## Decisions

- **The topic of a feed is stored with the feed.** FreshRSS finds the feeds of a push by comparing the topic with their address, which loses every feed whose own address differs from the address it is subscribed by.
- **A push never moves a feed to another address.** FreshRSS replaces the address of the feed by the pushed `rel="self"`.
- **A push triggers no cleanup of old entries**; the daily poll does it. FreshRSS runs the cleanup after one push in thirty.
- **A renewal is not repeated within an hour, and a failing hub is left alone for 23 hours even when the lease is running out.** FreshRSS asks on every refresh once less than 23 hours are left.
- **A feed that names another hub is subscribed to there** after the usual pause. FreshRSS refuses to subscribe while the stored hub differs.
- **A push without a valid signature is answered with 403**, although the recommendation allows a 2xx: a hub that sees its pushes refused stops sooner.
- **Nothing is unsubscribed actively.** When the last feed of a topic is gone, the next push is answered with 410, which tells the hub to stop.

## Verification scenarios

`internal/refresh/websub_test.go`, with a hub played by a local server and by direct calls of the callback; each on SQLite and, under `make test-integration`, on PostgreSQL.

- R14, W2, W3, W4: `TestWebSubSubscribes` — the request the hub gets, the stored subscription and topic, the confirmation and its refusals, no second request while the lease is long.
- R14, W5, W7: `TestWebSubPush` — a push stored for two users without a request to the feed, entries it does not list left unread under `read_upon_gone`, the time of the last poll unchanged, a muted feed and a disabled user passed over, SHA-1 and SHA-256 signatures.
- R14, W5, W6: `TestWebSubRefusesPushes` — no signature, a wrong secret, a signature of another document, an unknown method, an unknown key (410), another feed in the document or in the `Link` header (422), an empty and an oversized body; the entries stay as they were.
- R14, W3: `TestWebSubRenews` — a renewal under the same key with 22 hours left, one request to a failing hub and the next a day later.
- W8, W9: `TestWebSubPollsLessOften`. W7, the end of a subscription: `TestWebSubEndsWithItsReaders`.
- R14, W1: `TestWebSubOff`; `TestRoutes` in `cmd/freshgo` for the address without WebSub.
- Storage: `TestWebSubSubscriptions` in `internal/store`.

No real hub took part: the request for a subscription and the push are those of the recommendation as read, not as observed.
