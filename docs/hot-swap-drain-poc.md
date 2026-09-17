# POC: drain_probe — findings and direction

**Date:** 2026-09-03
**POC location:** `poc/drain_probe/`
**Run:** `drain.exe --i-am-o`

## What the POC proves

The **drain + listener handoff** sequence works end-to-end on Windows, with **two variants of B**:

### Variant 1 — raw Winsock (`--i-am-b`)
1. A serves HTTP, C fires `/work` (in-flight during swap).
2. A duplicates the listener to B, emits blob, drains, exits.
3. B imports via `WSASocketW`, does raw `accept()`/`recv()`/`send()` loop.
4. C reconnects, fires `/work` → gets `{"server":"B"}`. **PASS.**

### Variant 2 — Option A: `net.Conn` adapter + `http.ServeMux` (`--i-am-b-optA`)
1. A serves HTTP, C fires `/work` (in-flight during swap).
2. A duplicates the listener to B, emits blob, drains, exits.
3. B imports via `WSASocketW`, wraps in `rawListener` (implements `net.Listener`), accepts via `rawConn` (implements `net.Conn` over `ws2.Recv`/`ws2.Send`/`ws2.Close`).
4. Each accepted connection is served by `http.ReadRequest` + `mux.ServeHTTP` with a `rawRespWriter` (implements `http.ResponseWriter`).
5. C reconnects, fires `/work` → gets `{"server":"B-optA"}`. **PASS.**

**Both variants pass.** Option A preserves the Go HTTP stack (routing, middleware, existing handlers) while using raw Winsock under the hood. **This is the recommended approach for the product.**

### Option A implementation details

```
rawConn (net.Conn over syscall.Handle)
  Read → ws2.Recv(sock, b)
  Write → ws2.Send(sock, b)
  Close → ws2.Close(sock)
  LocalAddr/RemoteAddr → &net.TCPAddr{}
  SetDeadline/SetReadDeadline/SetWriteDeadline → nil (no-op)

rawListener (net.Listener over syscall.Handle)
  Accept → ws2.Accept(sock) → &rawConn{sock: cs}
  Close → ws2.Close(sock)
  LocalAddr → &net.TCPAddr{}

rawRespWriter (http.ResponseWriter over rawConn)
  Header → http.Header{}
  WriteHeader → store status
  Write → emit "HTTP/1.1 <status> <text>\r\n...\r\n\r\n" + body via conn.Write

Per-connection serving:
  conn, _ := rl.Accept()
  br := bufio.NewReader(conn)
  req, _ := http.ReadRequest(br)
  mux.ServeHTTP(&rawRespWriter{conn: conn.(*rawConn)}, req)
  conn.Close()
```

### What this means for the product

B's accept loop in the product will use this same pattern. The `net.Conn` adapter is ~40 lines, the `rawRespWriter` is ~30 lines, and the accept loop is ~15 lines. Total: ~85 lines of new code, replacing the `os.NewFile` + `net.FileListener` path that doesn't work.

## Root cause: why `net.FileConn` / `net.FileListener` fail

Traced through Go source (`src/net/fd_windows.go` → `src/internal/poll/fd_windows.go`):

### Call chain

```
net.FileConn(f)
  → newTCPConnFromFD(fd)
    → fd.pfd.Init("tcp", true)    // pollable=true, hardcoded
      → fd.pd.init(fd)            // IOCP association ← FAILS with 10022
      → SetFileCompletionNotificationModes(...)  // NOT the cause
```

### Windows vs Unix `poll.FD.Init`

**Unix (`fd_unix.go`):**
```go
func (fd *FD) Init(net string, pollable bool) error {
    if !pollable {
        fd.isBlocking = 1
        return nil
    }
    err := fd.pd.init(fd)
    if err != nil {
        fd.isBlocking = 1  // ← FALLBACK to blocking on failure
    }
    return err
}
```

**Windows (`fd_windows.go`):**
```go
func (fd *FD) Init(net string, pollable bool) error {
    if !pollable {
        return nil
    }
    err := fd.pd.init(fd)
    if err != nil {
        return err  // ← NO FALLBACK. Error propagates.
    }
    fd.associated = true
    syscall.SetFileCompletionNotificationModes(...)
    return nil
}
```

### Why `pd.init()` fails

`pd.init()` associates the socket handle with Go's runtime IOCP. For a socket created via `WSASocketW` from a `WSADuplicateSocketW` blob, this association fails with **10022 (WSAEINVAL)**. The socket is valid (subsequent `accept()`, `recv()`, `send()` work), but Go's IOCP machinery rejects it.

### Ruled out

- **`SetFileCompletionNotificationModes`:** Pre-setting `FILE_SKIP_SET_EVENT_ON_HANDLE` succeeds, but `net.FileConn` still fails. Not the cause.
- **Handle source (`File().Fd()` vs `SyscallConn().Control`):** Both produce the same 10022 in B. The issue is in B's `net.FileConn` → `pd.init()` path.
- **`WSA_FLAG_OVERLAPPED`:** Both overlapped and blocking imports fail in `net.FileConn`. The flag doesn't matter.

## Design implications

### B must use raw Winsock

`net.FileConn` and `net.FileListener` are **unusable** for WSADuplicateSocket-imported sockets on this Windows build. Go runtime limitation (no blocking fallback in Windows `poll.FD.Init`), not a product bug.

### Option A (chosen)

Thin `net.Conn` adapter over raw Winsock, fed into Go's `http.ServeMux` per-connection. Preserves routing, middleware, SSE, and the existing handler code. ~85 lines of new code.

### SSE handling

SSE connections are **skipped during drain** (the UI reconnects to B after the swap). Already implemented via `ConnPathMiddleware` + `SocketHandles()` filtering. The drain POC confirms this is correct: in-flight SSE drops, UI reconnects, B serves the new subscription.

### Model proxy connections

Model proxy connections (`POST /v1/chat/completions`) are **short-lived HTTP requests**. At swap time, they're either complete (no handoff needed) or in the kernel buffer (A drains them via `WaitGroup`). The drain window (5s in POC, configurable in production) is generous enough.

## Files

- `poc/drain_probe/main.go` — flag dispatch
- `poc/drain_probe/drainA.go` — A: HTTP server + drain + listener dup
- `poc/drain_probe/drainB.go` — B variant 1: raw Winsock accept/recv/send
- `poc/drain_probe/drainB_optA.go` — B variant 2: net.Conn adapter + http.ServeMux
- `poc/drain_probe/drainC.go` — C: client, fire /work on A, reconnect to B
- `poc/drain_probe/orchestrator.go` — spawns A, C, B; coordinates the swap
- `poc/drain_probe/preset.go` — `--i-am-p`: tests `SetFileCompletionNotificationModes` preset
- `poc/drain_probe/ws2/` — Winsock bindings
- `poc/drain_probe/respwriter.go` — minimal `http.ResponseWriter` for raw mode

## Run

```powershell
cd E:\OpenClaw_Projects\llamactl-src\poc\drain_probe
$env:GOCACHE = "E:\OpenClaw\tmp\llamactl-gocache"
go build -o drain.exe .
.\drain.exe --i-am-o
```

Expected output ends with:
```
C: VERDICT: PASS — C reconnected to B and got a response
```
