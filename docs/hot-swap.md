# Hot-Swap Design for llamactl

**Status:** Design (POC-validated)
**Date:** 2026-09-02
**Author:** Selina
**POC:** `poc/socket_handoff/` — see Appendix A for measured results

---

## 1. Problem

llamactl is the control plane for llama-server model instances. Today, updating the
llamactl binary requires stopping it, which in turn stops every model instance it
owns (force-kill after a 5-second grace on Windows). This disconnects every client
talking to the models — including OpenClaw, which routes all local model traffic
through `http://127.0.0.1:8079/v1`.

**Goal:** Replace the running llamactl binary (and its web UI) with a newer version
**without** stopping the model instances and **without** dropping any in-flight or
subsequent client connections.

**Non-goal:** Zero-loss handoff of in-flight *compute* (a generation that is mid-
inference in the old process's context may still need to complete in the model
child). We preserve the *connection* and the *request*; the model child does the
work.

---

## 2. What we know (measured, not assumed)

All measurements were taken on the target host: **Windows 10.0.22631 (x64)**,
Go 1.27, Admin / High Mandatory Level.

### 2.1 Socket rebind

| Scenario | Result |
|---|---|
| Close listener, immediately rebind (no live clients) | **110 ms, attempt 1.** No `TIME_WAIT` blocking. |
| Close listener with one held (in-flight) connection | **`srv.close()` blocks** until the held connection ends. The port is not released. |

**Implication:** The old process *cannot* release `:8079` while a streaming
response is live. The handoff must therefore keep the old process serving until
its connections are done, or move the connections to the new process *before*
the old one exits. `WSADuplicateSocket` (below) is the mechanism that makes the
second option possible.

### 2.2 Binary replacement

| Operation on a running `.exe` | Result |
|---|---|
| `Remove-Item` (delete) | **Access denied.** |
| `Rename-Item` (rename) | **Succeeds.** The running process keeps its handle to the old inode; the directory entry is freed. |

**Implication:** The swap sequence is *rename-old → rename-new → (later) delete-old*.
This is the standard Windows "you can't replace a running binary, but you can
rename it" dance.

### 2.3 Cross-process socket handoff (`WSADuplicateSocket`)

**PROVEN on this host.** A live TCP connection was handed from process A to
process B via `WSADuplicateSocket` + `WSASocket`. The client (a third process)
saw zero interruption: no TCP reset, no re-handshake, data intact in the kernel
buffer.

Key findings:

- Go's `net.TCPConn.File().Fd()` **is** the Winsock `SOCKET` on Windows. No FFI
  dance needed — the FD is directly usable with `WSADuplicateSocket`.
- `WSADuplicateSocket` returns a **544-byte** `WSAPROTOCOL_INFO` blob on this
  system. The struct layout in the POC is correct.
- The target process must call `WSAStartup` before `WSASocket(..., &info)`.
- Data in the kernel receive buffer **survives the handoff**. A did `Accept()`
  but did not `Read()`; B adopted the socket and read the full payload.
- The handoff protocol (READY → DUP → GOT → BYE → ACK) is clean, debuggable,
  and line-based over a dedicated TCP port.

Full POC code: `poc/socket_handoff/` (6 files, ~500 lines Go).
Run: `go run . --i-am-a` (A orchestrates, spawns B and C).

---

## 3. Architecture

### 3.1 Three objects, three survival guarantees

| Object | Owner | Must survive the swap? |
|---|---|---|
| **Client TCP connection** (client ↔ `:8079`) | Kernel (as long as a socket holds it) | **Yes** — never drop |
| **llamactl process** (control plane, web UI) | Userland | **No** — die and be replaced freely |
| **Model child** (llama-server, VRAM) | Child process | **Yes** — never die |

The design principle: **keep all real work in the model child (which survives),
and let llamactl be pure plumbing (which can die).**

### 3.2 Process model

```
                    ┌─────────────────────────────────────────────┐
                    │            llamactl (control plane)          │
                    │  • starts/stops model children               │
                    │  • serves API + web UI on :8079              │
                    │  • proxies inference to model children       │
                    │  • swappable: dies and is replaced freely    │
                    └────────────────┬────────────────────────────┘
                                     │ spawns (detached)
                    ┌────────────────┴────────────────────────────┐
                    │         model children (llama-server)        │
                    │  • own the VRAM                               │
                    │  • survive llamactl's death                   │
                    │  • adopted by the new llamactl                │
                    └─────────────────────────────────────────────┘

Client ──TCP──▶ :8079 (llamactl's listener)
                 │
                 │  WSADuplicateSocket hands the socket to the new llamactl
                 ▼
           :8079 (new llamactl's listener)
```

**No relay process.** The new llamactl directly inherits the client's sockets
via `WSADuplicateSocket`. This is simpler than a forwarding sidecar and was
validated by the POC.

### 3.3 The swap sequence

```
A (old llamactl)                          B (new llamactl)
─────────────────                         ─────────────────
T0:  Receive "hot-swap" request
     (API: POST /api/v1/hot-swap
      with binary_path)
     Acquire the swap mutex (see §3.8)

T0.5: Write handoff-state.json
      (phase: "starting", b_pid: 0)
      — before spawning B, so B has a
        state file to find if A crashes

T1:  Pick a free handoff port P
     (bind :0, read back, close)

T2:  Copy the new binary into A's
     directory (if not already there):
     binary_path → llamactl-candidate.exe

T3:  Spawn B detached:
     llamactl-candidate.exe --handoff-port=P --hot-swap
     (CREATE_NEW_PROCESS_GROUP |
      DETACHED_PROCESS on Windows)
                                     T4:  WSAStartup
T5:  Update handoff-state.json
     (phase: "starting", b_pid: <actual PID>)
                                     T6:  Load config from DB (same
                                         SQLite file as A)
                                     T7:  Listen on P (handoff port)
                                     T8:  Send READY pid=<B_PID>
T9:  Connect to P, receive READY
     Verify B_PID matches the PID
     from CreateProcess (sanity check)
     Update handoff-state.json
     (phase: "in_progress")

T10: For each live client socket S
     on :8079:
       WSADuplicateSocket(S, B_PID, &info)
       Serialize info → send over P
                                     T11: Receive info
                                          WSASocket(..., &info) → S'
                                          Send GOT ok
T12: Receive GOT ok
     Update handoff-state.json
     (sockets_handed_off: N)

T13: Send BYE
                                     T14: Send ACK
                                         Close handoff port P
T15: Receive ACK

T16: Rename old binary:
     llamactl.exe → llamactl.old.exe

T17: Rename candidate:
     llamactl-candidate.exe → llamactl.exe

T18: Update handoff-state.json
     (phase: "complete")

T19: Close :8079 listener
T20: Release swap mutex
T21: Exit
                                     T22: Now owns all client sockets
                                         Bind :8079 for new connections
                                         Adopt model children (pidfile
                                         + /health probe + log tail)
                                         Start background goroutine:
                                         poll A's PID for up to 60s;
                                         when A dies, delete
                                         llamactl.old.exe
                                         Continue serving
```

**Total client-visible downtime:** the window between T19 (A closes `:8079`)
and T22 (B binds `:8079`). Measured at **~110 ms** in the rebind probe (no
live clients). With live clients, A keeps serving on the duplicated sockets
until it exits, so the window is even smaller.

**Why A does the renames (T16–T17) before exiting (T21):**
The canonical path (`llamactl.exe`) must point to the new binary the *moment*
A exits. If B did the renames, there would be a window where the canonical
path still points to the old (dead) binary. A doing both renames before
exiting eliminates that window.

### 3.4 Model child adoption

The model children are spawned **detached** (own process group, not console
children), so they survive A's death. B discovers and adopts them:

1. **Pidfile:** A writes `data/instances/<name>/runtime.json` at start:
   ```json
   {
     "pid": 12345,
     "port": 8081,
     "started_at": "2026-09-02T20:00:00Z",
     "generation": 7,
     "log_file": "data/logs/qwen38.log",
     "log_offset": 4096
   }
   ```
   Written atomically (write to temp, rename). Removed on clean stop.
   `log_offset` is the byte offset of the last byte A read, so B can
   re-attach the log tail from the right position.

2. **Adoption on startup:** For each instance with a `runtime.json`:
   - Check PID alive (`OpenProcess` on Windows).
   - Probe `http://127.0.0.1:<port>/health` (already exists in llama-server).
   - Re-attach log tail (open the log file, seek to `log_offset`, read from there).
   - Mark the instance `Running` with `adopted=true`.

3. **Stop path for adopted instances:** Signal the PID (`TerminateProcess`
   on Windows, or `SIGTERM` on Unix), wait for the port to release. Less
   graceful than the pipe-based stop for fresh instances, but the model
   children are the *same* binaries — they respond to the same signals.

4. **Generation token:** Each start increments `generation`. A stale monitor
   from a prior generation must not close a newer generation's state. The
   existing `monitorProcess` stale-monitor logic in `process.go` is the right
   pattern; extend it to adoption.

### 3.5 Binary swap sequence

```
1. A copies the new binary into its directory:
   binary_path → llamactl-candidate.exe
   (T2 in the swap sequence; handles cross-drive/cross-folder paths)

2. A renames itself:
   llamactl.exe → llamactl.old.exe
   (succeeds even while the old process is running)

3. A renames the candidate:
   llamactl-candidate.exe → llamactl.exe

4. A exits.

5. B (background goroutine) deletes llamactl.old.exe
   after confirming A is dead (poll A's PID, up to 60s).

6. Safety net: any new llamactl startup sweeps for stale
   llamactl.old.exe files and deletes them if the associated
   PID is dead.
```

This is the standard Windows binary-replacement dance. It works because the
running process holds a handle to the file's *inode*, not the directory entry.

### 3.6 Handoff state file (safety mechanism)

**File:** `data/handoff-state.json` (in the same data dir as the DB)

**Schema:**
```json
{
  "phase": "starting | in_progress | complete | failed",
  "a_pid": 11111,
  "b_pid": 22222,
  "handoff_port": 51170,
  "binary_path": "C:\\path\\to\\llamactl.new.exe",
  "started_at": "2026-09-02T22:30:00Z",
  "completed_at": null,
  "sockets_handed_off": 0,
  "total_sockets": 3,
  "error": null
}
```

**Lifecycle:**

| Phase | Written by | Meaning |
|---|---|---|
| `starting` (b_pid: 0) | A, before spawning B (T0.5) | Swap initiated, B not yet spawned |
| `starting` (b_pid: N) | A, after spawning B (T5) | B is alive, handoff not yet started |
| `in_progress` | A, after verifying READY (T9) | Sockets are being handed off |
| `complete` | A, after receiving ACK (T18) | Handoff succeeded, renames done |
| `failed` | A, on error | Handoff failed, A cleaned up |

**A's cleanup (primary):**
If the handoff fails at any point, A:
1. Kills B (by PID from the state file).
2. Keeps serving on its own sockets (the `DUP` created copies in B; A's
   originals are still valid).
3. Writes `phase: "failed"` + `error` to the state file.
4. Deletes the state file.
5. Releases the swap mutex.
6. Reports the failure to the caller (API 500 + error message).

**B's takeover (secondary):**
On startup, B checks for `handoff-state.json`:

| State file | B's action |
|---|---|
| Not present | Normal startup. Adopt model children. Continue. |
| `phase: "starting"`, b_pid == B's PID | I'm the survivor. A crashed after spawning me. Continue: adopt model children, bind :8079, start serving. |
| `phase: "starting"`, b_pid != B's PID | Stale state from a previous attempt. Kill the old b_pid if alive. Delete state file. Continue. |
| `phase: "in_progress"` | I was mid-handoff. A crashed. I hold some sockets. Continue: adopt model children, bind :8079 for new connections, serve on my adopted sockets. Delete state file. |
| `phase: "complete"` | Previous handoff succeeded. I'm the new process. Continue normally. Delete state file. |
| `phase: "failed"` | A already cleaned up. Delete state file. Continue. |

**Worst case (A crashes before writing the state file):**
The state file doesn't exist. B starts, sees no state file, assumes normal
startup. A's client sockets are orphaned (TCP RST from A's death). The model
children are detached and survive. B adopts them. Clients reconnect. This is
equivalent to a cold start, minus the model cold-load.

### 3.7 Binary cleanup

**Primary: B's background goroutine.**
After a successful handoff, B spawns a goroutine that polls A's PID
(from the state file or from the spawn call) for up to 60 seconds.
When A dies, B deletes `llamactl.old.exe`.

```go
// B's cleanup goroutine (pseudocode)
go func(aPID int) {
    for i := 0; i < 60; i++ {
        if !isProcessAlive(aPID) {
            os.Remove("llamactl.old.exe")
            return
        }
        time.Sleep(1 * time.Second)
    }
    // A still alive after 60s. Leave the old binary.
    // Safety net: next startup will sweep it.
}()
```

**Safety net: startup sweep.**
On any llamactl startup (B or any future process), check for `llamactl.old.exe`:
- If it exists, check the state file for the associated PID.
- If the PID is dead, delete `llamactl.old.exe`.
- If the PID is alive, leave it (A is still running; it will clean up on its own).

This handles the case where B's 60s timeout expired and the old binary was
left behind.

### 3.8 Swap mutex (concurrency gate)

The hot-swap must not run concurrently with instance actions (start/stop/restart),
and instance actions must not run during a hot-swap. This prevents:
- A starting a new instance while B is adopting (double-start).
- A stopping an instance that B is about to adopt (race).
- Two hot-swaps running simultaneously (impossible in practice, but the mutex
  makes it explicit).

**Implementation:** A single `sync.Mutex` (or `sync.RWMutex`) in the manager,
shared between the hot-swap handler and the instance action handlers.

```go
// In the manager (pseudocode)
type Manager struct {
    swapMutex sync.Mutex // held during hot-swap
    // ... existing fields
}

// Hot-swap handler
func (m *Manager) HotSwap(binaryPath string) error {
    m.swapMutex.Lock()
    defer m.swapMutex.Unlock()
    // ... T0 through T21
}

// Instance action handler (start/stop/restart)
func (m *Manager) StartInstance(name string) error {
    m.swapMutex.Lock()
    defer m.swapMutex.Unlock()
    // ... existing start logic
}
```

**API behavior:** If a hot-swap is in progress and the user clicks "Start" on
an instance, the instance action returns `503 Service Unavailable` with a
`Retry-After` header. The UI shows a "Server is updating, retrying..." toast
and retries after 2s.

**UI behavior:** While a hot-swap is in progress, the instance action buttons
(Start/Stop) are disabled. The "Hot Swap" button in the toolbar shows a
progress indicator.

### 3.9 Platform gate

The hot-swap mechanism is **Windows-only** in v1:

- `WSADuplicateSocket` is a Windows API (the POC is Windows-only).
- The binary swap (rename → rename → delete) is a Windows pattern.
- The process detachment flags (`CREATE_NEW_PROCESS_GROUP`, `DETACHED_PROCESS`)
  are Windows.

**Implementation:**
```go
// In the hot-swap handler
if runtime.GOOS != "windows" {
    http.Error(w, `{"error": "hot-swap is not supported on this platform"}`, http.StatusBadRequest)
    return
}
```

**API:** `POST /api/v1/hot-swap` returns `400` on non-Windows.
**Config endpoint:** `GET /api/v1/config` includes:
```json
{
  "platform": "windows",
  "hot_swap_supported": true
}
```
**UI:** The "Hot Swap" button in the top toolbar is hidden if
`hot_swap_supported` is false.

The Unix path is a TODO stub that returns a clear error. The `ws2` package
already has `//go:build windows` on it, so it doesn't pollute the Unix build.

### 3.10 API endpoints

**`POST /api/v1/hot-swap`**

Request:
```json
{
  "binary_path": "C:\\path\\to\\llamactl.new.exe"
}
```

Response (200, swap started successfully):
```json
{
  "status": "complete",
  "a_pid": 11111,
  "b_pid": 22222,
  "sockets_handed_off": 3,
  "total_sockets": 3,
  "started_at": "2026-09-02T22:30:00Z",
  "completed_at": "2026-09-02T22:30:05Z"
}
```

Response (500, swap failed):
```json
{
  "status": "failed",
  "error": "binary not found: C:\\path\\to\\llamactl.new.exe",
  "b_pid": 22222,
  "b_killed": true
}
```

Response (400, not supported):
```json
{
  "error": "hot-swap is not supported on this platform"
}
```

**`GET /api/v1/hot-swap/status`**

Returns the current swap state (from `handoff-state.json`):
```json
{
  "phase": "in_progress",
  "a_pid": 11111,
  "b_pid": 22222,
  "sockets_handed_off": 2,
  "total_sockets": 3,
  "started_at": "2026-09-02T22:30:00Z"
}
```

Or, if no swap is in progress:
```json
{
  "phase": "idle"
}
```

The UI polls this endpoint every 500ms while a swap is in progress to show
progress. It also uses this to check state after a disconnect.

### 3.11 UI

**Toolbar button:**
A "Hot Swap" button in the top toolbar, next to "Help" and "Logout".
Hidden if `hot_swap_supported` is false (from `GET /api/v1/config`).

**Dialog (on click):**
```
┌─────────────────────────────────────────────┐
│  Hot Swap                                    │
├─────────────────────────────────────────────┤
│  New binary path:                            │
│  [C:\path\to\llamactl.new.exe          ] [Browse]│
│                                              │
│  [ Cancel ]            [ Start Swap ]         │
└─────────────────────────────────────────────┘
```

**Progress (during swap):**
The dialog stays open, the "Start Swap" button changes to a progress
indicator, and the status text updates from polling
`GET /api/v1/hot-swap/status`:
```
Swapping... (2/3 sockets handed off)
```

**Completion:**
On success, the dialog closes. The UI does **not** refresh the page.
The SSE connection drops (A dies) and the UI's existing reconnect logic
re-establishes it with B. The UI then:
1. Re-subscribes to events (existing reconnect behavior).
2. Refreshes the instance list (`GET /api/v1/instances`).
3. Re-enables the instance action buttons (Start/Stop).

**Error handling:**
- If the swap fails (API 500), the dialog shows the error message.
- If a request (e.g., "Start instance") returns 503 during the swap,
  the UI shows a "Server is updating, retrying..." toast and retries
  after 2s.

**No full page refresh:** The UI relies on the SSE reconnect + instance
list refresh. This avoids the jolt of a full reload and is consistent
with the "no relay process" architecture.

---

## 4. OpenClaw (harness) continuity

OpenClaw talks to `http://127.0.0.1:8079/v1`. During a swap:

| Phase | OpenClaw sees | Model state |
|---|---|---|
| T0–T15 | Normal (A still serving, sockets being duplicated) | Running |
| T19–T22 | **~110 ms window**: new connections may fail with `ECONNREFUSED` | Running (in VRAM) |
| T22+ | Normal (B serving, model adopted, KV cache intact) | **Same model, same cache** |

**What OpenClaw must do to be swap-tolerant:**

1. **Retry on `ECONNREFUSED` / connection error.** The existing
   `400 Failed to tokenize prompt` classification is a *format* error and
   won't retry. A connection-refused during the T19→T22 window must be
   treated as a **transient retry**, not a format error. This is the one
   OpenClaw-side change that matters.

2. **Model ID is stable.** `llamactl/qwen38-27b-192k` is the same string
   before and after. No reconfiguration needed.

3. **SSE streams that cut mid-stream** — OpenClaw should treat a truncated
   stream as a retryable error (map to the existing compact+retry path).

4. **No re-auth needed.** The API key is per-instance and stored in the DB,
   which B reads from the same SQLite file.

---

## 5. Milestones

| # | Milestone | Deliverable | Effort | Status |
|---|-----------|-------------|--------|--------|
| 0 | **POC: WSADuplicateSocket** | Proven cross-process socket handoff on Windows | — | **DONE** (`poc/socket_handoff/`) |
| 1 | **Detach children** | `setProcAttrs` real on Windows; `Shutdown()` skips kill for adopted; `runtime.json` written at start | M | |
| 2 | **Pidfile + identity on disk** | Atomic `runtime.json` write/remove; schema versioned; `log_offset` field | S | |
| 3 | **Adoption on startup** | Probe `/health`, re-attach log tail (from `log_offset`), rebuild state, mark `adopted` | M | |
| 4 | **Stop path for adopted** | PID-signal + port-release wait; keep pipe-stop for fresh | M | |
| 5 | **Hot-swap handler** | The T0–T21 sequence in `pkg/manager/`; swap mutex; `handoff-state.json` lifecycle; platform gate | M | |
| 6 | **Binary swap** | Copy → rename-old → rename-new → delete-old (B's goroutine + startup sweep) | S | |
| 7 | **API endpoints** | `POST /api/v1/hot-swap`, `GET /api/v1/hot-swap/status`; 503 on instance actions during swap | S | |
| 8 | **UI** | Toolbar button (gated on `hot_swap_supported`); dialog with path input; progress polling; SSE reconnect; no full refresh | M | |
| 9 | **End-to-end test** | Script: start model → hot-swap → verify model still serving → verify log continuity → verify binary replaced | S | |

**Critical path:** 1 → 2 → 3 → 5. Milestones 4, 6, 7, 8, 9 are hardening.

**Estimated total:** ~6–8 focused days for a solid v1.

---

## 6. Risks and open questions

| Risk | Mitigation |
|---|---|
| **A dies before B claims the socket** | B's takeover logic (§3.6) handles this: B reads the state file, sees `phase: "in_progress"`, continues serving on its adopted sockets. The state file is written before B is spawned, so B always has it. |
| **A crashes before writing the state file** | Worst case: B starts with no state file, assumes cold start. Client sockets are orphaned (TCP RST). Model children survive (detached). B adopts them. Equivalent to a cold start, minus the model cold-load. |
| **`runtime.json` race** (two llamactl writing) | Generation token + atomic rename. Needs a concurrent-write test. |
| **Log tail re-attach** | `log_offset` in `runtime.json` tells B where to resume. Edge case: log rotated during the swap (unlikely; the model child is the same process writing the same file). |
| **Windows socket rebind with held connections** | The T19→T22 window is bounded by the longest in-flight stream. For v1, accept it. If it becomes a problem, A can drain (wait for in-flight to complete) before closing the listener — it already does this in `Stop()` with a 30s grace. |
| **Orphaned model children** (if B never adopts) | The children are detached and survive A's death. B's adoption is best-effort; a failed adoption leaves a running model that's discoverable via pidfile. The startup sweep (§3.7) handles stale pidfiles. |
| **Stale `llamactl.old.exe`** | B's 60s goroutine + startup sweep (§3.7) handle this. |
| **Swap mutex held too long** | The swap is bounded by the handoff time (typically < 5s). If B doesn't ACK within 30s, A times out, kills B, and releases the mutex. |

---

## 7. What this is *not*

- **Not a zero-downtime guarantee.** There is a ~110 ms window where new
  connections may fail. In-flight requests are preserved (the socket is
  handed off with data in the kernel buffer). Compute that was in-flight in
  A's proxy context (between "A read the bytes" and "A wrote them to the
  model pipe") is lost — but that window is tiny and the model child
  continues the generation.
- **Not a container/orchestrator solution.** We're not adding Docker, K8s,
  or systemd. We're using Windows process APIs and the existing llamactl
  codebase.
- **Not a replacement for the existing cold-swap path.** The current
  "stop instances → swap → start instances" flow still works and is the
  fallback if hot-swap fails.
- **Not cross-platform in v1.** Windows only. The Unix path is a TODO stub.

---

## Appendix A: POC evidence

**Location:** `E:\OpenClaw_Projects\llamactl-src\poc\socket_handoff/`

**Files:**
- `go.mod` — module definition (Go 1.27, no external deps)
- `ws2/ws2.go` — Winsock 2 bindings: `WSAStartup`, `WSADuplicateSocket`, `WSASocket`, `WSAPROTOCOL_INFO` struct (544 bytes on this system)
- `protocol.go` — line-based handoff protocol (READY/DUP/GOT/BYE/ACK)
- `process_b.go` — B: WSAStartup, listen on handoff port, adopt sockets, read, respond
- `process_c.go` — C: client, connect, send payload, verify no drop
- `main.go` — A: orchestrator, spawns B+C, drives the swap
- `dispatch.go` — CLI dispatcher (`--i-am-a` / `--i-am-b` / `--i-am-c`)

**Run:**
```
cd poc/socket_handoff
go build -o socket-handoff.exe .
.\socket-handoff.exe --i-am-a
```

**Expected output (PASS):**
```
A: WSADuplicateSocket OK, blob size = 544 bytes
B: adopted socket 408 (raw handle)
B: read 23 bytes from adopted socket: "HELLO-FROM-C pid=...\n"
B: wrote response to client on adopted socket
C: received response: "RESPONSE-FROM-B pid=... data=...\n"
C: PASS — response is from B, payload echoed intact, no TCP reset seen.
C: The connection survived the swap. The socket was handed off cleanly.
```

**Hypothesis results:**

| H | Test | Result |
|---|---|---|
| H1 | Cross-process socket transfer, client sees no reset | **PASS** |
| H2 | Works over loopback (`127.0.0.1`) | **PASS** |
| H3 | `WSAStartup` required before `WSASocket` | **PASS** |
| H4 | A dies before B claims socket | **DEFERRED** (handled by state-file takeover in v1) |
| H5 | Data integrity (payload in kernel buffer survives handoff) | **PASS** |
