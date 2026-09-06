# Ratatoskr — P2P Backbone Plan

Module path: `github.com/achmadss/ratatoskr`

---

## 1. What this is

Two Go programs that let one machine read files on another machine over
the Internet, with the file bytes travelling directly between the two.

| Name | Role | Runs on |
|------|------|---------|
| **ratatoskr** | The agent. Owns the files. Also acts as a client. | Windows, macOS, Linux |
| **heimdall** | The signaling server. Introduces two agents to each other. | A small Linux VPS |

Ratatoskr is the squirrel that carries messages up and down the world
tree. Heimdall is the watchman at the bridge: he sees who is coming and
lets them across, but he never carries their luggage.

### The core rule

> Heimdall coordinates the connection. Ratatoskr carries the files.

File bytes never pass through the server on the normal path. That is the
whole point of the design, and it is what keeps hosting cheap.

### Out of scope

No web client. No UI. No installer. No pairing UX. No file index. No
uploads, deletes or renames. Read-only, command line only.

---

## 2. Shape

```
ratatoskr A                    heimdall                    ratatoskr B
(client)                    (WebSocket)                    (serving files)
    |                             |                             |
    |--- register --------------->|<--------------- register ---|
    |--- offer  (for B) --------->|---------- offer ----------->|
    |<-- answer ------------------|<--------- answer -----------|
    |<-> ice candidates <-------->|<------> ice candidates <---->|
    |                             |                             |
    |============ WebRTC : DTLS + SCTP DataChannels =============|
    |   "ctrl"    JSON requests and replies, reliable + ordered  |
    |   "xfer-N"  binary chunks, one channel per active download |
```

Heimdall holds one piece of state: which peer id is on which socket. It
sees no file names, no file contents, no directory listings.

---

## 3. Transport

WebRTC DataChannels, via [Pion](https://github.com/pion/webrtc) on both
ends. Pion is pure Go.

ICE order of preference:

1. Host candidates — same LAN, fastest
2. Server-reflexive via STUN — the normal Internet case
3. Relay via TURN — only when the network blocks everything else

The agent must log which candidate pair actually won, so "direct" versus
"relay" is a measured fact and not a guess.

---

## 4. Wire protocol

Two channels, two formats. Never mix them.

### 4.1 Control channel — label `ctrl`

Reliable, ordered. One JSON object per message.

```
HELLO       -> HELLO_OK        version negotiation
PING        -> PONG            liveness and round-trip time
LIST        -> LIST_RESULT     directory listing
STAT        -> STAT_RESULT     one entry
OPEN        -> OPEN_OK         begin a transfer; returns transfer_id, size, hash
CREDIT                         receiver grants N more chunks
CANCEL                         stop a transfer
ERROR                          code + safe message
```

Reserved but not implemented yet: `AUTH`, `AUTH_OK`.

Every request carries `version`, `request_id`, `type`.
Every reply echoes `request_id`.

Example:

```json
{ "version": 1, "request_id": "01J...", "type": "LIST", "path": "/Documents" }
```

```json
{
  "version": 1,
  "request_id": "01J...",
  "type": "LIST_RESULT",
  "entries": [
    { "name": "movie.mkv", "kind": "file", "size": 4294967296,
      "modified": "2026-09-05T08:00:00Z" }
  ]
}
```

### 4.2 Transfer channel — label `xfer-<transfer_id>`

Reliable, ordered. Binary frames, no JSON.

```
bytes 0..3   uint32 big-endian sequence number
bytes 4..N   payload, up to 16 KB
```

An empty frame means end of file. Then the channel closes.

`OPEN_OK` carries the size and a BLAKE3 hash of the whole file so the
receiver can verify what it got.

### 4.3 Limits, enforced by the serving side

- Control message larger than 64 KB → close the connection
- More than 4 concurrent transfers per peer → refuse with `ERROR`
- More than 8 connected peers → refuse the connection
- Unknown message type → reply `ERROR`, do not close
- `ERROR` never contains a real filesystem path or a stack trace

---

## 5. Backpressure

The disk reads far faster than the network sends. Without a brake,
memory grows until something dies. Two brakes, both required.

**Local brake — send buffer.**
Pause sending when `bufferedAmount` goes above 1 MB. Resume when it drops
below 256 KB.
Pion: `SetBufferedAmountLowThreshold` plus `OnBufferedAmountLow`.

**Remote brake — credit window.**
The receiver sends `CREDIT { transfer_id, chunks: N }`. The sender may
send at most N more chunks. Start with 64 credits, top up when half are
spent.

The local brake only knows about the local socket. It says nothing about
whether the far side is keeping up with writing to disk. That is why both
are needed.

Chunk size is **16 KB**. The spec allows up to 256 KB, but 16 KB is the
safe interoperable size and it performs fine.

Expected throughput: 30–100 MB/s on a LAN. Over the Internet, whatever
the serving side's upload link gives.

---

## 6. Signaling protocol — heimdall

One WebSocket endpoint. JSON messages. The server relays opaque blobs and
understands almost nothing.

```
client -> server   { "type": "register", "peer_id": "..." }
server -> client   { "type": "registered" }

client -> server   { "type": "offer",  "to": "<peer>", "sdp": "..." }
server -> peer     { "type": "offer",  "from": "<peer>", "sdp": "..." }

client -> server   { "type": "answer", "to": "<peer>", "sdp": "..." }
server -> peer     { "type": "answer", "from": "<peer>", "sdp": "..." }

client -> server   { "type": "ice",    "to": "<peer>", "candidate": {...} }
server -> peer     { "type": "ice",    "from": "<peer>", "candidate": {...} }

server -> client   { "type": "error",  "code": "...", "message": "..." }
server -> client   { "type": "peer_gone", "peer": "<peer>" }
```

Rules:

- Ping/pong every 20 seconds. Drop a socket that misses two.
- A `register` for a peer id that is already connected replaces the old
  socket. This is what makes agent restart work.
- Rate limit per socket. Signaling is cheap to abuse.
- Later, heimdall also mints short-lived TURN credentials. Never ship a
  static TURN password.

---

## 7. Path safety

Every filesystem call, without exception:

```
requested path
      -> reject if it contains a null byte
      -> filepath.Clean
      -> join to the allowed root
      -> filepath.EvalSymlinks   (resolves symlinks and junctions)
      -> verify the result is still inside the root
      -> open
```

`EvalSymlinks` is the step that stops a symlink pointing out of the
shared folder. Do not skip it, and do the check on the **resolved** path,
not the requested one.

Windows needs extra care: `C:` style prefixes, `\\?\` paths, alternate
data streams (`file.txt:stream`), and reserved names (`CON`, `NUL`,
`COM1`). Reject anything that is not a plain relative path.

The agent shares only explicitly listed roots. There is no default root.

---

## 8. Local control API

The agent runs an HTTP server on `127.0.0.1` on a random free port. It
writes the port and a random token to a state file, mode `0600`:

| OS | Path |
|----|------|
| Linux | `~/.config/ratatoskr/control.json` |
| macOS | `~/Library/Application Support/ratatoskr/control.json` |
| Windows | `%AppData%\ratatoskr\control.json` |

In Go: `os.UserConfigDir()`.

Every other CLI subcommand is an HTTP client against this API. So is any
future UI. Bind to `127.0.0.1` only, never `0.0.0.0`. Require the token
in an `Authorization` header.

This is the single most valuable early decision: it means a UI can be
written later in any language, and choosing that language costs nothing
today.

Endpoints:

```
GET  /v1/status        device id, signaling state, peers, connection type
GET  /v1/folders
POST /v1/folders       { "path": "..." }
DEL  /v1/folders       { "path": "..." }
GET  /v1/peers
GET  /v1/events        server-sent events, for live status
```

---

## 9. CLI surface

```
ratatoskr run                     start the agent in the foreground
ratatoskr status                  is it up, online, direct or relay
ratatoskr id                      print this device's peer id
ratatoskr folders list
ratatoskr folders add PATH
ratatoskr folders remove PATH
ratatoskr peers                   currently connected clients
ratatoskr version

ratatoskr connect PEER_ID ls PATH        client mode: list a directory
ratatoskr connect PEER_ID get PATH OUT   client mode: download a file
```

`run` is the process. Everything else is a thin HTTP client against a
running `run`.

`connect` is the client side. It exists so the whole system can be tested
with two agents and no browser. Later it becomes machine-to-machine
transfer.

```
heimdall serve --addr :8080
```

---

## 10. Build and platform rules

**The agent must stay free of cgo.** Pion is pure Go. Keep it that way
and one machine builds every target:

```
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/ratatoskr.exe   ./cmd/ratatoskr
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o dist/ratatoskr-mac   ./cmd/ratatoskr
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o dist/ratatoskr-linux ./cmd/ratatoskr
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o dist/heimdall        ./cmd/heimdall
```

Consequences to respect:

- No Go GUI toolkit inside the agent. All of them need cgo. A UI, if it
  ever happens, is a separate binary that talks to the control API.
- No tray icon in the agent. Same reason.
- If SQLite is ever needed, use `modernc.org/sqlite` (pure Go), not
  `mattn/go-sqlite3`.

---

## 11. Repository layout

```
ratatoskr/
├── cmd/
│   ├── ratatoskr/        the agent CLI
│   └── heimdall/         the signaling server
├── internal/
│   ├── protocol/         wire messages, shared by both binaries
│   ├── transport/        Pion, ICE, channels, backpressure
│   ├── signal/           signaling client and server logic
│   ├── fsroot/           allowed roots, path validation
│   ├── control/          127.0.0.1 HTTP API and control.json
│   └── config/           device identity, folder list, per-OS paths
├── Makefile
├── PLAN.md
├── TODO.md
└── go.mod
```

One module, two binaries. They share `internal/protocol`, so the message
format cannot drift apart. That is the reason to keep them together.

---

## 12. Build order

Each step has a check you can actually run. Do not move on early.

| Step | Goal | Passes when |
|------|------|-------------|
| 0 | Two agents on one machine, offer and answer pasted by hand | a string echoes back over a DataChannel |
| 1 | heimdall relays signaling | `ratatoskr connect` links up in under 2 s, no pasting |
| 2 | Control protocol: HELLO, PING, LIST, STAT | a real listing prints; `../../etc/passwd` is refused |
| 3 | Local control API and config file | `ratatoskr status` talks to a running `ratatoskr run` |
| 4 | Transfer one small file | a 10 MB file arrives, hash matches |
| 5 | Backpressure and a large file | 10 GB transfers, memory flat on both sides |
| 6 | Real NAT over the Internet | direct connection, proven by the logged candidate pair |
| 7 | TURN fallback with coturn | 1 GB transfers with STUN disabled |
| 8 | Survival | sleep, wake, network change, restart, cancel — never hangs |

Step 5 is the milestone that proves the project. Step 6 is the first step
that can genuinely fail.

---

## 13. Risks

| Risk | Likely | Response |
|------|--------|----------|
| TURN relay rate is much higher than 20% | Medium | Measure it from step 6. It is the number that decides whether hosting stays cheap |
| Throughput disappoints on long-distance links | Medium | SCTP is latency sensitive. Test one intercontinental hop early |
| Corporate networks block UDP entirely | Certain for some users | coturn on TCP and TLS port 443. A config, not a rewrite |
| Windows path edge cases open a hole | Medium | Treat `internal/fsroot` as security code. Table-driven tests with hostile inputs |
| The serving machine's upload speed is the real limit | Certain | Nothing to fix. Report it honestly |

None of these stop the project. The relay rate is the only one that can
change the economics.

---

## 14. Dependencies

| Project | Role | License |
|---------|------|---------|
| pion/webrtc | WebRTC in Go, both binaries | MIT |
| coder/websocket | WebSocket, client and server | ISC |
| zeebo/blake3 | File hashing | CC0 / Apache-2.0 |
| coturn | TURN server, step 7, not bundled | BSD-3-Clause |

Keep the list this short. Generate a third-party notices file before
distributing any binary.
