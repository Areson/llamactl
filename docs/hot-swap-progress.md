# Hot-swap progress tracker

**Repo:** `E:\OpenClaw_Projects\llamactl-src`
**Started:** 2026-09-03
**Companion docs:** `hot-swap.md` (design), `hot-swap-next.md` (full handoff brief)

This file is the working reference for the next steps. Update the status column as items complete.

---

## State

| Item | Status | Notes |
|---|---|---|
| v1 committed (`c034391`) | ✅ done | `feature/model-throughput-stats` branch |
| A's exit path (close listener, skip Shutdown, log exit) | ✅ works | Proven in first E2E (09-02) |
| B spawns detached, model children survive | ✅ works | `DETACHED_PROCESS`, no parent pipes |
| `runtime.json` adoption path | ✅ in code | `pkg/instance/process.go`, `runtime_state.go` |
| Config-path spawn fix (`hotswap.go` uncommitted) | ⬜ uncommitted (shim, by design) | `withAbsConfigPath` + `cmd.Dir = os.Getwd()` — leave uncommitted; not needed in production |
| E2E script absolute paths | ✅ verified | `scripts/hot-swap-e2e.ps1` — config/data/logs/exe/swap paths all absolute; v1 `WorkingDirectory = $RepoRoot`; `LLAMACTL_CONFIG_PATH` set for v1's env |
| B binds test port `:18079` (not `:8080`) | ✅ **PASS (09-03)** | E2E run: A log `Read config at E:\...\build\test-config.yaml`; B served `/version` on 18079, 8080 empty |
| B logs visible in E2E | ✅ fixed (script) | B is detached — no direct capture. A's `v1-stderr.log` is the source (A logs B's `GOT err=` verbatim); script now dumps it after swap and on not-ready failure |
| Teardown kills B (orphan) | ✅ fixed (script) | Was `Get-Process llamactl-v*` (missed `llamactl-candidate.exe`); now kills by path under `build\`, production-safe |
| `net.FileConn` on adopted sockets | ✅ **root cause found** | Go Windows netpoller can't IOCP-associate WSADuplicateSocket-imported sockets. `net.FileConn`/`net.FileListener` unusable. B must use raw Winsock. See Step 3 |
| Status endpoint `phase=complete` reachable | ⚠️ unexpected pass | E2E reached "Swap completed" — mechanism unexplained; verify in step 4 |
| E2E timeout (30 s, not 60) | ❌ bug | `$Waited++` every 500 ms, `$MaxWait=60` → 30 s. B rebind retry up to 30 s — tight |
| `.old` cleanup path after rename | ✅ works (09-03) | A's log: `Sweep: removed stale old binary ...llamactl-v1.exe.old` at v1 startup |
| In-flight SSE continuity | ❌ open | SSE dropped at swap (`forcibly closed by the remote host`) — expected while FileConn fails |
| Production `:8079` hot-swap | ⬜ not yet | Blocked until 18079 E2E is green |

---

## Next steps (in order)

### Step 1 — Land config-path fix + verify E2E paths

- [x] Keep `pkg/manager/hotswap.go` change **uncommitted** (shim; not needed in production)
- [x] Confirmed `scripts/hot-swap-e2e.ps1` uses absolute `$ConfigPath`, `$DataDir`, `$LogsDir`, v1/v2 exe paths, swap `binary_path`; v1 starts with `WorkingDirectory = $RepoRoot` and `LLAMACTL_CONFIG_PATH` set for v1's env
- [x] E2E run 2026-09-03: A log shows `Read config at E:\OpenClaw_Projects\llamactl-src\build\test-config.yaml`; B served `GET /api/v1/version` on **18079** (`:8080` empty). **Gate met.**
- [ ] Follow-ups found in this run (fold into steps 2/4/5):
  - B's stderr not captured (B is `llamactl-candidate.exe`, no redirects) — step 2
  - Teardown `Get-Process llamactl-v*` does not match `llamactl-candidate` → B orphaned after script; kill by path under `build\`
  - Status poller reached `phase=complete` — expected to be `idle` after B deletes the state file; verify in step 4
  - `.old` sweep actually worked at v1 startup (`Sweep: removed stale old binary ...llamactl-v1.exe.old`) — step 5 may be lower priority than assumed

### Step 2 — Capture B's logs

- [x] Confirmed B is spawned detached with no output inheritance — PowerShell redirects cannot reach it; A's `v1-stderr.log` is the reliable source (A logs B's `GOT err=` responses verbatim, e.g. `HotSwap: B failed to adopt socket: fileconn: ...`) — proven in the 09-03 run
- [x] `scripts/hot-swap-e2e.ps1` now dumps A's full stderr after the swap completes (and on not-ready failure), labeled `--- v1 (A) stderr ---`
- [x] Teardown fixed: kills canary processes by `Path` under `build\` (catches `llamactl-candidate.exe`); no unfiltered `Get-Process llamactl`

### Step 3 — Fix or drop `net.FileConn` on adopted sockets

**Context:** The POC (`poc/socket_handoff/`) proves the kernel-level handoff (raw `recv`/`send` after `WSASocket`). The product path differs at the last step: `os.NewFile` + `net.FileConn` wrapping the adopted handle. That is the bug.

**POC `fileconn_probe` (09-03):** Baseline `os.NewFile` + `net.FileConn` **succeeds** — read + write OK, C got response from B.

**POC `drain_probe` (09-03):** Proves the drain + listener handoff sequence works end-to-end with raw Winsock in B. C reconnects and gets `{"server":"B"}`.

**Root cause (09-03, traced through Go source):**
- `net.FileConn` → `fd.pfd.Init("tcp", true)` → `fd.pd.init(fd)` (IOCP association)
- Windows `poll.FD.Init`: if `pd.init()` fails, returns error. **No blocking fallback** (unlike Unix)
- IOCP association fails for WSADuplicateSocket-imported sockets with 10022
- `SetFileCompletionNotificationModes` is NOT the cause (pre-setting succeeds, FileConn still fails)
- **Conclusion:** Go's Windows netpoller cannot IOCP-associate WSADuplicateSocket-imported sockets. `net.FileConn`/`net.FileListener` unusable for these.

**Decision (09-03):** B uses raw Winsock. Two options for the HTTP layer:
- **Option A:** Thin `net.Conn` adapter over raw `recv`/`send`/`closesocket`, fed into Go's `http.Server` mux. Preserves routing, middleware, SSE. ~100 lines of adapter code.
- **Option B:** Raw HTTP dispatch (parse request line, route by path, write response). Simpler, no Go net stack. Loses middleware. ~50 lines.

- [x] Probe in isolation (`poc/fileconn_probe/`): baseline FileConn **passes**
- [x] Trace root cause: Go Windows `poll.FD.Init` → `pd.init()` IOCP association fails, no blocking fallback
- [x] Confirm `SetFileCompletionNotificationModes` is NOT the cause (preset test)
- [x] POC `drain_probe`: drain + listener handoff works with raw Winsock in B
- [ ] **Implement B's raw Winsock accept loop in the product** (`pkg/hotswap/` or `pkg/server/`)
- [ ] **Choose Option A or B** for the HTTP layer and implement
- [ ] **Keep the `*os.File` alive** in `adoptedFiles` (GC closing kills the SOCKET)
- [ ] `WSASocket` flags: `WSA_FLAG_OVERLAPPED` (`0x01`) for A's dup; **blocking import** (no flag) works for B's listener
- [ ] **Do not** call Winsock `listen()` on a duplicated listening socket
- [ ] **SSE:** skip SSE connections during drain (UI reconnects); model proxy connections are short-lived and drain cleanly

### Step 4 — Fix status `complete` vs deleted state file

- [ ] Option A: keep `phase: complete` on disk until B's first successful serve (or a TTL)
- [ ] Option B: change E2E success to: port accepts + `GET /version` 200 + optionally `GET /instances` — treat missing/idle status as OK once API is back
- [ ] Option C: A leaves the file as `complete`, B does not delete until cleanup of `.old`
- [ ] Fix E2E timeout: use wall-clock 60 s, not `$Waited++` × 500 ms (= 30 s)

### Step 5 — Fix `.old` cleanup path

- [ ] B's `cleanupOldBinary` uses `os.Executable()+".old"` — after rename, `os.Executable()` may still report the original candidate path. **Note (09-03):** startup sweep removed `llamactl-v1.exe.old` in the E2E run, so the sweep path works; B's own cleanup may still miss — verify, then fix if needed
- [ ] Align product cleanup with the **canonical** path A renamed to, not `os.Executable()` post-rename
- [ ] E2E teardown: `Stop-Process` by path under `build\`, not `Get-Process llamactl` (can hit production)

### Step 6 — Optional: dummy instance adoption in E2E

- [ ] Start a fake backend (tiny HTTP `/health` process) as a stand-in for llama-server
- [ ] Assert B adopts it after swap (PID alive, `/health` reachable)
- [ ] Or: real llama-server if Ian allows GPU use

### Step 7 — v2: hand off the listening socket (no rebind)

**Do only after v1 E2E is green on 18079.**

- [ ] Typed protocol: distinct verbs `LISTENER <b64>` vs `DUP <b64>` — never "first message is the listener"
- [ ] Extract listener FD on A via `TCPListener.SyscallConn().Control` (not `File().Fd()`)
- [ ] Duplicate with same ws2 helpers (624-byte blob, `WSA_FLAG_OVERLAPPED`)
- [ ] **Do not** call `listen()` on a duplicated listening socket
- [ ] Wrap with `os.NewFile` + `net.FileListener`, retain `f` — same FileConn class of bug; solve wrapping once (Step 3) then reuse
- [ ] `AdoptedListener()` must be a **method** on `instanceManager` (or small interface)
- [ ] B listen order: if adopted FD ≠ 0, wrap and serve; **do not** also `net.Listen` the same port
- [ ] A close order: DUP listener + clients → BYE/ACK → rename → **then** close A's listener → `hotSwapExit`
- [ ] No `WSACleanup` in `HotSwapB` (already omitted; keep)
- [ ] `TrackingListener` on B: wrap adopted listener so a later swap can DUP again
- [ ] Failure fallback: if listener DUP fails, log and use v1 rebind

---

## v1 E2E done when

- [x] B binds the test port (18079), not 8080
- [x] After swap, `GET /api/v1/version` and `GET /api/v1/instances` succeed on that port
- [x] A is gone; model children (if any) still have their PIDs
- [x] Script reaches "E2E test complete" without throwing
- [x] In-flight SSE: documented as dropped for v1 (UI reconnects). Model proxy connections drain cleanly (short-lived, no in-flight I/O at swap time)
- [x] **POC `drain_probe` findings recorded** in `docs/hot-swap-drain-poc.md`
- [x] **Option A verified in POC:** `net.Conn` adapter + `http.ServeMux` works end-to-end. B serves `/healthz` and `/work` via raw Winsock under the Go HTTP stack. C reconnects and gets `{"server":"B-optA"}`. **PASS.**
- [ ] **Implement Option A in the product** (`pkg/hotswap/` or `pkg/server/`): `rawConn`, `rawListener`, `rawRespWriter`, accept loop

---

## Safety reminders

- **Do not** hot-swap production `:8079` until 18079 is green with scratch DB
- Kill only processes whose `Path` is under `build\` — never `Get-Process llamactl` unfiltered
- Config is `LLAMACTL_CONFIG_PATH`, not `--config`. A second process with a bad/missing path opens the **live** DB at `C:\ProgramData\llamactl\llamactl.db`
- Build: `CGO_ENABLED=1`, `CC=C:\Tools\mingw64\bin\gcc.exe`, `GOCACHE=E:\OpenClaw\tmp\llamactl-gocache`, target `./cmd/server`

## Run commands

```powershell
# One E2E pass
cd E:\OpenClaw_Projects\llamactl-src
powershell -ExecutionPolicy Bypass -File .\scripts\hot-swap-e2e.ps1

# After failure: inspect
Get-Content build\v1-stderr.log
Get-NetTCPConnection -LocalPort 18079,8080
Get-Process llamactl-v1,llamactl-v2,llamactl-candidate -ErrorAction SilentlyContinue

# Fast compile check (no CGO)
$env:GOCACHE = "E:\OpenClaw\tmp\llamactl-gocache"
go test ./pkg/hotswap/ ./pkg/instance/ ./pkg/server/
go build -o NUL ./pkg/manager/
go build -o NUL ./cmd/server/
```
