# Hot-swap continuation brief

**Audience:** another agent picking this up cold.  
**Repo:** `E:\OpenClaw_Projects\llamactl-src`  
**Date of last work:** 2026-09-02 / 2026-09-03  
**Do not commit** `poc/`, `docs/hot-swap.md` (design), `build/`. This file (`docs/hot-swap-next.md`) is the handoff; include it if Ian wants it in git.

---

## 0. Current state (read this first)

### Git

- Branch: `feature/model-throughput-stats`
- **Committed v1:** `c034391` — `feat: Windows hot-swap of llamactl without stopping model children`
- **Uncommitted product fix (must keep):** `pkg/manager/hotswap.go` — spawn B with **A’s cwd** and rewrite `LLAMACTL_CONFIG_PATH` to an **absolute** path (`withAbsConfigPath`). Without this, B misses the test/prod config and binds the default port (`:8080`).
- **Uncommitted, never in a product commit:** `scripts/hot-swap-e2e.ps1` (E2E). Absolute paths for config/data/exe/swap body were added after the first E2E run. `docs/hot-swap.md`, `poc/`, `build/` stay untracked.

### What v1 is supposed to do

1. Duplicate **live client** TCP sockets from A → B via `WSADuplicateSocketW` + `WSASocketW`.
2. A closes the public listener, then **exits without** `instanceManager.Shutdown()` (that would kill llama-server / VRAM).
3. B retries `net.Listen` on the public port (rebind, ~110 ms empty-port window on this host).
4. B injects adopted client conns via `server.NewChainedListener`.
5. Model children are adopted from `runtime.json` (PID + `/health`).
6. Windows children are spawned **without parent pipes** (stdin=`NUL`, stdout/stderr=log file) so A dying does not EOF-kill them.

### What the first E2E actually showed (2026-09-02, port 18079, scratch DB)

| Step | Result |
|---|---|
| CGO builds | Both ~24.3 MB (CGO on) |
| v1 up | Ready, PID 62564 |
| `POST /api/v1/hot-swap` | HTTP 200 in 243 ms |
| A exit | **Works.** Closed listener, skipped instance Shutdown, logged `Exiting llamactl (hot-swap).` |
| Client socket DUP | **Fail.** B: `FileConn failed … The parameter is incorrect.` (fds 544 and 556) |
| B on `:18079` | **Fail.** B (PID 51644) stayed as `llamactl-candidate.exe` listening on **`:8080`** (default config). Relative `LLAMACTL_CONFIG_PATH=build\test-config.yaml` + `cmd.Dir = exe dir` (`build\`) made B look for the yaml in the wrong place. |
| Status poll | Never saw `phase=complete` on 18079. Script was killed; B was killed. |

A’s exit path is done. Rebind is still the right v1 plan. Two blockers remain for a green E2E: **(1) B must load the same config as A** (code exists, uncommitted), **(2) wrapping an adopted SOCKET in `net.FileConn` still fails.**

### Skills / build

- Build: `E:\OpenClaw\main\skills\llamactl-build\SKILL.md` — `CGO_ENABLED=1`, `CC=C:\Tools\mingw64\bin\gcc.exe`, `GOCACHE=E:\OpenClaw\tmp\llamactl-gocache`, target `./cmd/server`. ~24 MB = CGO on; ~23 MB = CGO off.
- Config is **`LLAMACTL_CONFIG_PATH`**, not `--config`. A second process with a bad/missing path opens the **live** DB at `C:\ProgramData\llamactl\llamactl.db`. Never do that.
- YAML durations must be strings (`0s`, not `0`). Paths in YAML: forward slashes.
- Socket probes: `E:\OpenClaw\main\skills\windows-process-socket-probes\SKILL.md`. POC that **did** work for connected sockets (raw `recv`/`send`, not `FileConn`): `poc/socket_handoff/`.

### Key files

| Path | Role |
|---|---|
| `cmd/server/main.go` | `--hot-swap` → `HotSwapB()` then rebind; `hotSwapExit` skips Shutdown |
| `pkg/manager/hotswap.go` | A-side: spawn B, DUP clients, rename exe, close listener |
| `pkg/manager/hotswap_b.go` | B-side: READY/DUP/BYE, `AdoptSocket` + `FileConn` |
| `pkg/hotswap/ws2/ws2.go` | `WSADuplicateSocketW`, `WSASocketW` with `WSA_FLAG_OVERLAPPED`, blob size **624** |
| `pkg/server/tracking_listener.go` | Track conns; `SocketHandles()` via `SyscallConn` |
| `pkg/server/chained_listener.go` | Adopted conns then inner listener |
| `pkg/instance/process.go` | `runtime.json` write; Windows no-pipe spawn |
| `pkg/instance/runtime_state.go` | Pidfile schema |
| `scripts/hot-swap-e2e.ps1` | Isolated `:18079` E2E |

---

## 1. Finish v1 E2E (B serving + script completes)

Do these in order. Do not start listener inheritance (v2) until a v1 E2E pass gets past “B is listening on the test port and `/api/v1/version` works after the swap.” In-flight socket continuity can still fail; that is a separate sub-goal.

### 1.1 Land the config-path spawn fix

File: `pkg/manager/hotswap.go` (already edited, not committed).

- `cmd.Dir` = `os.Getwd()` (A’s cwd), not the exe directory.
- `cmd.Env = withAbsConfigPath(os.Environ())` so `LLAMACTL_CONFIG_PATH` is absolute.

Also keep E2E using **absolute** `$ConfigPath`, `$DataDir`, `$LogsDir`, v1/v2 exe paths, and swap `binary_path` (already in `scripts/hot-swap-e2e.ps1`). v1 start: `WorkingDirectory = $RepoRoot`.

**Verify:** after swap, B’s stderr must say `Read config at <abs>\build\test-config.yaml` (or equivalent) and bind `127.0.0.1:18079`, **not** `:8080`.

### 1.2 Capture B’s logs (script gap)

The E2E only redirects **v1** stdout/stderr. B is spawned by A (`llamactl-candidate.exe`) with no redirects, so FileConn / listen errors vanish.

- Redirect B somehow, or after a failed run inspect whatever A logged (`HotSwap: B failed to adopt socket: …` is A’s view of B’s `GOT err=`).
- After spawn, `Get-Process llamactl-candidate` + `Get-NetTCPConnection` on `$Port` vs 8080 is the fastest “which config did B load?” check.

### 1.3 Fix `net.FileConn` on adopted sockets

This is the remaining **product** bug for in-flight connections.

**Symptom (measured):** `ws2.AdoptSocket` returns a handle (544, 556). Then:

```text
fileconn: file file+net adopted-conn-544: The parameter is incorrect.
```

(`os.NewFile` + `net.FileConn` in `pkg/manager/hotswap_b.go`.)

**What already works:** `poc/socket_handoff/` — A duplicates a **connected** socket; B calls `WSASocket` and uses **raw** `recv`/`send`. Do not treat the POC as proof that `FileConn`/`http.Serve` work.

**What to try (smallest first):**

1. **Probe in isolation** (new tiny program under `poc/` is fine; do not ship it). Adopt a connected socket, then try:
   - `os.NewFile` + `net.FileConn` (current, known fail)
   - `net.FileConn` after `syscall.SetNonblock` / overlapped already set
   - wrapping via `windows.SetFileCompletionNotificationModes` if Go’s poller rejects the handle
   - serving with raw Winsock accept/read only as a diagnostic, not the product path
2. **Do not use `TCPConn.File().Fd()` on A.** v1 already uses `SyscallConn().Control` in `TrackingListener.SocketHandles()`. Keep it that way; `File().Fd()` dups and can flip blocking **in A**.
3. **Keep the `*os.File` alive** if FileConn ever succeeds (already attempted). On Windows GC closing that file kills the SOCKET.
4. **`WSASocket` flags:** already `WSA_FLAG_OVERLAPPED` (`0x01`). Blob size already **624** (`WSAPROTOCOL_INFOW`). If you change Unicode vs ANSI, `WSADuplicateSocketW` and `WSASocketW` must stay a pair.
5. If FileConn cannot be made to work: **v1 can still ship without in-flight continuity.** Drop DUP of client sockets, only rebind the port, document the ~110 ms `ECONNREFUSED` + dropped SSE. OpenClaw should retry connection errors. That is an acceptable v1 if Ian agrees; the E2E currently opens SSE before swap and will warn, not necessarily fail, on stream errors.

**Do not** call Winsock `listen()` on a duplicated **connected** socket.

### 1.4 Status endpoint vs E2E “complete”

E2E polls `GET /api/v1/hot-swap/status` until `phase == complete`.

B’s `HotSwapB` currently **deletes** `handoff-state.json` after BYE (`hotswap.RemoveHandoffState`). After a successful swap the status handler returns `{phase: idle}`. The poller will **never** see `complete` even if B is healthy on 18079.

**Fix one of:**

- Keep `phase: complete` on disk until B’s first successful serve (or a TTL), **or**
- Change E2E success to: port accepts, `GET /version` 200, optionally `GET /instances` — treat missing/idle status as OK once the API is back, **or**
- Have A leave the file as complete and have B **not** delete it until cleanup of `.old`.

Also: E2E loop does `$Waited++` every 500 ms with `$MaxWait = 60` → **30 seconds**, not 60. B’s rebind retry is up to 30 s; this is tight. Use wall-clock timeout (e.g. 60 s).

### 1.5 Binary rename vs E2E process names

A copies the new exe to `llamactl-candidate.exe` next to **A’s** exe, then:

- `os.Rename(aExe, aExe+".old")` → e.g. `llamactl-v1.exe.old` (not `llamactl.old.exe`)
- `os.Rename(candidate, aExe)` → candidate becomes `llamactl-v1.exe` while B is still the process that started from `llamactl-candidate.exe`

B’s `cleanupOldBinary` uses `os.Executable() + ".old"`. After rename, `os.Executable()` on Windows often still reports the **original** path (`…\llamactl-candidate.exe`), so cleanup looks for `llamactl-candidate.exe.old` and **misses** `llamactl-v1.exe.old`.

E2E was updated to look for `llamactl-v1.exe.old`. Align product cleanup with the **canonical** path A renamed to, not `os.Executable()` after a rename.

Leftover `llamactl-candidate.exe` **process** after a failed bind is expected until killed; teardown must `Stop-Process` by path under `build\`, not `Get-Process llamactl` (that can hit **production**).

### 1.6 How to run one E2E pass

```powershell
cd E:\OpenClaw_Projects\llamactl-src
# Do not use the live DB. Script isolates to build/data.
powershell -ExecutionPolicy Bypass -File .\scripts\hot-swap-e2e.ps1
```

Default port **18079**. After failure:

- `build\v1-stderr.log`, `build\v1-stdout.log`
- `Get-NetTCPConnection -LocalPort 18079,8080`
- `Get-Process llamactl-v1,llamactl-v2,llamactl-candidate -ErrorAction SilentlyContinue`

Kill **only** processes whose `Path` is under `build\`. Production llamactl on `:8079` must stay up.

Fast compile check (no CGO): `go test ./pkg/hotswap/ ./pkg/instance/ ./pkg/server/; go build -o NUL ./pkg/manager/; go build -o NUL ./cmd/server/` with `GOCACHE` set. Full exe: CGO env from the build skill.

### 1.7 v1 E2E done when

- [ ] B binds the **test** port (18079), not 8080.
- [ ] After swap, `GET /api/v1/version` and `GET /api/v1/instances` succeed on that port.
- [ ] A is gone; model children (if any were started) still have their PIDs (empty E2E has no models — optional follow-up: start a dummy listener as a fake llama-server and assert adoption).
- [ ] Script reaches “E2E test complete” without throwing.
- [ ] In-flight SSE: either stays up (FileConn fixed) or is explicitly documented as dropped for v1.

---

## 2. v2 — hand off the **listening** socket (no rebind)

Do this only after v1 E2E shows B serving on the correct port. Listener inheritance removes the ~110 ms `ECONNREFUSED` window.

### Goal

A duplicates the **listening** SOCKET on the public port to B. B adopts it and `http.Serve`s on that listener. A closes its copy and exits. No `net.Listen` race, no TIME_WAIT fight.

### Why v1 did not do this

An earlier attempt mixed listener DUP with client DUP (“first DUP is the listener”), used `net.FileListener`, called `listen()` on the adopted fd, and type-asserted `AdoptedListener()` on the manager — that method was a **package function**, so B always fell through to rebind. `FileListener` never got a fair test. v1 deliberately dropped listener DUP.

Connected-socket POC ≠ listening-socket + `http.Serve`.

### Implementation steps

1. **Typed protocol.** Distinct verbs, e.g. `LISTENER <b64>` vs `DUP <b64>` (or `CONN`). Never “first message is the listener.” If listener extraction fails, do not DUP a client as a listener.

2. **Extract listener FD on A** via `TCPListener.SyscallConn().Control` (already the correct Windows path). `net.TCPListener.File().Fd()` is **not** the Winsock SOCKET (unlike `TCPConn` on some builds). Store it on `TrackingListener` (`ListenerFD()` existed in an earlier draft; v1 removed it — add it back).

3. **Duplicate with the same ws2 helpers** as clients: 624-byte W blob, `WSADuplicateSocketW` / `WSASocketW` + `WSA_FLAG_OVERLAPPED`.

4. **Do not call `listen()` on a duplicated listening socket.** A listening socket stays listening across `WSADuplicateSocket`. `listen()` without bind on a wrong/fresh socket is how you get a handle that is not on `:8079`. If `WSASocket` yields a non-listening socket, that is a duplication bug — fix flags/blob, don’t `listen()` blindly.

5. **Wrap for `http.Serve`:** `os.NewFile` + `net.FileListener`, **retain `f`**. Same FileConn class of bug may appear; solve wrapping once for connected sockets first, then reuse for the listener.

6. **`AdoptedListener()` must be a method** on `instanceManager` (or a small interface main already type-asserts). Package-level `var adoptedListenerFD` + `func AdoptedListener()` will fail `instanceManager.(interface{ AdoptedListener() syscall.Handle })`.

7. **B listen order:** if adopted listener FD ≠ 0, wrap and serve; **do not** also `net.Listen` the same port. Remove the 30 s rebind loop for the v2 path (keep as fallback only if DUP of the listener fails and A will close).

8. **A close order:** DUP listener and clients → BYE/ACK → rename binaries → **then** close A’s listener (so B already holds a listening duplicate) → signal `hotSwapExit`. If A closes first, there is a gap; if A never closes, two processes share the listener (Windows `SO_EXCLUSIVEADDRUSE` may make that fail anyway).

9. **No `WSACleanup` in `HotSwapB`.** Current code already omits it; keep it that way. Cleanup before `http.Serve` races Go’s net poller.

10. **TrackingListener on B:** wrap the adopted listener so a later swap can DUP again.

### v2 tests

- Extend `poc/socket_handoff` (or a new probe) to DUP a **listening** socket, have B `accept` one client, then wrap with `FileListener` + a trivial `http.Server`.
- E2E: hold an HTTP request open across the swap; client must not see `ECONNREFUSED` on **new** connections during T19–T22 (there should be no such window).
- Failure fallback: if listener DUP fails, log and use v1 rebind.

---

## 3. Other holes (address after E2E is green, or sooner if they bite)

These were found in review / first E2E. Not all are blockers for “B answers on 18079.”

### Process / lifetime

- **`os.Exit(0)` from a goroutine is not blocked by `<-stop`.** That was a misdiagnosis. A now signals `hotSwapExit`; main returns without `Shutdown()`. Do not add `TerminateProcess` folklore. B used to try to kill A **after deleting** `handoff-state.json`, so `getHotSwapAPID()` was always 0 — that kill path was removed; don’t bring it back.
- **Swap mutex is held until A exits** (`releaseOnReturn = false` on success). Start/stop/restart return `ErrHotSwapInProgress` → HTTP 503 + `Retry-After: 2`. Confirm UI toasts/retries if you care about dashboard clicks during swap.
- **`DETACHED_PROCESS` on every model child** is required for survival; `GenerateConsoleCtrlEvent` is then theater. Non-adopted stop must remain stdin-EOF (Unix) or `TerminateProcess` / force-kill (Windows). Adopted stop is PID-based (`stop_process_windows.go`).

### Sockets / protocol

- **`WSAPROTOCOL_INFO` “544 bytes” in the POC is the allocated buffer, not the Windows struct.** Product uses 624. Don’t shrink it.
- **ChainedListener + TrackingListener:** B wraps chained then tracking. `Accept` of an already-adopted conn should still register in the tracker for the next swap.
- **Handoff protocol is line-based over TCP.** Large base64 blobs are fine; don’t add a second control channel.

### Persistence / adoption

- **`WriteRuntimeState` is called from `process.start` after spawn.** Confirm it runs on Windows no-pipe path (it should; `writeRuntimeState()` is after `cmd.Start()`).
- **`Adopt()` probes `/health` and `attachTail`.** `attachTail` now only reopens timber for UI logs; the child writes the file itself. `log_offset` is not updated periodically (`UpdateLogOffset` exists but is unused in the hot path).
- **SQLite:** B opens the same DB while A may still be flushing. WAL is the usual mode; if the live DB is exclusive, B’s `Open` will fail. E2E uses a scratch DB. Confirm production journal mode before a live swap.
- **`startupSweep` instance dir:** `filepath.Join(dataDir, "instances")` may not match `globalConfig.Instances.InstancesDir`. Use the real instances dir.

### Binary swap / cleanup

- Rename is `aExe+".old"` (`llamactl.exe.old` / `llamactl-v1.exe.old`), not `llamactl.old.exe`. Docs and E2E must match code.
- B cleanup via `os.Executable()+".old"` is wrong after rename (see §1.5).
- A `defer os.Remove(candidatePath)` runs on success after rename-away; on Windows that remove of a missing path is fine. If rename of candidate fails, A tries to restore `oldPath` → `aExe`.

### Config / API / UI

- **`GET /config` is the raw sanitized config** (not wrapped). Hot-swap support is `GET /hot-swap/status` (`phase != "unsupported"`). Do not re-wrap `/config` — it broke settings.
- Web UI is **`//go:embed dist/*`**. Changing `webui/src` without `npm run build` then CGO rebuild does not change the served UI. E2E does not need the new button.
- `HotSwapDialog` must keep the loading gate so Radix doesn’t crash on `candidates.length` before fetch.

### E2E / safety

- Script teardown `Get-Process llamactl` is dangerous if it is not filtered by `build\` path. Keep the filter.
- No model instance is started in the current E2E, so **adoption is untested**. Add a later pass with a fake backend (e.g. a tiny HTTP `/health` process) or a real llama-server if Ian allows GPU use.
- `timeout_check_interval: 0` in YAML is an **int** field (minutes), OK. `connection_max_lifetime: 0s` is a duration, must stay a string.

### OpenClaw (harness) — out of this repo

- Retry `ECONNREFUSED` / truncated SSE as transient, not as tokenize/format errors. Needed for v1 rebind window even if llamactl is perfect.

---

## 4. Suggested work order for the next agent

1. Commit or stash-review the `hotswap.go` config-path fix; confirm E2E script abs paths.
2. Run E2E; expect B on **18079**. If still on 8080, B did not inherit config — fix spawn env/cwd before anything else.
3. If FileConn still errors: isolate in a probe; either fix wrapping or (with Ian) drop in-flight DUP for v1.
4. Fix status `complete` vs deleted state file so the script can finish.
5. Fix `.old` cleanup path vs `os.Executable()`.
6. Optional: dummy instance adoption in E2E.
7. Only then implement §2 (listener handoff).

**Do not** hot-swap the **production** `:8079` process until E2E is green on 18079 with a scratch DB.
