# Graceful drain POC (Windows)

This is a reconnect-safe HTTP restart POC. It intentionally does **not** transfer active client sockets or use listener duplication: the adopted-listener-to-Go-`net.Listener` path remains unresolved on Windows.

## What it demonstrates

1. A starts an HTTP server and spawns B with a private loopback control port plus one-time nonce.
2. B proves it is alive (`READY`), but does not bind the public port yet.
3. A marks itself draining, disables keep-alives, and closes its listener.
4. B receives `RELEASE`, rebinds the same public port, then acknowledges `SERVING`.
5. A calls `Shutdown` with a 10-second deadline, allowing already-admitted handlers to complete while B accepts reconnects.

There is a deliberately small `ECONNREFUSED` window between A releasing and B binding. Clients must retry. Existing keep-alive/HTTP2 connections are drained/closed by A rather than transferred.

## Run

```powershell
cd E:\OpenClaw_Projects\llamactl-src\poc\graceful_drain
go build -o graceful-drain.exe .
.\graceful-drain.exe --role=demo --port=18080 --drain-after=2s
```

Expected final line:

```text
demo: PASS — retry reached B: ok from B
```

For manual testing, run A and issue `GET /work?ms=...` requests while it drains. `GET /healthz` identifies the serving process.
