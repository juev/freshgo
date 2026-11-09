# Native-client translation API

This optional extension uses Google Reader API authentication, not a web session.
It does not change any standard Google Reader response: those remain original.
See `translate.md` for the existing translator, settings and paragraph validation.

## Address and authorization

All operations are available at `/api/freshgo/translation` and beneath the
Google Reader alias (`/api/greader.php/api/freshgo/translation`). A deployment
prefix remains in front of either address. Send the existing
`Authorization: GoogleLogin auth=<ClientLogin Auth value>` header. API-enabled
installation/user and ownership checks apply to both reads and writes. Do not
forward this credential to the downstream translation service. No web cookies,
provider key, model selection or uploaded article body is accepted here.

- `GET /api/freshgo/translation`: JSON `{"version":1,"enabled":true}` when the
  translator is configured; `enabled:false` otherwise. Capability discovery is
  authenticated even when disabled. Identify this capability/version, not HTML
  or the server's name. Old servers may return 400/404 for an unknown extension.
- `GET /api/freshgo/translation/entry?id=<decimal ID>&language=<BCP47 tag>`:
  current translation representation; never calls the model.
- `POST /api/freshgo/translation/entry`: URL-encoded form fields `id`, `language`,
  `revision` and `done` copied from the last representation. Advances at most
  one step. The language is canonicalized by Go's language parser. `und`, invalid
  tags, negative progress and invalid IDs are refused.

## Representation (version 1)

Every response contains these JSON fields:

- `id`: decimal entry ID as a string (not the hexadecimal Google Reader item tag).
- `language`: canonical language tag.
- `sourceHash`: lowercase full SHA-256 of UTF-8 title, one NUL byte, and HTML.
  The title is the Google Reader title after decoding the fullwidth substitutions
  `＆＜＞？＼／，；` to `&<>?\/,;`. The body is exactly the Google Reader summary
  HTML, including enclosure presentation and media-proxy rewriting. No HTML or
  whitespace normalization is applied to the body. Clients must compare this
  with their selected source before requesting paid work or displaying a result.
- `revision`: opaque lowercase full SHA-256 of the stored source title, NUL and
  stored HTML, before presentation transforms. Echo it unchanged in POST.
- `exists`, `complete`, `sameLanguage`: booleans. Before translation `exists`
  and `complete` are false. An already-target-language result is stored for reuse
  and has all three true, with the original title/body and zero progress.
- `title`, `content`: translation so far, independent of the web reader's
  Show original preference. Empty when no translation exists. HTML has the
  translator's original controlled structure; clients still sanitize before use.
- `done`, `total`, `failed`: nonnegative paragraph counts. `failed <= done <= total`;
  `complete` means `exists && done == total`. A completed result with `failed > 0`
  is explicitly partial: failed paragraphs remain original, not silently omitted.

If the Google Reader source body would be truncated (stored HTML exceeds its
500000-byte limit), the extension refuses with 409 rather than translating more
text than the client selected. Reader-specific display hooks can also produce a
source mismatch; clients must fail closed, not infer equivalence from the URL.
A local full-text copy is supported only when its title/HTML fingerprint matches
this server representation. Uploading it or triggering server extraction is out
of scope.

## Progress, retries and consistency

Translation steps share a cancellable serialization gate with web translation
steps. Under that gate the server verifies ownership and source revision. A POST
whose `done` is behind committed progress reads the current result without another
model request; a complete result is also read-only. Progress ahead of the server
or a changed source is 409. This prevents replay of an already committed step,
including two concurrent requests with the same preconditions. A source changed
while the model answers gets no stale translation stored/published.

GET and API advancement preserve web display preference, including a newer Show
original choice made while a step is in flight. Brook's local Show original does
not call a server display toggle. The resulting stored translation can be reused
by the web reader.

After a lost response, GET the current representation before a user-initiated
retry. Do not blindly repeat paid POSTs. A failed or cancelled uncommitted model
request may still have cost money; this API promises neither refunds nor
exactly-once model billing across a process crash. Cancellation stops waiting or
remaining work where possible; a service may already have accepted the request.

Status codes: 400 malformed/unsupported request; 401 missing/invalid API
credential; 404 no translator or no owned entry; 409 source/progress conflict or
truncated source; 502 translation-service failure. Ordinary installation/user API
restrictions retain their existing status codes. Responses never include the
service's credential or raw error body.

## Verification

`internal/greader/translation*_test.go` exercises authenticated capability discovery,
root/alias routing, real translator steps through a synthetic local chat service,
replay/concurrency, foreign entries, source changes in flight, failure and
same-language reuse. `internal/translate/client_entry_test.go` gates an actual model
response while the web reader hides the translation. `cmd/freshgo/serve_test.go`
checks root routing. These fixtures are not a live-provider quality benchmark or
a production deployment.
