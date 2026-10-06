# Web interface

Status: in progress. Implemented: the frame of every page, texts in two languages, static files, error pages (U1–U8); logging in and who may see what (U9–U18). The rest of the interface is added step by step, see `plan.md`.
Sources: user requests of 2026-10-06 (an interface of freshgo's own, usable from the keyboard alone, with everything the web interface of FreshRSS can do, in English and Russian) and the decisions recorded in `plan.md`; FreshRSS at commit `219eaf58` for what the settings carried over by the import mean.

## Purpose and scope

`internal/web` is the web interface: pages rendered by the server, on which every action is a link or a form. It is freshgo's own; it shares neither markup, addresses, scripts, themes nor translations with the interface of FreshRSS.

Out of scope: the frontend of FreshRSS, PHP extensions, languages other than English and Russian.

## Requirements

- U1. Whatever path no other part of the server owns (`/accounts/`, `/reader/`, `/api/greader.php`, `/api/misc.php`, `/favicon/`, `/websub/`) belongs to the interface. A path it has no page for is answered with an HTML page and status 404, a method a page does not take with 405.
- U2. Every page has the same frame: a link to the content as the first thing `Tab` reaches, the name of the installation, the main menu with the current section marked `aria-current="page"`, a live region for messages (`#messages`, `role="status"`), and the content in `main#content`, which can take focus.
- U3. The language of a page is the best match among, in this order, what the browser asks for (`Accept-Language`) and the language of the installation (`language` of the system settings); English when neither is a language of the interface. A language only related to one of ours is not a match. `<html lang>` says which was chosen.
- U4. The interface speaks English and Russian. Every text exists in both, takes the same values in both, and a text that depends on a number has every plural form its language tells apart. A text missing in a language is shown in English.
- U5. Pages are sent with `Content-Security-Policy` that lets scripts and styles come from the server only (no inline code), while pictures, sound and frames of entries may come from anywhere; with `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` and `Cache-Control: no-store`.
- U6. The stylesheet and the script are built into the binary and served under `/static/`. Pages link them by an address that carries a digest of the file; asked for by that address a file may be cached for good, asked for otherwise it is revalidated.
- U7. When the public address of the server (`-base-url`) has a path, every link starts with it; requests are expected without it, as a reverse proxy that strips the path sends them.
- U8. The name shown as the installation's is `title` of the system settings. `/about` shows the version and the address API clients connect to.

- U9. How users are told apart is `auth_type` of the system settings:
  - `form`: by a login with name and password;
  - `http_auth`: by the reverse proxy in front of the server, which names the user in `Remote-User` or `X-WebAuth-User`. The header counts only when the request comes from an address in `-trusted-proxies` (`$FRESHGO_TRUSTED_PROXIES`; this machine by default), names one user, and the name can be a user name. A name nobody has yet becomes a user without passwords when `http_auth_auto_register` is on;
  - `none`: everybody is the default user.
- U10. `POST /login` takes `username`, `password`, `remember` and `next`. The password is checked against the bcrypt hash in the user's setting `passwordHash`, which import carries over, so the web password of FreshRSS keeps working. A wrong password, an unknown or malformed name, a disabled user (`enabled: false`) and a user without a web password are refused alike: status 401, one message, the name kept in the form, no cookie, and the same work done, so that neither the answer nor the time it takes tells which names exist. A name without a usable hash is checked against a decoy as costly as the hash of the default user. The body of a login request is read up to 16 KiB.
- U11. After five failed logins in a row for a name from an address, further ones from there are answered with 429 and `Retry-After` without looking at the password: for 30 seconds, then twice as long after every further failure, up to 15 minutes. Attempts are counted before the password is looked at, so attempts made at once count like attempts made in a row. A login that works, or 15 quiet minutes, clears the count. The address is that of the connection; when the connection comes from a trusted proxy, it is the last address in `X-Forwarded-For`, which is the one the proxy added. A name no user can have is refused without being counted.
- U12. A login is a random secret in the cookie `freshgo_session` (`HttpOnly`, `SameSite=Lax`, `Secure` when the browser reaches the server over HTTPS, for the public path of the interface) and its SHA-256 in the database. It lasts 24 hours after its last use; with `remember`, for `limits.cookie_duration` (90 days by default) after its last use, and the cookie then outlives the browser. Logins survive a restart of the server. `POST /logout` ends the login. A user who is disabled or deleted is out at once.
- U13. After logging in the visitor lands on `next` if it is a path of the interface, on the start page otherwise; never on another site.
- U14. A request that is not a GET or HEAD and comes from another site (`Sec-Fetch-Site`, or an `Origin` that is neither the server's nor the public address) is answered with 403 and changes nothing.
- U15. A page is for everybody, for readers, or for users. Readers are users and, when `allow_anonymous` is on, visitors who are not logged in: they read what the default user reads and can change nothing. A visitor who asks for a page that is not for them is sent to `/login?next=<the page>` when logging in is possible, and gets 403 otherwise; a request that changes something gets 403.
- U16. An administrator is the default user of the installation (`default_user`) and every user with the setting `is_admin`.
- U17. A user's pages are in their language (`language`, when it is a language of the interface) and colours (`darkMode`: `no` is light, `dark` is dark, anything else follows the system). An anonymous visitor reads in the language of their browser.
- U18. From the command line: `freshgo user create` gives the user one password for the web interface and for API clients, `-admin` makes them an administrator, and the first user of an installation becomes its default user. `freshgo user passwd` changes both passwords and ends the user's logins.

## Invariants

- Colours are CSS variables in one place, in a light and a dark set; the dark one applies by the preference of the system unless the page says `data-theme="light"`, and always when it says `data-theme="dark"`.
- Keyboard focus is visible on every control.
- Installation-wide settings live in the database: `storage.md`, S29.

## Verification scenarios

In `internal/web`, on SQLite and, under `make test-integration`, on PostgreSQL.

- U1: `TestErrorPages`; `TestRoutes` and `TestServe` in `cmd/freshgo`.
- U2, U5, U8: `TestAboutPage`.
- U3: `TestLanguageAndTitle`; `TestMatch` in `internal/web/i18n`.
- U4: `TestLanguagesAreComplete`, `TestTexts` in `internal/web/i18n`; `TestTemplatesUseKnownTexts`.
- U6: `TestStaticFiles`.
- U7: `TestPublicPath`.
- U9: `TestProxyAuthentication`, `TestNoAuthentication`; `TestTrustedProxies` in `internal/config`.
- U10: `TestLogin`, `TestLoginRefused`, `TestRefusalsTakeTheSameWork`, `TestLoginInputIsBounded`, over the reference installation of FreshRSS 1.30.1 and its passwords.
- U11: `TestLoginGuard`, `TestGuardPauses`, `TestGuardCountsParallelAttempts`, `TestLoginGuardBehindProxy`, `TestLoginInputIsBounded`.
- U12: `TestLogin`, `TestSessionLife`, `TestBehindProxy`; `TestSessions` in `internal/store`.
- U13: `TestLoginTarget`.
- U14: `TestCrossSiteRequests`, `TestBehindProxy`.
- U15: `TestAnonymousReading`, `TestLogin`.
- U16: `TestLogin`.
- U17: `TestUserPreferences`.
- U18: `TestUsersFromTheCommandLine`, `TestUserCreateAdmin` in `cmd/freshgo`; `TestFirstUserIsTheDefault` in `internal/store`.

## Decisions

- **Pages are rendered by the server** with `html/template`; there is no build step and no framework. Rejected: a single-page application over a JSON API, which doubles the code and does not work without a script.
- **Texts are JSON files, one per language, with CLDR plural categories**, chosen by `golang.org/x/text`. The translations of FreshRSS are not used: the interface has texts of its own.
- **Static files are told apart by a digest in the query string**, not in the file name: the files keep their names in the source tree and no step renames them.
- **The password is sent by the login form as it is**, protected by TLS, and compared with the stored hash. The challenge of FreshRSS, where the browser hashes the password with bcrypt, is not reproduced: it exists for logins without HTTPS and needs a script.
- **Logins are rows in the database**, not signed cookies: they can be ended from the server, which a change of the password does.
- **Requests from other sites are turned down by `http.CrossOriginProtection`** of the standard library; forms carry no token.
- **Failed logins are counted per name and address, in memory.** Counting per name alone would let anybody lock a user out; a restart of the server clears the counts.
- **Typing the password again before administrative actions (`reauth_time`) comes with the administrative pages**; the time of the last login is already recorded with the session.
