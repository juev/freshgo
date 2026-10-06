# Web interface

Status: in progress. Implemented: the frame of every page, texts in two languages, static files, error pages (U1–U8). The rest of the interface is added step by step, see `plan.md`.
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

## Decisions

- **Pages are rendered by the server** with `html/template`; there is no build step and no framework. Rejected: a single-page application over a JSON API, which doubles the code and does not work without a script.
- **Texts are JSON files, one per language, with CLDR plural categories**, chosen by `golang.org/x/text`. The translations of FreshRSS are not used: the interface has texts of its own.
- **Static files are told apart by a digest in the query string**, not in the file name: the files keep their names in the source tree and no step renames them.
