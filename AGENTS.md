# Agent rules for a greenfield project

Copy this file — or just its Ground rules — into a new repository as `AGENTS.md`
or `CLAUDE.md`. It assumes nothing outside the repository depends on it yet.
Once that stops being true, see "When these rules expire".

## Ground rules

**No backward compatibility.** Rename, move, and delete freely. Never leave a
deprecated shim, a `V2` duplicate, an old field kept "just in case", or a
compatibility branch in a parser. Nothing is pinned to an old version yet, so
every such leftover is dead weight that no one will come back to remove.

**Simplest implementation that fully covers the current requirements.** No
abstraction until there are three real call sites, no layer added for a feature
that is not being built now, no configuration knob nobody asked for. "Fully"
carries as much weight as "simplest": the short version must handle the stated
requirement, not a convenient subset of it.

**Proven libraries over custom implementations.** Where the answer is already
known — dates, parsers, crypto, retries, terminal rendering — take a maintained
library instead of writing one. Check that it is alive (recent release, issues
answered) and that it pays for itself: a dependency pulled in for a twenty-line
wrapper costs more in updates and audits than it saves.

## When these rules expire

The first rule dies per surface, not for the whole repository. Internals stay
free to change long after a public API has frozen. A surface is frozen as soon
as someone outside this repository can pin to it:

- a tagged release or a published package others can depend on;
- a deployed HTTP/gRPC API with clients you do not ship yourself;
- persisted data — database schema, on-disk files, message payloads already
  sitting in a queue;
- a rolling deploy, where the old and the new binary run at the same time.

From that point, changing the surface is planned work: version it, migrate in
two phases (write both, read both, then cut over), and write down when the old
path gets removed. "Leave the old thing next to the new one" is not a plan — it
is how the shims this file forbids get created.
