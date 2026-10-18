# Daily digest

Status: implemented.
Sources: GitHub issue 138 and its decisions (a daily e-mail digest of unread entries, a per-user switch, offered only with SMTP and a mail address); the letters of `web.md` (U80, U81, U106).

## Purpose and scope

`internal/digest` writes one letter a day to the users who ask for it, with the unread entries that arrived since the last one. It sends through the sender of `internal/mail` and takes its texts from the languages of the interface (`internal/web/i18n`). `serve` runs it beside the refresh of the feeds, and only where `-smtp-url` is set.

Out of scope: another period than a day, a choice of feeds or categories, HTML letters, a link back to the interface, an unsubscribe link (the switch is in the profile, U109).

## Requirements

### Switch

- D1. A user turns the digest on in the profile (`web.md`, U109), which stores `digest` in the settings of the user. The switch is offered only while the server has an SMTP server and the user has an address (`mail_login`). A form that asks for it without either stores it off, so an address emptied by the same form takes the digest away.
- D2. Turning the digest on from off stores `digest_sent`, Unix seconds, as the start of the first period. Saving the profile with the digest already on leaves it as it is.

### Sending

- D3. The server looks at the users when it starts and every hour after that, until it stops. A pass that fails is logged and the next one still happens.
- D4. A user gets a digest when `digest` is on, the user is not kept out (`enabled`), has an address, and that address is not waiting for confirmation (`email_validation_token` empty), and at least 24 hours have passed since `digest_sent`. A user with the digest on and no `digest_sent` gets it set to now and no letter, so that the first letter does not carry a backlog.
- D5. The letter is one plain-text message to `mail_login`. Its entries are the unread ones of the reading list of the user (`greader-api.md`, A13: feeds of priority 0 and above) whose identifier, the time the entry was added, lies from `digest_sent` up to the time of the pass; entries read, of other users, of hidden feeds, and from before the period are not in it.
- D6. Each entry takes a line `feed: title` and, when it has one, a line with its link; the title of an entry without one is a placeholder. The newest come first. At most 100 are named; a line says how many more there are. The subject and the other lines are in the language of the user (`language`, then that of the installation), in the forms of the language for the number of entries.
- D7. With no entries in the period no letter goes out. After a letter that went out, or none that was due, `digest_sent` becomes the time of the pass: the next period starts there.
- D8. A letter that cannot be sent is logged and leaves `digest_sent` as it is; the next pass tries again for the same period, and the entries that came meanwhile join. One user's failure does not hold up the others.

## Decisions

- **The period starts when the switch is turned on**, not at the time of the last entry, so a user who turns the digest on does not get what they have not read since last year.
- **The period ends at the pass, not at the 24-hour mark**: a pass an hour late shifts the next letter by that hour, and nothing is sent twice or skipped. The next period starts exactly where the last ended.
- **An address that is yet to be confirmed gets nothing**, as it gets no password reset (U106): a letter must not go to an address nobody has shown to be the user's.
- **No knob for the hour or the interval**: the look every hour is a constant of `serve`.
- **The time of the last digest is a setting of the user** (`digest_sent`), changed through `UpdateUserSettings`, so it needs no table and is carried by backups and exports of the settings.

## Verification scenarios

`internal/digest`, on SQLite and, under `make test-integration`, on PostgreSQL; the clock is the `Now` of the options and the letters are taken by the SMTP server of `internal/mail/mailtest`.

- D4–D7: `TestOneLetterForThePeriod` (read, hidden, another user's and earlier entries absent; none before 24 hours; none on a second look within the day; only the newer after it), `TestNoLetterWithoutNewEntries`, `TestWhoGetsNothing` (switch off, no address, unconfirmed address, kept out, no start yet).
- D6: `TestLongList` (a hundred named, the rest counted), `TestLetterInTheLanguageOfTheUser`.
- D8: `TestFailedLetterIsRetried`.
- D1, D2: `TestDigestSwitch`, `TestDigestNeedsLetters` in `internal/web`.
- D3: not run in a test: the loop is `Schedule`, which calls `Run` on a ticker.
