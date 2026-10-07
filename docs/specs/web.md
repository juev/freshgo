# Web interface

Status: in progress. Implemented: the frame of every page, texts in two languages, static files, error pages (U1–U8); logging in and who may see what (U9–U18); the reading screen without a script (U19–U31). The rest of the interface is added step by step, see `plan.md`.
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
- U8. The name shown as the installation's is `title` of the system settings. `/about` shows the version and the address API clients connect to. The start page `/` is the reading screen.

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

The reading screen, for readers (U15); what changes something is for users.

- U19. Streams of entries and their addresses: `/` and `/all`, the entries of the feeds shown in the main stream (priority 10 and above); `/starred`, the starred entries of every feed; `/feeds/<id>`, a feed whatever its priority; `/categories/<id>`, the feeds of a category shown in categories (priority 0 and above); `/labels/<id>`, the entries with a label. A feed, category or label the user does not have is 404.
- U20. Beside the stream stands the tree of what there is to read: the three streams of the interface, the categories with their feeds, the labels, each with its number of unread entries, counted over the same feeds its stream lists; the current one is marked `aria-current="page"`, a feed whose last refresh failed is marked. While only unread entries are listed and `hide_read_feeds` is not off, feeds without unread entries are left out, the current one excepted.
- U21. `state` narrows a stream to `unread`, `all`, `starred` or `unread-or-starred` entries. Without it the state follows the user's `default_view`: `all`; `unread`; `unread_or_favorite`; otherwise (`adaptive`, the default) unread entries while the stream has any, all when it has none. `/all` lists all, and with `show_fav_unread` so do `/starred` and the labels.
- U22. `sort` is `added` (the time an entry was received), `published`, `title`, `feed` or `random`; `order` is `desc` or `asc`. Without them the order is that of the feed or the category when its attributes name one (`defaultSort`, `defaultOrder`), else that of the user (`sort`, `sort_order`); the names of FreshRSS `id`, `date`, `title`, `f.name` and `rand` are understood, any other (`c.name`, `lastUserModified`, `length`, `link`) means `added`, for a feed too: its order is not replaced by that of the user. A random order is the shuffle `seed` picks (`storage.md`, S32); a page asked for without a seed picks one and names it in every address it gives out, the one actions come back to included, so that a shuffle lasts through its pages and through what the reader does on them. Links to states and to other streams keep the parameters the reader gave; a parameter with a value the interface does not know is passed over.
- U23. A page lists `posts_per_page` entries (20 when unset or not from 1 to 500) and links to the next one (`rel="next"`, parameter `after`) while there is one; pages follow each other as `storage.md`, S32, says. An `after` no page gave out is 404.
- U24. `q` is a search in the language of `search.md`, read with labels (Q15) and with the saved searches of the user (`queries`), dates in the user's time zone (`timezone`). The stream then lists what the search finds among its entries in the state shown (`storage.md`, S33). A search that cannot be read is answered with 400, a message marked `role="alert"` that says what is wrong, and no entries. The tree drops the search, the other links keep it.
- U25. An entry is listed as an `article` with its title as a heading inside the `summary` of a `details` element, which opens the entry in place without a script; with `display_posts` entries are listed open. An open entry and the page of an entry, `/entries/<id>`, show the title, the feed, the authors, the date in the user's time zone (the time the entry was received when it has no date), the text, the enclosures the text does not show already (pictures, players for sound and video, links for the rest), the tags of the feed and the labels. An entry of another user is 404.
- U26. The text of an entry is cleaned by the sanitizer of feed content once more when it is shown: an import brings entries FreshRSS cleaned, and not all of them well (`guid.md`). The link of an entry and its enclosures are shown only with an `http` or `https` address.
- U27. Handlers of `EntryBeforeDisplay` see every entry about to be shown; one they drop is left out of the list and has no page.
- U28. `POST /entries/<id>/read` (`read` 1 or 0) and `POST /entries/<id>/star` (`starred` 1 or 0) change the state of an entry, stamp the change (`storage.md`, S20) and call `EntriesRead`, when the state changed, and `EntriesFavorite`. `POST /entries/<id>/labels` gives the entry exactly the labels ticked (`label`, repeated) and the one named in `new`, which is created unless a label has the name already, be it one another request made at the same moment; a name a category has, or one longer than 191 characters, creates nothing and says so. Each sends the reader back to `next`, a path of the interface, at the entry (`#e<id>`).
- U29. `POST /read-all` marks read the unread entries of a stream (`stream`, its address) in the state and search the page showed (`state`, `q`) that were there when the page was made (`before`, an entry identifier, never later than now); with `older` `day` or `week`, only those received more than 24 hours or 7 days ago. The next page says how many entries it was. Handlers of `EntriesRead` are not called, as for `mark-all-as-read` of the API.
- U30. What an action has to say is shown once, in the live region of the page the reader lands on; it travels in the cookie `freshgo_notice` as the key of a text and a number, never as text.
- U31. A request with a parameter in its address, or a field in the form of an action, that is not text (bytes that are not UTF-8, or a NUL, which PostgreSQL does not keep) is answered with 400 and changes nothing.
- A look at a page changes nothing: opening an entry does not mark it read without a script. Marking on opening (`mark_when.article`) comes with the script.

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
- U19–U21: `TestReadingScreen` (every stream, state and order against what the store lists; counts of the tree), `TestReadingSettings`; `TestBehindProxy` for the public path.
- U20: `TestTreeCountsByPriority`. U22, U23: `TestReadingSettings` (19 entries five a page; `default_view`, `show_fav_unread`, `sort`, `sort_order`, the order of a feed, an order of a feed freshgo lacks, `hide_read_feeds`), `TestShuffleIsKept` (the pages of a shuffle, the page an action comes back to).
- U24: `TestSearchLine`. U25–U27: `TestEntryIsShown`. U28: `TestEntryActions`, `TestEntryLabels`, `TestEntryLabelsAtOnce` (eight forms naming one new label). U29, U30: `TestMarkAllRead`, `TestEntryLabels`. U31: `TestUnreadableText`.
- U15 on the reading screen: `TestVisitorOnlyReads`.
- U18: `TestUsersFromTheCommandLine`, `TestUserCreateAdmin` in `cmd/freshgo`; `TestFirstUserIsTheDefault` in `internal/store`.

## Decisions

- **Pages are rendered by the server** with `html/template`; there is no build step and no framework. Rejected: a single-page application over a JSON API, which doubles the code and does not work without a script.
- **Texts are JSON files, one per language, with CLDR plural categories**, chosen by `golang.org/x/text`. The translations of FreshRSS are not used: the interface has texts of its own.
- **Static files are told apart by a digest in the query string**, not in the file name: the files keep their names in the source tree and no step renames them.
- **The password is sent by the login form as it is**, protected by TLS, and compared with the stored hash. The challenge of FreshRSS, where the browser hashes the password with bcrypt, is not reproduced: it exists for logins without HTTPS and needs a script.
- **Logins are rows in the database**, not signed cookies: they can be ended from the server, which a change of the password does.
- **Requests from other sites are turned down by `http.CrossOriginProtection`** of the standard library; forms carry no token.
- **Failed logins are counted per name and address, in memory.** Counting per name alone would let anybody lock a user out; a restart of the server clears the counts.
- **An entry opens in place by `details`**, the element browsers open and close from the keyboard themselves; the script of the next step builds on it instead of replacing it.
- **Opening an entry does not mark it read without a script**: that would be a GET that changes something, or a button where a link belongs. The form next to the entry does it.
- **"Mark as read" is bounded by the moment the page was made**, not by the entries it listed: an entry identifier is the time the entry was received, so the moment is a bound every entry the reader could have seen is under.
- **Typing the password again before administrative actions (`reauth_time`) comes with the administrative pages**; the time of the last login is already recorded with the session.
