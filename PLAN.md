# Ratatoskr — P2P Backbone Plan

Module path: `github.com/achmadss/ratatoskr`

---

## 1. What this is

Two Go programs that let one machine reach files on another machine, on
the same network or across the Internet, with the file bytes travelling
directly between the two.

| Name | Role | Runs on |
|------|------|---------|
| **ratatoskr** | The agent. Owns the files. Also acts as a client. | Windows, macOS, Linux |
| **heimdall** | The rendezvous server. Introduces two agents that cannot see each other. | A small Linux VPS |

Ratatoskr is the squirrel that carries messages up and down the world
tree. Heimdall is the watchman at the bridge: he sees who is coming and
lets them across, but he never carries their luggage.

### The core rule

> Heimdall coordinates the connection. Ratatoskr carries the files.

File bytes never pass through the server on the normal path. That is what
keeps hosting cheap and what makes the design worth building.

### Out of scope for now

No web client. No UI. No installer. No account dashboard. No file index.
No uploads, deletes or renames. Read-only, command line only.

---

## 2. Two ideas that must stay separate

This is the most important paragraph in the document.

**Discovery** is how peer A learns where peer B is and swaps connection
notes with it.

**The data path** is where the file bytes actually travel.

They are decided by different machinery, and confusing them leads to bad
design. In particular:

> Being on the same LAN already gives you a direct LAN data path, even
> when discovery went through a server on the Internet.

ICE always prefers host candidates. Two peers on one LAN that signalled
through heimdall will still connect over the LAN, at LAN speed, with the
bytes never leaving the building. Step 0 already proved this: the winning
pair was `host <-> host`.

So LAN discovery is **not** what gives you LAN speed. ICE does that on
its own. LAN discovery gives you two other things, both worth having:

1. **Independence.** The connection works with the Internet unplugged,
   or with heimdall down, or with your VM's bill unpaid.
2. **Privacy.** Nothing at all leaves the network. Not the file bytes,
   which were never going to, but also not the fact that two of your
   machines just talked to each other.

Those are good enough reasons to make LAN the default path. See 4.5.
It is still built after heimdall, because heimdall is the path that
always works and the harder one to get right.

---

## 3. Device identity

Every agent has a permanent identity, created on first run. This is the
Tailscale model.

### 3.1 Not a hardware fingerprint

An identity derived from hardware (MAC address, disk serial, machine
UUID) breaks in ways that are painful and hard to debug: virtual
machines clone it, network adapters change, macOS randomises MAC
addresses, a disk swap loses it. It is also a privacy problem, because
the identity then leaks facts about the machine.

Tailscale does not do this either. A node's identity is a keypair.

### 3.2 A keypair instead

On first run the agent generates an **Ed25519 keypair**.

```
private key  ->  config dir, mode 0600, never leaves the machine
public key   ->  the identity
peer id      ->  base32(sha256(public key)[:16]), lowercase, no padding
```

That gives a 26 character id, printed in groups for readability:

```
rt-k4m2 q9xw 7bnp 3vdh 5tzy 6rfc ag
```

### 3.3 Why this shape is worth it

The peer id is a **hash of a public key**. That single fact buys three
things:

1. **The id is self-authenticating.** Anyone claiming to be
   `rt-k4m2...` can be challenged to sign a nonce. Only the holder of
   the private key can answer. An impostor cannot fake it.
2. **Heimdall does not have to be trusted.** It cannot impersonate an
   agent, and it cannot man-in-the-middle a connection, because it does
   not hold any private key. The worst it can do is refuse to introduce
   two peers.
3. **The same proof works on the LAN and over the Internet.** One auth
   mechanism, not two.

### 3.4 Mutual authentication

Right after the control channel opens, both sides prove who they are.

```
A -> B   HELLO   { peer_id, public_key, nonce_a, version }
B -> A   HELLO   { peer_id, public_key, nonce_b, version }
A -> B   AUTH    { signature over ("ratatoskr-auth-v1" || nonce_b || dtls_fingerprint) }
B -> A   AUTH    { signature over ("ratatoskr-auth-v1" || nonce_a || dtls_fingerprint) }
```

Each side checks that:

- `sha256(public_key)` really produces the claimed `peer_id`
- the signature verifies against that public key
- the peer id is in this machine's allow list

The DTLS fingerprint is included in the signed material so the proof is
bound to this specific WebRTC connection and cannot be replayed onto
another one.

Anything that fails, closes the connection. Fail closed, always.

### 3.5 The allow list

Each agent keeps a list of peer ids it will accept, in its config. For
now these are added by hand:

```
ratatoskr trust rt-k4m2q9xw7bnp3vdh5tzy6rfcag  --name "my laptop"
ratatoskr untrust rt-k4m2q9xw7bnp3vdh5tzy6rfcag
ratatoskr trusted
```

Later, an account dashboard hands out this list instead. The wire format
does not change when that happens, which is the point of doing it this
way now.

---

## 4. Discovery

Given a peer id, find a way to swap SDP with it. Two methods. **The LAN
is the default. Heimdall is the fallback.**

```
ratatoskr connect rt-k4m2...
        |
        +-- t=0     mDNS query for the peer id        no Internet needed
        |
        |           if it answers in time:
        |              -> use it, and never contact heimdall at all
        |
        +-- t=400ms heimdall rendezvous               starts only if the
        |                                             LAN has not answered
        v
   first working session wins; the loser is cancelled
```

### 4.0 Why a head start and not a strict sequence

A strict sequence — try LAN, wait for it to fail, then try heimdall —
makes every remote connection pay the full mDNS timeout before it even
begins. That is the wrong tax, because remote is the common case.

A plain race, with both starting at t=0, has the opposite problem: it
sends a packet to heimdall even when the peer is sitting on the same
switch. That breaks the independence and privacy points above.

The **head start** gets both. mDNS starts immediately. Heimdall is held
for 400 ms. On a LAN, mDNS answers in 50-200 ms, so heimdall is never
contacted. Off a LAN, the cost is 400 ms once, at connect time.

Fallback is triggered by either of two things, not just one:

- the mDNS query times out, **or**
- the peer is found on the LAN but the handshake with it fails

The second case matters. A local firewall can block the signal port
while multicast still works. Finding the peer is not the same as
reaching it.

### 4.1 LAN discovery — mDNS

Each running agent advertises itself on the local network:

```
service : _ratatoskr._udp.local
TXT     : id=<peer id>  pk=<base64 public key>  sp=<local signal port>  v=1
```

A peer looking for `rt-k4m2...` browses the service and matches on the
`id` field.

Once found, it has an IP and a port. It posts an offer straight to the
other agent's **local signal endpoint**:

```
POST http://<lan-ip>:<sp>/v1/signal     body: the SDP offer
response:                               the SDP answer
```

No Internet involved. No server involved.

Notes:

- This endpoint listens on the LAN, so it is the one piece of attack
  surface exposed to the local network. It must do nothing except
  accept an offer and return an answer. All real authentication still
  happens inside the encrypted WebRTC channel, per 3.4. A stranger on
  your café Wi-Fi can make the agent burn a few CPU cycles on a
  handshake and nothing more.
- Rate limit it hard, and cap concurrent handshakes.
- mDNS uses UDP 5353. macOS and Windows will show a firewall prompt on
  first run. Expect it.
- Many corporate and guest networks block multicast between clients.
  When that happens, discovery falls through to heimdall, which is
  exactly the intended behaviour.

### 4.2 Internet discovery — heimdall

A WebSocket rendezvous server. See section 7.

### 4.3 Why keep WebRTC even on the LAN

Once mDNS has found the peer, there is already a working HTTP connection
to it. It is tempting to just stream the file over that.

Do not. That would mean two complete file transfer implementations to
write, test, and keep in sync — chunking, backpressure, cancellation,
hashing, resume — for no gain, because ICE over the LAN already picks
host candidates and runs at wire speed.

**One data path. Always WebRTC.** mDNS and heimdall are two doors into
the same room.

### 4.5 A LAN session must stay on the LAN

Discovery choosing the LAN is not enough on its own. ICE would still
query the configured STUN servers, so packets would leave the network
even though the connection is local. That quietly undoes the whole
point.

So a session that was discovered on the LAN is built with **host
candidates only**: no STUN, no TURN, empty ICE server list.

```
discovered via LAN  ->  ICE servers: none          nothing leaves the network
discovered via net  ->  ICE servers: STUN, + TURN  normal path
```

This makes "it works with the Internet unplugged" a guarantee that can
be tested, rather than something that happens to work most of the time.

If a host-only session fails to connect, fall back to heimdall like any
other failure.

### 4.6 Where this leaves a browser

A browser cannot do mDNS. There is no web API for discovering devices on
the local network, and there will not be one. A page served over HTTPS
also cannot call a plain `http://192.168.x.x` endpoint, because that is
blocked as mixed content.

So a browser client always uses heimdall for discovery. The LAN-default
rule in this section applies to agent-to-agent connections only.

**This costs almost nothing**, because of section 2: a browser on the
same LAN as the agent still gets a direct LAN data path via ICE. Only
the few kilobytes of signalling go out to the Internet.

The one case that genuinely does not work is a browser on a LAN with no
Internet at all. Solving that means the agent serving real HTTPS on a LAN
address, which needs a public wildcard certificate and DNS pointing at
private IPs. Plex does this. It is a project of its own. It is out of
scope, and it is noted here so nobody rediscovers it as a surprise.

---

## 5. Shape

```
ratatoskr A                    heimdall                    ratatoskr B
(client)                    (WebSocket)                    (serving files)
    |                             |                             |
    |--- register + proof ------->|<-------- register + proof ---|
    |--- offer  (for B) --------->|---------- offer ----------->|
    |<-- answer ------------------|<--------- answer -----------|
    |<-> ice candidates <-------->|<------> ice candidates <---->|
    |                             |                             |
    |============ WebRTC : DTLS + SCTP DataChannels =============|
    |   "ctrl"    JSON requests and replies, reliable + ordered  |
    |   "xfer-N"  binary chunks, one channel per active download |


on the same LAN, heimdall is skipped entirely:

ratatoskr A  --- mDNS query ------>  (multicast)
             <-- TXT: id, pk, port --  ratatoskr B
             --- POST /v1/signal ---->
             <-- SDP answer ----------
             ============ WebRTC ============
```

Heimdall holds one piece of state: which peer id is on which socket. It
sees no file names, no file contents, no directory listings.

---

## 6. Transport

WebRTC DataChannels, via [Pion](https://github.com/pion/webrtc) on both
ends. Pion is pure Go.

ICE order of preference:

1. Host candidates — same LAN, fastest
2. Server-reflexive via STUN — the normal Internet case
3. Relay via TURN — only when the network blocks everything else

The agent must log which candidate pair actually won, so "direct" versus
"relay" is a measured fact and not a guess.

---

## 7. Signaling protocol — heimdall

One WebSocket endpoint. JSON messages. The server relays opaque blobs and
understands almost nothing.

```
client -> server   { "type": "register", "peer_id": "...", "public_key": "...",
                     "nonce": "...", "signature": "..." }
server -> client   { "type": "registered" }

client -> server   { "type": "offer",  "to": "<peer>", "sdp": "..." }
server -> peer     { "type": "offer",  "from": "<peer>", "sdp": "..." }

client -> server   { "type": "answer", "to": "<peer>", "sdp": "..." }
server -> peer     { "type": "answer", "from": "<peer>", "sdp": "..." }

client -> server   { "type": "ice",    "to": "<peer>", "candidate": {...} }
server -> peer     { "type": "ice",    "from": "<peer>", "candidate": {...} }

server -> client   { "type": "peer_gone",  "peer": "<peer>" }
server -> client   { "type": "not_found",  "peer": "<peer>" }
server -> client   { "type": "error", "code": "...", "message": "..." }
```

Rules:

- `register` must carry a signature over a server-issued nonce. Heimdall
  verifies that `sha256(public_key)` equals the claimed `peer_id` and
  that the signature checks out. This stops anyone from squatting on
  another peer's id.
- Ping/pong every 20 seconds. Drop a socket that misses two.
- A `register` for a peer id that is already connected replaces the old
  socket. This is what makes agent restart work.
- Rate limit per socket and per IP. Signaling is cheap to abuse.
- Heimdall does **not** decide who may talk to whom. That is the agent's
  allow list, checked inside the encrypted channel. Heimdall being
  compromised must not grant anyone file access.
- Later, heimdall also mints short-lived TURN credentials. Never ship a
  static TURN password.

---

## 8. Wire protocol

Two channels, two formats. Never mix them.

### 8.1 Control channel — label `ctrl`

Reliable, ordered. One JSON object per message.

```
HELLO       -> HELLO           identity and version exchange
AUTH        -> AUTH            signed nonce, both directions
PING        -> PONG            liveness and round-trip time
LIST        -> LIST_RESULT     directory listing
STAT        -> STAT_RESULT     one entry
OPEN        -> OPEN_OK         begin a transfer; returns transfer_id, size, hash
CREDIT                         receiver grants N more chunks
CANCEL                         stop a transfer
ERROR                          code + safe message
```

Every request carries `version`, `request_id`, `type`.
Every reply echoes `request_id`.
No message other than `HELLO` and `AUTH` is served before auth completes.

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

### 8.2 Transfer channel — label `xfer-<transfer_id>`

Reliable, ordered. Binary frames, no JSON.

```
bytes 0..3   uint32 big-endian sequence number
bytes 4..N   payload, up to 16 KB
```

An empty frame means end of file. Then the channel closes.

`OPEN_OK` carries the size and a BLAKE3 hash of the whole file so the
receiver can verify what it got.

### 8.3 Limits, enforced by the serving side

- Control message larger than 64 KB → close the connection
- More than 4 concurrent transfers per peer → refuse with `ERROR`
- More than 8 connected peers → refuse the connection
- Unknown message type → reply `ERROR`, do not close
- `ERROR` never contains a real filesystem path or a stack trace

---

## 9. Backpressure

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

## 10. Path safety

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

## 11. Local control API

The agent runs an HTTP server on `127.0.0.1` on a random free port. It
writes the port and a random token to a state file, mode `0600`:

| OS | Path |
|----|------|
| Linux | `~/.config/ratatoskr/` |
| macOS | `~/Library/Application Support/ratatoskr/` |
| Windows | `%AppData%\ratatoskr\` |

In Go: `os.UserConfigDir()`. The directory holds `identity.key` (0600),
`config.json`, and `control.json`.

Every other CLI subcommand is an HTTP client against this API. So is any
future UI. Bind to `127.0.0.1` only, never `0.0.0.0`. Require the token
in an `Authorization` header.

This is the single most valuable early decision: it means a UI can be
written later in any language, and choosing that language costs nothing
today.

```
GET  /v1/status        peer id, signaling state, discovered LAN peers,
                       connected peers, connection type per peer
GET  /v1/folders   POST /v1/folders   DELETE /v1/folders
GET  /v1/trusted   POST /v1/trusted   DELETE /v1/trusted
GET  /v1/peers
GET  /v1/discover      what mDNS can currently see
GET  /v1/events        server-sent events, for live status
```

Note the difference from the LAN signal endpoint in 4.1: the control API
is `127.0.0.1` and full power. The LAN signal endpoint is reachable by
the network and can do exactly one thing.

---

## 12. CLI surface

```
ratatoskr run                     start the agent in the foreground
ratatoskr id                      print this device's peer id and public key
ratatoskr status                  online state, peers, direct or relay
ratatoskr discover                list ratatoskr agents on this network
ratatoskr peers                   currently connected peers

ratatoskr folders list | add PATH | remove PATH
ratatoskr trusted     | trust ID [--name N] | untrust ID

ratatoskr connect PEER_ID ls PATH         client mode: list a directory
ratatoskr connect PEER_ID get PATH OUT    client mode: download a file
ratatoskr connect PEER_ID --via lan|net   force one discovery method
ratatoskr version
```

`run` is the process. Everything else is a thin HTTP client against a
running `run`.

`connect` is the client side. It exists so the whole system can be tested
with two agents and no browser. Later it becomes machine-to-machine
transfer, which is a feature in its own right.

`--via` exists so tests can prove each discovery path separately instead
of guessing which one won. `--via lan` also means "fail rather than fall
back", which is what makes the Internet-unplugged test meaningful.

```
heimdall serve --addr :8080
```

---

## 13. Build and platform rules

**The agent must stay free of cgo.** Pion is pure Go. Keep it that way
and one machine builds every target:

```
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/ratatoskr.exe ./cmd/ratatoskr
```

Consequences to respect:

- No Go GUI toolkit inside the agent. All of them need cgo. A UI, if it
  ever happens, is a separate binary that talks to the control API.
- No tray icon in the agent. Same reason.
- The mDNS library must be pure Go. This rules out anything wrapping
  Apple's Bonjour or Avahi via cgo.
- If SQLite is ever needed, use `modernc.org/sqlite` (pure Go), not
  `mattn/go-sqlite3`.

---

## 14. Repository layout

```
ratatoskr/
├── cmd/
│   ├── ratatoskr/        the agent CLI
│   └── heimdall/         the rendezvous server
├── internal/
│   ├── identity/         keypair, peer id, sign and verify
│   ├── protocol/         wire messages, shared by both binaries
│   ├── transport/        Pion, ICE, channels, backpressure
│   ├── discovery/        the Discovery interface and the race
│   │   ├── lan/          mDNS advertise, browse, local signal endpoint
│   │   └── net/          heimdall client
│   ├── signal/           heimdall server logic
│   ├── fsroot/           allowed roots, path validation
│   ├── control/          127.0.0.1 HTTP API and control.json
│   └── config/           per-OS paths, folder list, allow list
├── Makefile
├── PLAN.md
├── TODO.md
└── go.mod
```

One module, two binaries. They share `internal/protocol` and
`internal/identity`, so the formats cannot drift apart. That is the
reason to keep them together.

`internal/discovery` defines one small interface with two
implementations. Nothing above it knows whether a connection was found on
the LAN or through heimdall — it only asks for a signalling session with
a peer id.

---

## 15. Build order

Each step has a check you can actually run. Do not move on early.

| Step | Goal | Passes when |
|------|------|-------------|
| 0 ✅ | Two agents, offer and answer pasted by hand | a string echoes back over a DataChannel |
| 1 | Device identity: keypair, peer id, config dir | `ratatoskr id` prints a stable id across restarts, on all three OSes |
| 2 | Mutual auth over `ctrl` | an untrusted peer id is refused; a tampered signature is refused |
| 3 | heimdall rendezvous over the Internet | `ratatoskr connect <id>` links up with no pasting |
| 4 | mDNS discovery and the local signal endpoint | `ratatoskr discover` lists the other machine; `connect --via lan` works with the router's Internet unplugged |
| 5 | LAN-first discovery with a heimdall head start | on a LAN, heimdall is never contacted at all; multicast blocked falls through; a found-but-unreachable peer falls through too |
| 6 | Control protocol: PING, LIST, STAT | a real listing prints; `../../etc/passwd` is refused |
| 7 | Local control API | `ratatoskr status` talks to a running `ratatoskr run` |
| 8 | Transfer one small file | a 10 MB file arrives, hash matches |
| 9 | Backpressure and a large file | 10 GB transfers, memory flat on both sides |
| 10 | Real NAT across the Internet | direct connection, proven by the logged candidate pair |
| 11 | TURN fallback with coturn | 1 GB transfers with STUN disabled |
| 12 | Survival | sleep, wake, network change, restart, cancel — never hangs |

Step 9 is the milestone that proves the project works.
Step 10 is the first step that can genuinely fail.

---

## 16. Risks

| Risk | Likely | Response |
|------|--------|----------|
| TURN relay rate much higher than 20% | Medium | Measure it from step 10. It is the number that decides whether hosting stays cheap |
| mDNS blocked on the networks you actually use | Medium | Expected. Falls through to heimdall after the head start. Measure how often LAN discovery wins |
| Firewall prompts on first run confuse users | High | Two prompts, UDP 5353 and the local signal port. A packaging problem, note it now |
| The LAN signal endpoint becomes an attack surface | Medium | It does exactly one thing and grants nothing. Real auth is inside the encrypted channel. Rate limit and fuzz it |
| Windows path edge cases open a hole | Medium | Treat `internal/fsroot` as security code. Table-driven tests with hostile inputs |
| Throughput disappoints on long-distance links | Medium | SCTP is latency sensitive. Test one intercontinental hop early |
| Corporate networks block UDP entirely | Certain for some users | coturn on TCP and TLS port 443. A config, not a rewrite |
| Upload speed of the serving machine is the real limit | Certain | Nothing to fix. Report it honestly |

---

## 17. Dependencies

| Project | Role | License |
|---------|------|---------|
| pion/webrtc | WebRTC in Go, both binaries | MIT |
| coder/websocket | WebSocket, client and server | ISC |
| a pure-Go mDNS library | LAN discovery — candidates: `libp2p/zeroconf/v2`, `grandcat/zeroconf`, `pion/mdns`. Pick at step 4 | MIT / Apache-2.0 |
| zeebo/blake3 | File hashing | CC0 / Apache-2.0 |
| coturn | TURN server, step 11, not bundled | BSD-3-Clause |

Ed25519, SHA-256 and base32 all come from the standard library. No
dependency needed for identity.

Keep the list this short. Generate a third-party notices file before
distributing any binary.

---

## 18. Reserved for later

Written down so the wire format does not have to change when they arrive.

- **Account pairing.** A dashboard hands the agent a claim token; the
  agent registers its public key against an account; the account hands
  out the allow list. Nothing in sections 3, 7 or 8 changes.
- **Browser client.** A third kind of peer speaking the same protocol.
  Discovery via heimdall only, per 4.4.
- **Browser on an offline LAN.** Needs real TLS on a private address.
  A project of its own.
