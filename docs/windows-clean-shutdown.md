# Windows clean shutdown for instances — findings and design

**Repo:** `E:\OpenClaw_Projects\llamactl-src`
**Branch:** `feat/windows-clean-stop` (from `feat/start-group-eviction` @ `83bb635`)
**Date:** 2026-10-05
**User-facing docs:** `managing-instances.md` → *How a Stop Works*

## Why

On Windows every instance Stop was a hard kill: in-flight drain, then a 5 s
grace that nothing could end early, then `TerminateJobObject`. Backends never
ran their shutdown code. Concrete casualty: TabbyAPI's prompt-cache
persistence (local patch in `E:\Model Cache\tabbyapi\tabbyAPI`, saved from
`ExllamaV3Container.unload()`) never fired on a llamactl Stop — real sessions
ended mid-log with no `Persisted prompt cache` line, while test runs that went
through Tabby's own unload saved 2.6 GB in ~3.3 s.

We want a **generic** clean stop (no backend HTTP endpoint required), with a
hard kill only after a timeout.

## Why the earlier attempt (`c13238a`) failed

`ce87038` gave children a hidden console and called, from llamactl itself,
`GenerateConsoleCtrlEvent(CTRL_C_EVENT, childPID)`. `c13238a` removed it as
"non-functional in a service session". The recorded symptoms are all explained
by the method, not by session 0:

| Symptom in `c13238a` | Actual cause |
|---|---|
| Event to child PID fails, Win32 87 | The 2nd arg is a **process-group ID on the caller's own console**, not a PID. The child was on a different console. |
| Shared-console group event reaches the caller, not the child | Same: the caller can only signal its own console. |
| `AttachConsole` access-denied | `AttachConsole` fails while the caller **already has a console**. Must `FreeConsole` first — not safe inside llamactl. |
| (not recorded) handler never runs even when delivered | **Inherited "ignore Ctrl-C" flag.** `CREATE_NEW_PROCESS_GROUP` sets it; children inherit it; with it set, `CTRL_C_EVENT` is delivered but no handler runs. llamactl spawned children with that flag, and hot-swap B is itself spawned with it. |

## Technique that works

1. **Spawn** the backend with `CREATE_NO_WINDOW` (its own hidden console) and
   **without** `CREATE_NEW_PROCESS_GROUP`. Before spawning, the parent calls
   `SetConsoleCtrlHandler(NULL, FALSE)` once to clear its own inherited ignore
   flag so the child starts with Ctrl-C enabled.
2. **Stop**: run a short-lived, console-less helper (`DETACHED_PROCESS`) that does
   `FreeConsole` → `AttachConsole(pid)` → `SetConsoleCtrlHandler(NULL, TRUE)` →
   `GenerateConsoleCtrlEvent(CTRL_C_EVENT, 0)` → `FreeConsole`. Group `0` = every
   process attached to that console, i.e. the backend and anything it re-exec'd
   (venv `python.exe` stub → real python → workers).
3. Wait up to the grace period for exit; then hard-kill the tree as before.

The helper is llamactl itself: `llamactl.exe __console-ctrl <pid>`. It needs only
the PID, so it works for instances adopted after a hot-swap.

How runtimes see it: Python → `SIGINT` (uvicorn graceful shutdown, then Tabby's
`unload_model`), Go → `os.Interrupt`, Node → `SIGINT`, llama-server → its
`SetConsoleCtrlHandler` (handles `CTRL_C_EVENT` only — not BREAK). Programs with
no handler get the default handler → `ExitProcess`, still gentler than
`TerminateProcess`.

Why Ctrl-C and not Ctrl-Break: Break ignores the inherited flag, but llama-server
does not handle it, and Python maps it to `SIGBREAK`, which uvicorn catches and
then re-raises after its shutdown — Tabby registers no `SIGBREAK` handler, so the
re-raise kills the process before `unload_model` runs.

## Evidence (prototype)

Prototype: a Go runner + `child.py` (venv python parent that `Popen`s a python
grandchild; both trap SIGINT, simulate a 2 s save, exit 0). Core of the helper:

```go
procFreeConsole.Call()
if r, _, err := procAttachConsole.Call(uintptr(pid)); r == 0 { /* exit 2 */ }
procSetConsoleCtrlHandler.Call(0, 1)          // ignore it ourselves
procGenerateConsoleCtrlEvent.Call(CTRL_C_EVENT, 0) // whole console
procFreeConsole.Call()
```

| Run | Session | Result |
|---|---|---|
| `CREATE_NO_WINDOW`, ignore flag cleared | 1 (interactive) | ✅ parent + grandchild got SIGINT, saved, exit 0 in 2.1 s |
| hidden `CREATE_NEW_CONSOLE`, ignore flag cleared | 1 | ✅ same |
| `CREATE_NO_WINDOW`, ignore flag **not** cleared (control) | 1 | ❌ no handler ran; killed after 15 s |
| `CREATE_NO_WINDOW`, ignore flag cleared, as SYSTEM scheduled task | **0** (same as the Servy service) | ✅ parent + grandchild got SIGINT, clean exit in 2.1 s |

## Implementation (this branch)

| Piece | Where |
|---|---|
| Spawn flags `CREATE_NO_WINDOW`; clear inherited ignore flag once | `pkg/instance/process_group_windows.go` (`setProcAttrs`) |
| `signalStop` → console Ctrl-C (Windows) / SIGINT (Unix); returns error | `process_group_windows.go`, `process_group_unix.go` |
| Helper `__console-ctrl <pid>`, `sendConsoleCtrlC`, `waitPIDExit` | `pkg/instance/console_ctrl*.go`; dispatched first thing in `cmd/server/main.go` |
| Owned stop: request → wait `graceful_stop_timeout_sec` → `killTree`; request failure → immediate hard kill | `process.go` `stop()` |
| Adopted stop: request → `waitPIDExit` → `stopAdoptedProcessTree` only if still alive | `process.go` `stopAdopted()` |
| Config `instances.graceful_stop_timeout_sec` (default 30, env `LLAMACTL_GRACEFUL_STOP_TIMEOUT_SEC`); replaces the hard-coded 30 s Unix / 5 s Windows | `pkg/config` |
| Tests: real spawn flags + real helper (test binary doubles as helper and Ctrl-C child via `TestMain`); no-console fails fast; adopted path | `pkg/instance/console_ctrl_windows_test.go` |

## Service stop and hot-swap: llamactl itself on Ctrl-C

How the service stops (observed 2026-10-05): Servy (`StopTimeout` 5 s) sends
Ctrl-C to the console of `start-llamactl.ps1` (in `E:\Model Cache\llamactl`).
llamactl, started by the supervisor with `-NoNewWindow`, shares that console
and already runs its full shutdown on `os.Interrupt` (`main.go`). But:

- the supervisor POSTs a nonexistent `/api/v1/shutdown`, then hard-kills
  llamactl ~2 s later and `TerminateProcess`es each model's root PID from
  `runtime.json` (root only — Tabby's real python can be orphaned);
- a hot-swapped generation was spawned `DETACHED_PROCESS |
  CREATE_NEW_PROCESS_GROUP`: no console, so Servy's Ctrl-C never reached it.

Fix without any endpoint:

| Piece | Where |
|---|---|
| B inherits A's console (no creation flags); `CREATE_NO_WINDOW` if A has none (avoids a visible new console window) | `pkg/hotswap/hotswap_a.go` (`bCreationFlags`) |
| `ClearInheritedCtrlCIgnore()` at llamactl startup, so llamactl itself honours Ctrl-C however it was launched | `cmd/server/main.go` |
| Supervisor: wait for the generation to exit on its own; hard-kill (by job/tree) only after its timeout | `start-llamactl.ps1` — **outside repo, drafted, not applied** |
| Servy `StopTimeout` 5 s → ~90 s | Servy config — **not applied** |

A console outlives any one attached process, so B survives A's exit exactly
as it did detached. Models stay on their own consoles, so the service Ctrl-C
does not hit them directly; llamactl stops each with drain + grace.

Evidence:
- Prototype: A (on a console) spawns B with default flags and exits; Ctrl-C on
  the console via B's PID → B gets SIGINT, clean exit.
- **E2E with the real binary** (scratch runner, port 18079, isolated data dir):
  v1 on its own console (runner's ignore flag deliberately *not* cleared) →
  `fake-a` instance → hot-swap to v2 (B adopts `fake-a`) → `fake-b` started by
  B → `llamactl __console-ctrl <B pid>` → B exited on its own in 1.2 s; both
  fakes logged `received interrupt; saving` / `clean exit`; llamactl logged
  `clean stop requested` and `shut down gracefully` for both (adopted and
  owned paths). No hard kills.

Seen during the E2E (pre-existing, not addressed): A's final rename of
`handoff-state.json` can fail with "Access is denied" while something polls
`/hot-swap/status`, leaving `phase` stuck at `in_progress` after a successful
swap.

Compatibility: instances started by an older binary (`DETACHED_PROCESS`, no
console) make `AttachConsole` fail → stop logs it and hard-kills immediately (no
wasted grace). Next start uses the new flags.

## Status

| Item | Status |
|---|---|
| Technique proven, session 1 and session 0 | ✅ |
| Implemented + unit tests | ✅ |
| E2E: llamactl Stop on a real Tabby instance → `Persisted prompt cache` in Tabby log | ⬜ needs a free GPU / a llamactl run on a test port |
| E2E: llama-server instance clean stop (log shows its shutdown path) | ⬜ |
| E2E: hot-swap, then Stop an adopted instance (spawned by the new binary) | ✅ (via service-style Ctrl-C of B; fake backends) |
| E2E: Ctrl-C reaches hot-swapped B; B stops all instances cleanly | ✅ |
| Supervisor `start-llamactl.ps1` waits instead of killing | ⬜ drafted (`start-llamactl.ps1.proposed`), needs review |
| Servy `StopTimeout` raised | ⬜ needs admin change |
| Interaction with `synchronous_group_eviction_timeout_sec` (30 s) when a victim uses most of a 30 s grace | ⬜ watch; sync wait already falls back to async on timeout |

## Not pursued

- Backend HTTP shutdown/unload endpoints (e.g. Tabby `POST /v1/model/unload`):
  simpler but backend-specific; the goal was a generic mechanism.
- Named-event + in-process shim watcher: works only for backends we can inject
  code into (Python via `sitecustomize`).
- `CreateRemoteThread` into `kernel32!CtrlRoutine`: fragile, AV-hostile.
- `WM_CLOSE`: only for GUI apps; none of our backends are.
