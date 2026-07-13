# Known issues in the `want` dependency

This platform's backend delegates real inference to `want`
(`github.com/tim72117/want`, source at `/Users/caitingyu/Documents/want`), a
separate library. Some limitations live in `want` itself, not in this
repo's code — fixing them means editing `want`, not `backend/`. Tracked here
so they aren't rediscovered from scratch.

## Editing a tool's schema doesn't take effect (confirmed bug)

**Symptom:** a developer edits an existing tool's schema in the console and
saves. `RegisterAppRole` (`backend/internal/inference/agent_roles.go`) calls
`want`'s `types.RegisterTool` again for that tool name, expecting the new
schema to apply on the next prompt — per this project's own "changes take
effect immediately, no restart needed" design (see that file's doc comment,
and `Sidebar.tsx`'s footer hint text). It doesn't: the LLM keeps seeing the
original, first-registered version of the schema for the rest of the
process's lifetime.

**Root cause:** `want`'s `types.GlobalRegistry.Declarations` is append-only,
and the tool lookup (`internal/toolbox.go`'s `GetTools`) resolves a name to
its **first** matching declaration. Re-registering the same tool name adds a
second entry that's never reached. Full writeup, including the fix options
under consideration, is in the `want` repo:
`/Users/caitingyu/Documents/want/doc/tool-registry-append-only-bug.md`.

**Status:** confirmed by reading `want`'s source (2026-07-09), not yet fixed.
No workaround from this repo's side — the registry is a package-level
singleton in `want`, so there's no way to route around it from
`onagent`'s code.

## Single shared orchestrator serializes every user's every turn

**Symptom:** under concurrent load from multiple end users (across multiple
third-party apps, plus the console's own Playground), requests queue up
behind each other rather than running in parallel.

**Root cause:** `WantService.Complete()`
(`backend/internal/inference/want.go`) holds one mutex for the entire
duration of each call (including the wait for the LLM's response, up to 90s),
because there is exactly one `*orchestrator.Orchestrator` instance for the
whole backend process. `orch.AgentID`/`orch.Role` are swapped per-call to
fake per-session/per-app isolation — this correctly isolates conversation
*content* (no cross-user leakage), but does nothing for *throughput*: only
one agent turn can run at a time, backend-wide.

**Status:** identified 2026-07-09, not yet scoped or fixed. Would require
running multiple `want.Orchestrator` instances (e.g. a pool, or one per app)
instead of one shared instance — a nontrivial change to how this platform
uses `want`, and/or to `want` itself. Note that fixing this does **not**
fix the registry bug above — both issues were confirmed to be independent:
`want`'s tool registry is a global singleton regardless of how many
orchestrator instances exist, so multiple orchestrators would still share
(and still be affected by) the same append-only `Declarations` bug.
