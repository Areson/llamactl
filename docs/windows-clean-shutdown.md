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
| Supervisor: wait for the generation to exit on its own; hard-kill (by job/tree) only after its timeout | `start-llamactl.ps1` — outside repo; **deployed 2026-10-05** |
| Servy `StopTimeout` 5 s → ~90 s | Servy config — **set to 90 s 2026-10-05** |

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

Hot-swap quirks seen during this work — **fixed** (2026-10-05):

1. **`handoff-state.json` stuck at `in_progress`.** A's final state write
   failed with "Access is denied" on every live swap, even with nothing
   polling `/hot-swap/status`. Cause: `handoffMu` only serializes within one
   process, but A and B both read/write the file; Go's `os.ReadFile` opens
   without `FILE_SHARE_DELETE`, and plain `os.Rename` (`MoveFileEx`) cannot
   replace a file that is open even *with* `FILE_SHARE_DELETE` (probed).
   Fix (`pkg/hotswap/handoff_state*.go`): readers open with
   `FILE_SHARE_DELETE`; the writer replaces via POSIX-semantics rename
   (`SetFileInformationByHandle`/`FileRenameInfoEx`, which succeeds over such
   readers), falling back to `os.Rename` where unsupported, and retries
   access-denied/sharing-violation contention for up to 2 s (older binaries,
   antivirus). The manager's duplicate reader (`cleanup_helper.go`) now uses
   the shared one.
2. **`A still alive after 60s`; `llamactl.exe.old` left behind.**
   `PIDAlive` only checked that `OpenProcess` succeeded, which stays true for
   an exited process while any handle to it is open (the supervisor holds
   one). Fix (`pid_alive_windows.go`): alive = the process handle is not yet
   signalled (exit-code fallback without `SYNCHRONIZE` access). Also corrects
   adoption's stale-PID check and the startup sweep.

Tests reproduce both with the live error messages before the fix
(`handoff_state_windows_test.go`, `pid_alive_windows_test.go`). Real-binary
hot-swap E2E after the fix: phase `complete`, no rename failure, and B
logged `Cleanup: removed old binary` ~1 s after A exited.

**Incident (2026-10-05, test-only): self-re-exec runaway.** The helper is
`os.Executable() __console-ctrl <pid>`. In another package's *test* binary
(e.g. `manager.test.exe`), that arg is not dispatched, so the helper ran the
whole test suite again, whose instance stops launched more helpers. Found
~580 `manager.test.exe`, ~800 `sh -c "sleep 999999"` test instances and
~1,900 orphaned `sleep.exe` (each with a console) on the dev box; all killed
by exact match. Production was never affected (`main` dispatches the arg).
Fix: the helper is only used once `RunConsoleCtrlHelper` has run in this
process (`helperDispatched`); otherwise stop hard-kills as before. Test:
`TestConsoleCtrlC_RefusesWithoutHelperDispatch`; `manager` suite now leaves no
processes behind (2.2 s, was 22 s). Rule: never re-exec `os.Executable()`
with a mode flag unless the binary is known to dispatch it.

Compatibility: instances started by an older binary (`DETACHED_PROCESS`, no
console) make `AttachConsole` fail → stop logs it and hard-kills immediately (no
wasted grace). Next start uses the new flags.

## Status

| Item | Status |
|---|---|
| Technique proven, session 1 and session 0 | ✅ |
| Implemented + unit tests | ✅ |
| E2E: hot-swap, then Stop an adopted instance (spawned by the new binary) | ✅ (via service-style Ctrl-C of B; fake backends) |
| E2E: Ctrl-C reaches hot-swapped B; B stops all instances cleanly | ✅ |
| Deployed `1c50766` to the live service (2026-10-05 22:28) | ✅ |
| Live: llama-server Stop (`ne-e2b-summarizer`) | ✅ `clean stop requested` → `shut down gracefully` in 1 s; llama-server logged `cleaning up before exit...` |
| Live: Tabby Stop (`qwen38-27b-exl3-tabby`) | ✅ Tabby `Shutdown signal called. Exiting gracefully.` → `Model unloaded.` — first time `unload()` ran under llamactl |
| Supervisor `start-llamactl.ps1` waits instead of killing | ✅ deployed (from the `.proposed` draft) |
| Servy `StopTimeout` raised (5 → 90 s) | ✅ |
| Live: service stop via Servy | ✅ 22:41: Servy Ctrl-C → supervisor `received the Ctrl-C` → llamactl stopped Tabby cleanly → `exited cleanly, leftover models killed=0`; no hard kills |
| Service stop held 30 s by open SSE streams | ✅ fixed; deployed by hot-swap (`db6d45c`), service stop now ~2 s |
| Live: Tabby cache persisted on Stop and restored on next load | ✅ 23:00 saved 88 pages / 5 checkpoints (1.2 GB, 1.7 s); 23:02 restored in 1.2 s, next request 62% cached |
| Hot-swap quirks: stuck `in_progress` state, `.old` left behind | ✅ verified live: `222b949`→`222b949` swap (23:31) ended `phase: complete`; B removed `.old` 1 s after A exited |
| Live: idle-timeout stop of an adopted instance | ✅ 23:28 `qwen38-27b-exl3-tabby` (adopted after hot-swap) timed out → clean stop → cache persisted (80 pages, 7 checkpoints, 1.9 s) |
| Interaction with `synchronous_group_eviction_timeout_sec` (30 s) when a victim uses most of a 30 s grace | ⬜ watch; sync wait already falls back to async on timeout |

## Live findings (2026-10-05)

**SSE streams held every service stop for 30 s.** The 22:41 service stop took
32 s: `TrackingListener: closing listener (11 tracked conns left open)`, then
`Error shutting down server: context deadline exceeded` exactly 30 s later,
and only then were instances stopped. `http.Server.Shutdown` waits for active
requests and does not cancel their contexts; `/instances/events` (SSE) only
returned on client disconnect, so any open web UI tab held shutdown to its
deadline. Fix: `Handler.CloseStreams` (registered via
`httpServer.RegisterOnShutdown`) ends SSE streams; ordinary requests,
including in-flight inference, still drain. Tests:
`pkg/server/handlers_sse_test.go` — the open-streams test fails on the old
code with the same `context deadline exceeded`.

**Tabby cache persistence vs vision.** With persistence configured, Tabby
logged `Cache persistence is not supported with vision enabled; skipping.` on
both load and unload. `qwen38-27b-exl3-tabby` has vision on, so no save. The
stop path itself is fine. To see a save: test with vision off, or extend
`cache_persist.py` (vision is excluded deliberately — image embeddings are not
part of the persisted state).

**Stop before the Servy timeout change (22:37).** With `StopTimeout` still 5 s,
Servy logged `Graceful shutdown not supported or timed out. Forcing kill`
for conhost and llamactl 5 s in. No model was running, so nothing was lost;
this is why the timeout must exceed llamactl's drain + grace.

## Not pursued

- Backend HTTP shutdown/unload endpoints (e.g. Tabby `POST /v1/model/unload`):
  simpler but backend-specific; the goal was a generic mechanism.
- Named-event + in-process shim watcher: works only for backends we can inject
  code into (Python via `sitecustomize`).
- `CreateRemoteThread` into `kernel32!CtrlRoutine`: fragile, AV-hostile.
- `WM_CLOSE`: only for GUI apps; none of our backends are.
