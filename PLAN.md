# Ratatoskr — Plan

Module path: `github.com/achmadss/ratatoskr`

---

## 1. What this is

A personal file system that stays on your own machines, reachable from a
web or mobile app, where the file bytes travel directly between your
device and your machine.

### The pieces

| Name | What it is | Runs on | Language |
|------|-----------|---------|----------|
| **ratatoskr** | The agent. Owns the files. Can also act as a client. | Windows, macOS, Linux | Go |
| **heimdall** | Rendezvous. Introduces two peers that cannot see each other. Holds no data. | Linux VPS | Go |
| **mimir** | Control plane. Accounts, device registry, presence, access grants. Has the database. | Linux VPS | Go |
| **web app** | Browser file manager | anywhere | TypeScript |
| **mobile app** | iOS and Android file manager | phones | later |

Ratatoskr is the squirrel that carries messages up and down the world
tree. Heimdall is the watchman at the bridge: he sees who is coming and
lets them across, but never carries their luggage. Mimir is the well of
knowledge: he remembers who you are and what is yours.

### Words used precisely

This document has a lot of nouns. They are not interchangeable.

| Word | Means |
|------|-------|
| **account** | A person's login on mimir. Owns devices and clients. |
| **device** | A machine that holds files. One agent runs on it. Identified by a peer id. |
| **agent** | The `ratatoskr` process on a device. The side that **has** the files. |
| **client** | One *installation* of something that **asks** a device for files. |
| **peer** | Either end of a WebRTC connection. An agent is a peer; so is a client. |
| **peer id** | `rt-…`, the hash of a public key. Names a device, or a client. |
| **root** / **share** | A folder an agent offers, with a mode of `ro` or `rw`. |
| **grant** | Mimir's signed note: this client may use these folders on this device, until this time. |
| **ticket** | Mimir's signed note letting a client open a heimdall socket. |

The line that matters:

> **agent = the side that has the files. client = the side that asks.**

### What a "client" is, exactly

A client is **one installation**, not a person and not a machine.

```
your laptop, Chrome            client 1
your laptop, Firefox           client 2      same laptop, same you
your phone app                 client 3
your phone app, reinstalled    client 4      client 3 is now dead
`ratatoskr connect` from your laptop to your desktop   client 5
```

Each one generates its own keypair on first use and registers the public
half with mimir. That is what makes it a client rather than just a
session.

Three consequences, and they are the reason it is a first-class thing:

1. **Revocation is per install.** Losing a phone means killing client 3
   without logging out of your desktop browser.
2. **A stolen session cookie is not enough.** The private key cannot be
   exported, so an attacker with your cookie still cannot prove they are
   a registered client.
3. **An agent can be a client too.** `ratatoskr connect` makes your
   laptop a client of your desktop. It reuses its own `identity.key`
   rather than making a new one, so it appears as both a device and a
   client.

Clearing browser data destroys that client's key. That is a re-pair, not
a disaster — but it does mean web clients are disposable and mobile
clients are long-lived. Expect the client list to accumulate dead web
entries, and give the user a way to tidy them.

### The core rule, unchanged

> Mimir remembers. Heimdall introduces. Ratatoskr carries the files.

File bytes never pass through any server on the normal path. That is what
keeps hosting cheap and what makes the whole design worth building.

---

## 2. Who talks to whom

```
                    ┌──────────────────────────────────────┐
                    │             mimir                    │
   login,           │  accounts · devices · presence       │
   device list,     │  access grants · signal tickets      │
   status           │  (the only thing with a database)    │
        ┌──────────►│                                      │
        │           └───────────────┬──────────────────────┘
        │                           │ presence feed
        │                           │ grant public key
        │           ┌───────────────▼──────────────────────┐
        │           │            heimdall                  │
        │  ticket   │  WebSocket rendezvous · no database   │
        │  ┌───────►│  relays SDP and ICE, nothing else     │
        │  │        └───────────────┬──────────────────────┘
        │  │                        │
┌───────┴──┴────┐                   │            ┌──────────────┐
│  web app      │                   └───────────►│  ratatoskr   │
│  mobile app   │                                │  agent       │
└───────┬───────┘                                └──────┬───────┘
        │                                               │
        │═══════════ WebRTC : DTLS + SCTP ══════════════│
        │   ctrl    JSON requests and replies           │
        │   xfer-N  binary chunks, one per transfer     │
        └───────────────────────────────────────────────┘
```

Three facts to hold on to:

1. The frontend only ever asks **mimir** questions. It never talks to
   heimdall except to open one WebSocket with a ticket mimir gave it.
2. Neither mimir nor heimdall can read your files. Neither holds a key
   that would let them. Section 4 is how that is enforced.
3. The heavy line at the bottom is the only path file bytes take.

---

## 3. Two ideas that must stay separate

The most important paragraph in this document.

**Discovery** is how a client learns where an agent is and swaps
connection notes with it.

**The data path** is where the file bytes actually travel.

> Being on the same LAN already gives a direct LAN data path, even when
> discovery went through a server on the Internet.

ICE always prefers host candidates. A phone and an agent on one Wi-Fi
that signalled through heimdall still connect over the Wi-Fi, at LAN
speed, with the bytes never leaving the building. Step 0 already proved
this: the winning pair was `host <-> host`.

So LAN discovery is not what buys LAN speed. It buys two other things:
working with the Internet down, and not telling a server that two of your
own machines just talked. Both are worth having. See section 7.

---

## 4. Identity and authorisation

Three kinds of key. Nothing on a server is one of them.

### 4.1 Agent identity

On first run the agent generates an **Ed25519 keypair**.

```
private key  ->  config dir, mode 0600, never leaves the machine
peer id      ->  "rt-" + base32(sha256(pubkey)[:16]), lowercase, no padding
```

Printed in groups: `rt-k4m2 q9xw 7bnp 3vdh 5tzy 6rfc ag`

**Not a hardware fingerprint.** MAC addresses, disk serials and machine
UUIDs break when a VM is cloned, a disk is swapped, or macOS randomises
the MAC — and they leak facts about the machine. Tailscale does not use
them either. A node is a keypair.

Because the id is a hash of the public key, the id proves itself. Anyone
claiming it can be challenged to sign a nonce, and only the real holder
can answer.

### 4.2 Client identity

Every frontend install also gets a keypair.

| Kind of client | Where the private key lives |
|----------------|----------------------------|
| Web | WebCrypto Ed25519, **non-extractable**, stored in IndexedDB |
| Mobile | Keychain (iOS) / Keystore (Android), hardware backed |
| An agent acting as a client | the same `identity.key` as 4.1 — it does not make a second one |

Non-extractable means JavaScript can ask the browser to sign with the
key but can never read the key itself. A stolen session token is then
not enough to impersonate the device.

Clearing browser data destroys the key. That is a re-pair, not a
disaster. Treat a web client as disposable and a mobile client as
long-lived.

### 4.3 Access grants — how the agent decides who may in

The agent must not have to phone home to authorise a request. So mimir
signs a short document and the client carries it.

Mimir has its own signing keypair. When the agent is paired to an
account, it pins **mimir's public key**.

```json
{
  "v": 1,
  "account": "acc_7f3a",
  "device":  "rt-k4m2q9xw7bnp3vdh5tzy6rfcag",
  "client":  "<client public key>",
  "scopes":  { "/Documents": "rw", "/Videos": "ro" },
  "issued":  "2026-09-06T10:00:00Z",
  "expires": "2026-09-06T11:00:00Z",
  "sig":     "<mimir's signature over the above>"
}
```

The agent checks, in this order:

1. The signature verifies against mimir's pinned public key
2. `device` is this agent's own peer id
3. `expires` has not passed, allowing small clock skew
4. `account` matches the account this agent is paired to
5. The peer that just authenticated on the control channel holds the
   private key for `client`

Any failure closes the connection. Fail closed, always.

**Why this shape.** Mimir cannot read files: it holds no agent key and
no client key, and it is not on the data path. Heimdall cannot either,
and it never even sees a grant. A stolen grant expires in an hour and is
useless without the client's private key, which cannot be exported.

**Offline.** A grant is cached and reused until it expires. So a phone
that was online an hour ago still works on a LAN with the Internet down.
Grants for the mobile app can be issued with a longer life for exactly
this reason. A fully offline first connection is not possible, and that
is the honest limit of the model.

### 4.4 Mutual authentication on the wire

Right after the control channel opens, before anything else:

```
A -> B   HELLO  { peer_id, public_key, nonce, version }
B -> A   HELLO  { peer_id, public_key, nonce, version }
A -> B   AUTH   { sig over "ratatoskr-auth-v1" || peer nonce || DTLS fingerprint,
                  grant: {...} }
B -> A   AUTH   { sig ... }
```

The DTLS fingerprint is inside the signed material, so a captured proof
cannot be replayed onto a different connection.

### 4.5 The local trust list

The agent also keeps a hand-managed allow list, independent of mimir.
This is the development and self-hosting path, and it is what makes step
2 testable long before mimir exists.

```
ratatoskr trust rt-...  --name "my laptop"
ratatoskr untrust rt-...
ratatoskr trusted
```

An agent accepts a peer if **either** the local list allows it or a valid
grant covers it.

---

## 5. Mimir — the control plane

A normal HTTP service with a database. This is the only stateful piece.

### 5.1 What it stores

```
accounts        id, email, auth
devices         id (peer id), account, public key, name, os, created, last_seen
clients         id, account, public key, kind (web|ios|android|agent),
                name, created, last_seen        one row per installation
shares          device, path, mode (ro|rw)          what a device offers
grants          issued grants, for revocation and audit
```

### 5.2 API for the frontend

All routes need a logged-in session.

```
POST /v1/auth/...                 login, whatever you already use

GET  /v1/devices                  requirement 1: list my machines
GET  /v1/devices/{id}             requirement 2: one machine's status
POST /v1/devices/{id}/session     get a signal ticket + a grant
DELETE /v1/devices/{id}           unpair

POST /v1/pair                     redeem a pairing code from an agent
GET  /v1/clients                  this account's registered clients
DELETE /v1/clients/{id}           revoke a client
```

`GET /v1/devices` returns:

```json
[{
  "id": "rt-k4m2q9xw7bnp3vdh5tzy6rfcag",
  "name": "Work Laptop",
  "os": "darwin",
  "presence": "online",
  "last_seen": "2026-09-06T10:59:12Z",
  "shares": [ {"path": "/Documents", "mode": "rw"} ],
  "network_hint": "same_network"
}]
```

`POST /v1/devices/{id}/session` returns:

```json
{
  "signal_url": "wss://heimdall.example.com/v1/ws",
  "ticket": "<short-lived, signed, names this client and this device>",
  "grant": { ... },
  "ice_servers": [ {"urls": ["stun:..."]},
                   {"urls": ["turn:..."], "username": "...", "credential": "..."} ]
}
```

TURN credentials are minted here, per session, short-lived. A static TURN
password in a shipped app gets scraped and turns your relay into a free
one for the Internet.

### 5.3 Presence

Heimdall knows who is connected, because it holds the sockets. Mimir
needs that fact but must not hold the sockets itself.

Heimdall pushes presence changes to mimir on an internal endpoint:
`connected`, `disconnected`, plus a full reconciliation sweep every 30
seconds so a missed event self-heals. Mimir stores `presence` and
`last_seen`.

### 5.4 Deployment note

Mimir and heimdall are separate services with separate concerns: mimir is
a CRUD app with a database, heimdall is a connection holder with none.
They may run on the same box, and probably should at first. Keep the code
boundary clean anyway, because they scale on different things.

---

## 6. Status, honestly reported

Requirement 2 asks whether a machine is reachable and by what method.
Those are two different questions with two different answers, and mixing
them produces a UI that lies.

### 6.1 Before you connect — presence

From mimir. Cheap, and the answer to "should I even show this as
clickable".

```
online     the agent has a live socket to heimdall
offline    it does not
unknown    heimdall was unreachable, so we genuinely do not know
```

Plus one hint, and it is only a hint:

```
network_hint: same_network | different_network | unknown
```

Derived by comparing the public IP heimdall sees for the client with the
one it sees for the agent. Same public IP usually means the same NAT,
which usually means a LAN connection is available. It is a guess. Never
present it as a fact.

### 6.2 After you connect — the measured path

Only a real connection can answer this, because it is decided by ICE at
connect time.

```
lan       discovered by mDNS, host candidates only, nothing left the network
direct    a direct path found via STUN, possibly still over the LAN
relay     going through TURN; your server is paying for these bytes
```

This comes from the winning ICE candidate pair, never from a guess. It
must be logged per session, because the relay share is the number that
decides what this system costs to run.

### 6.3 What the UI shows

```
Work Laptop        ● Online          (before connecting)
Work Laptop        ● Online · Local network        (connected, lan or direct-host)
Work Laptop        ● Online · Direct               (connected, direct)
Work Laptop        ● Online · Relayed — slower     (connected, relay)
Work Laptop        ○ Offline
```

Never show the words ICE, STUN, TURN, SDP or candidate outside a
diagnostics screen.

---

## 7. Discovery

Given a device id, find a way to swap SDP with it.

### 7.1 What each client can actually do

| Client | LAN discovery | Notes |
|--------|--------------|-------|
| Agent | yes | mDNS, no restrictions |
| Mobile | yes | iOS needs the local network permission and the multicast entitlement; Android uses NSD |
| Web | **no** | No browser API exists for this, and a page on HTTPS cannot call `http://192.168.x.x`. Heimdall always |

So the web app always signals through heimdall — and, per section 3, it
still gets a LAN-speed data path when it is on the same network. Only a
few kilobytes go out.

### 7.2 LAN first, with a head start

For clients that can do mDNS:

```
connect rt-k4m2...
  |
  +-- t=0      mDNS query for the device id           no Internet needed
  |            answers in 50-200 ms -> use it, and never contact heimdall
  |
  +-- t=400ms  heimdall rendezvous                    only if the LAN is quiet
```

A strict sequence would make every remote connection wait out the full
mDNS timeout first — the wrong tax on the common case. A plain race would
send a packet to heimdall even for the laptop on the same switch. The
head start avoids both.

Fall back to heimdall on either trigger: mDNS timed out, **or** the peer
was found on the LAN but the handshake with it failed. A local firewall
can block the signal port while multicast still works. Finding is not
reaching.

### 7.3 mDNS details

```
service : _ratatoskr._udp.local
TXT     : id=<peer id>  pk=<base64 public key>  sp=<local signal port>  v=1
```

Once found, the client posts an offer straight to the agent:

```
POST http://<lan-ip>:<sp>/v1/signal    body: SDP offer
response:                              SDP answer
```

The endpoint does exactly one thing and grants nothing. All real
authentication happens inside the encrypted WebRTC channel, per 4.4. Rate
limit it hard and cap concurrent handshakes.

UDP 5353 means a firewall prompt on macOS and Windows at first run.
Expect two prompts, this one and the signal port.

### 7.4 A LAN session must stay on the LAN

Choosing the LAN is not enough by itself. ICE would still query the
configured STUN servers, so packets would leave the network anyway.

A session discovered on the LAN is built with **host candidates only**:
empty ICE server list, no STUN, no TURN.

```
found on the LAN   ->  ICE servers: none          nothing leaves the network
found via heimdall ->  ICE servers: STUN + TURN   normal path
```

This turns "works with the Internet unplugged" into something testable
rather than something that happens to work.

### 7.5 Keep one data path

After mDNS there is already a working HTTP connection to the agent, and
it is tempting to stream files over it. Do not. That means two complete
transfer implementations — chunking, backpressure, cancel, hashing,
resume — to write and keep in sync, for no gain, because ICE over the LAN
already runs at wire speed.

**One data path. Always WebRTC.** mDNS and heimdall are two doors into
the same room.

---

## 8. Heimdall — rendezvous

One WebSocket endpoint. It relays opaque blobs and understands almost
nothing.

```
client -> server   { "type": "register", "ticket": "..." }        frontends
client -> server   { "type": "register", "peer_id": "...",        agents
                     "public_key": "...", "nonce": "...", "signature": "..." }
server -> client   { "type": "registered" }

client -> server   { "type": "offer"|"answer"|"ice", "to": "<peer>", ... }
server -> peer     { "type": "offer"|"answer"|"ice", "from": "<peer>", ... }

server -> client   { "type": "peer_gone" | "not_found" | "error", ... }
```

Rules:

- An agent registers by signing a server-issued nonce. Heimdall checks
  that `sha256(public_key)` equals the claimed peer id, so nobody can
  squat on another device's id.
- A frontend registers with a ticket from mimir.
- Ping/pong every 20 s; drop a socket that misses two.
- Re-`register` of a live peer id replaces the old socket. This is what
  makes agent restart work.
- Rate limit per socket and per IP.
- Heimdall does **not** decide who may open files. That is the grant,
  checked inside the encrypted channel. Heimdall being fully compromised
  must not grant anyone a single byte.
- Push presence changes to mimir; reconcile every 30 s.

---

## 9. The file protocol

Two channels, two formats. Never mix them.

### 9.1 Control channel — label `ctrl`

Reliable, ordered. One JSON object per message. Every request carries
`version`, `request_id`, `type`; every reply echoes `request_id`.

```
session
  HELLO, AUTH            identity, grant, version. Nothing else is served first
  PING -> PONG           liveness and round-trip time

read
  LIST  -> LIST_RESULT   directory listing, paged
  STAT  -> STAT_RESULT   one entry
  READ  -> READ_OK       start a download; returns transfer_id, size, hash

write
  WRITE -> WRITE_OK      start an upload; client then sends on the xfer channel
  MKDIR -> OK
  MOVE  -> OK            rename, or move within or between writable roots
  COPY  -> COPY_OK       server-side copy, no round trip through the client
  DELETE-> OK

both
  CREDIT                 receiver grants N more chunks
  CANCEL                 stop a transfer
  ERROR                  code + safe message
```

`COPY` earns its place: copying a 4 GB file inside one folder should not
mean downloading and re-uploading it.

### 9.2 Transfer channel — label `xfer-<transfer_id>`

Reliable, ordered. Binary frames, no JSON. Used in both directions:
download and upload are the same code with the roles swapped.

```
bytes 0..3   uint32 big-endian sequence number
bytes 4..N   payload, up to 16 KB
```

An empty frame means end of stream, then the channel closes. `READ_OK`
and the reply to a completed `WRITE` both carry a BLAKE3 hash so each
side can verify what it got.

### 9.3 Limits, enforced by the agent

- Control message over 64 KB → close the connection
- More than 4 concurrent transfers per peer → `ERROR`
- More than 8 connected peers → refuse
- Unknown message type → `ERROR`, do not close
- `ERROR` never contains a real filesystem path or a stack trace

---

## 10. Operation classes — what needs a peer, and what does not

The goal of this section: **make the way you reach a machine independent
of the frontend that uses it**, the way WebDAV separates Finder from the
server behind it.

### 10.1 The layers

```
┌───────────────────────────────────────────────────────┐
│  Frontend        web app · mobile app · Finder · CLI  │
├───────────────────────────────────────────────────────┤
│  File API        one stable set of WebDAV-shaped verbs │  ← never changes
├───────────────────────────────────────────────────────┤
│  Transport       how a request reaches the agent       │  ← swappable
│                  webrtc · lan-http · loopback          │
├───────────────────────────────────────────────────────┤
│  Discovery       mDNS · heimdall                       │
└───────────────────────────────────────────────────────┘
```

A frontend calls the File API and never learns which transport answered.
`ratatoskr mount` from 21.3 is then just a local WebDAV server sitting on
top of the same File API — Finder talks WebDAV to `127.0.0.1`, and the
transport underneath is WebRTC to a laptop three countries away.

### 10.2 The four classes

Every operation a file manager performs falls into one of four groups.

| Class | Talks to | Typical size | Needs a peer connection? |
|-------|----------|--------------|--------------------------|
| **A. Account** | mimir, plain HTTPS | ~1 KB | **No.** Never touches the machine |
| **B. Session** | the agent | ~5 KB | Yes, once per session |
| **C. Metadata** | the agent | ~1 KB per call | Yes, but cheap on any path |
| **D. Bulk** | the agent | **MB to GB** | Yes, and the path decides the cost |

The split people expect is "HTTP versus P2P". That is not the real line.
The real lines are:

> **A never touches your machine. B, C and D all need a peer connection —
> because a laptop behind CGNAT cannot be reached by an HTTP call.**
>
> **Only class D cares which path it got.**

### 10.3 Class A — account plane. Plain HTTPS to mimir

Ordinary web requests. No peer, no WebRTC, works before any machine is
even online.

```
log in, log out, refresh session
list my devices                       requirement 1
device presence and last seen         requirement 2, the "before" half
list a device's shared folders and their modes
pair a new device · unpair a device
list registered clients · revoke one
open a session: signal ticket, access grant, ICE servers
```

### 10.4 Class B — session setup. Once, then reused

Paid once when you click a machine, then every later call is cheap.

```
discover the agent          mDNS on the LAN, or heimdall
ICE handshake               offer, answer, candidates
HELLO and AUTH              mutual signature check, grant verification
list roots and their modes  ro or rw
```

Budget: 50-200 ms on a LAN, 300 ms to 2 s over the Internet. Keep the
session open while the user is browsing. Do not tear it down between
clicks.

### 10.5 Class C — metadata. Small, and fine even over a relay

```
LIST      directory listing, paged           ~200 bytes + ~120 per entry
STAT      one entry                          ~200 bytes
MKDIR     create a directory                 ~150 bytes
MOVE      rename, or move within the shares  ~200 bytes
COPY      server-side copy                   ~200 bytes
DELETE    remove a file or directory         ~150 bytes
DF        free space on a share              ~100 bytes
HASH      checksum an existing file          ~150 bytes
SEARCH    runs on the agent, returns matches ~1-50 KB
```

These need a peer connection, but they are **too small for the path to
matter**. A thousand directory listings over a relay is a few megabytes.
Let them relay without a second thought.

Two of these deserve attention because they look like class D and are
not:

- **COPY is server-side.** Duplicating a 4 GB file inside a share costs
  about 200 bytes on the wire. If the frontend ever implements copy as
  "download then upload", you have thrown away the entire benefit.
- **MOVE is a rename.** Same thing. Moving 100 GB between two folders on
  one disk must never move a single byte across the network.

### 10.6 Class D — bulk. The only class that costs money

```
READ        download a whole file
READ range  a byte range — media seek, resume, previews
WRITE       upload a file
WRITE at    resume an interrupted upload from an offset
THUMBNAIL   generated on the agent, returns a small image
```

This is where the direct-versus-relay distinction earns its place:

```
lan or direct   free, fast, and your VPS never knows it happened
relay           every byte is paid for twice on your TURN bill
```

So class D, and only class D, gets the extra machinery: chunking,
backpressure and credits (13), progress and cancel, hashing, and resume.

**THUMBNAIL is class D but behaves like class C.** Ask the agent for a
256 px preview and 20 KB comes back instead of a 12 MB photo. A gallery
view that fetches full images instead is a bandwidth disaster on a relay
and a slow scroll everywhere else. Build the agent side of this early.

**READ range** is not optional. Without it there is no video seeking, no
resuming a failed download, and no cheap file-type sniffing.

### 10.7 Class E — not an operation at all

Worth naming so nobody builds a round trip for them:

```
breadcrumbs and back navigation   cached listings
sort, filter, search-in-view      in the frontend, on data already fetched
file type icons                   in the frontend, from the name
selection, rename-in-progress UI  in the frontend
recently opened                   in the frontend, or mimir
```

### 10.8 What this means for the product

- A relay connection is perfectly usable for **browsing**. Only transfers
  are expensive. So "Relayed" in the UI should read as *slower
  downloads*, not *broken*.
- Metering and warnings belong on class D alone.
- If WebRTC fails entirely, a future degraded mode could carry classes B
  and C through an application-level relay on heimdall at almost no cost,
  and simply refuse class D. That would be a real fallback, not a
  pretence. Not now — noted because the class split makes it possible.

### 10.9 The transport interface

One interface, in both Go and TypeScript. Nothing above it knows how the
bytes travel.

```go
type Transport interface {
    // class B, C: one small request, one small reply
    Call(ctx context.Context, req Request) (Response, error)

    // class D: a stream in either direction, with backpressure
    OpenStream(ctx context.Context, t TransferID) (Stream, error)

    Path() Path      // lan | direct | relay — measured, never guessed
    Close() error
}
```

Planned implementations:

| Implementation | Used by | Status |
|----------------|---------|--------|
| `webrtc` | everything | v1. The only one that works everywhere |
| `loopback` | tests, and `ratatoskr connect` to itself | v1, and it makes the whole file layer testable with no network |
| `lan-http` | native clients on the same LAN | later. Simpler and lower latency than WebRTC for class C, but useless in a browser, which cannot call `http://192.168.x.x` from an HTTPS page |

**For v1 there is exactly one real transport, and that is deliberate.**
ICE already gives a LAN-direct path, so a second LAN transport would add
maintenance for a small latency win. The interface exists so that adding
one later is a new file rather than a rewrite.

---

## 11. Write operations, carefully

This is where a bug destroys a user's data instead of merely failing. It
gets its own rules.

### 10.1 Permission

Every shared root has a mode, `ro` or `rw`. A write to an `ro` root is
refused before any path work happens. The grant's `scopes` can narrow a
root further but never widen it: an `ro` root stays `ro` even if a grant
says `rw`.

### 10.2 Atomic writes

Never write in place. A dropped connection must not leave a half file
where a good one used to be.

```
upload to  <dir>/.ratatoskr-tmp-<transfer id>
fsync the file
fsync the directory
rename over the target        (atomic on the same filesystem)
```

Clean up stale temp files on startup.

### 10.3 Rules for each verb

- **MKDIR** — no `-p` by default. Refuse if the parent is missing, so a
  typo does not silently build a tree.
- **MOVE** — validate source **and** destination independently against
  the roots. Both must be `rw`. Refuse a move that crosses a filesystem
  boundary unless it is implemented as copy-then-delete with a hash
  check. Never overwrite without `overwrite: true`.
- **DELETE** — never recursive unless `recursive: true` is explicit.
  Never follow a symlink out of the root: delete the link, not its
  target. Refuse to delete a share root itself.
- **WRITE** — check free space before starting. Enforce a size cap.
  Refuse if the target exists unless `overwrite: true`.
- **COPY** — same destination rules as MOVE.

### 10.4 Order of scope

Uploads and deletes are not in the first working version. Get read,
LIST and STAT solid first, because every write verb reuses the same path
validation, and it is much cheaper to find a hole there while nothing can
be destroyed.

---

## 12. Path safety

Every filesystem call, no exceptions:

```
requested path
   -> reject if it contains a null byte
   -> filepath.Clean
   -> join to the allowed root
   -> filepath.EvalSymlinks       resolves symlinks and junctions
   -> verify the result is still inside the root
   -> open
```

`EvalSymlinks` is the step that stops a symlink pointing out of the
share. Check the **resolved** path, not the requested one.

For writes there is an extra trap: the destination usually does not exist
yet, so `EvalSymlinks` cannot resolve it. Resolve the **parent
directory** instead, verify that, then join the final element and confirm
it contains no separator.

Windows needs extra care: `C:` prefixes, `\\?\` paths, alternate data
streams (`file.txt:stream`), reserved names (`CON`, `NUL`, `COM1`),
trailing dots and spaces. Reject anything that is not a plain relative
path.

There is no default root. The agent shares only what was explicitly
added.

---

## 13. Backpressure

The disk moves far faster than the network. Without a brake, memory grows
until something dies. Two brakes, both required, in both directions.

**Local brake.** Pause when `bufferedAmount` goes above 1 MB, resume
below 256 KB. Pion: `SetBufferedAmountLowThreshold` and
`OnBufferedAmountLow`. In the browser: `bufferedamountlow`.

**Remote brake.** The receiver sends `CREDIT { transfer_id, chunks: N }`.
The sender may send at most N more. Start at 64, top up at half spent.

The local brake only knows the local socket. It says nothing about
whether the far side is keeping up with its disk. That is why both exist.

Chunk size **16 KB**. The spec allows 256 KB, but 16 KB is the safe
interoperable size and performs fine.

Expect 30-100 MB/s on a LAN, and otherwise whatever the agent's upload
link gives.

### 13.1 Getting bytes to disk in a browser

A 10 GB file cannot sit in a JavaScript variable.

- **File System Access API** — `showSaveFilePicker()` gives a real
  writable handle. Best path. Chrome, Edge, Opera on desktop.
- **Service worker streaming** — the download URL is served by a service
  worker returning a `ReadableStream`. Works in Firefox and Safari too.
  More moving parts; the tab must stay open.
- **Blob in memory** — small files only.

Feature-detect and use the first available. Build the service worker
path first, because it is the one that must work everywhere and the one
that can surprise you.

---

## 14. Local control API on the agent

The agent runs an HTTP server on `127.0.0.1` on a random free port, and
writes the port plus a random token to `control.json`, mode 0600.

| OS | Config dir |
|----|-----------|
| Linux | `~/.config/ratatoskr/` |
| macOS | `~/Library/Application Support/ratatoskr/` |
| Windows | `%AppData%\ratatoskr\` |

`os.UserConfigDir()`. The directory holds `identity.key` (0600),
`config.json`, `control.json`.

Every CLI subcommand is an HTTP client against this API, and so is any
future desktop UI. Bind `127.0.0.1` only, never `0.0.0.0`. Require the
token.

```
GET  /v1/status  /v1/peers  /v1/discover  /v1/events
GET|POST|DELETE  /v1/folders  /v1/trusted
POST /v1/pair    redeem a pairing code against mimir
```

Note the contrast with the LAN signal endpoint in 7.3: the control API is
loopback and full power; the LAN endpoint is network-reachable and can do
exactly one harmless thing.

---

## 15. CLI surface

```
ratatoskr run                     start the agent in the foreground
ratatoskr id                      peer id and public key
ratatoskr status                  presence, peers, measured path per peer
ratatoskr discover                agents visible on this network
ratatoskr peers                   connected peers
ratatoskr pair CODE               pair this machine to an account
ratatoskr folders list | add PATH [--rw] | remove PATH
ratatoskr trusted   | trust ID [--name N] | untrust ID

ratatoskr connect ID ls PATH
ratatoskr connect ID get PATH OUT
ratatoskr connect ID put SRC PATH
ratatoskr connect ID mkdir|mv|rm PATH...
ratatoskr connect ID --via lan|net       force one discovery path
ratatoskr version

heimdall serve --addr :8080 --mimir https://...
mimir     serve --addr :8081 --db ...
```

`connect` is the agent acting as a client. It exists so the whole system
is testable with two machines and no frontend at all — and it later
becomes machine-to-machine transfer, which is a feature in its own right.
`--via lan` fails rather than falling back, which is what makes the
Internet-unplugged test mean something.

---

## 16. Build and platform rules

**The agent stays free of cgo.** Pion is pure Go. Keep it that way and one
machine builds every target:

```
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/ratatoskr.exe ./cmd/ratatoskr
```

- No Go GUI toolkit in the agent; every one needs cgo. A desktop UI, if
  it happens, is a separate binary on the control API.
- The mDNS library must be pure Go. Rules out Bonjour and Avahi wrappers.
- If SQLite is needed in the agent, `modernc.org/sqlite`, not
  `mattn/go-sqlite3`. Mimir runs on a server and may use Postgres freely.

---

## 17. Repository layout

```
ratatoskr/
├── cmd/
│   ├── ratatoskr/     the agent
│   ├── heimdall/      rendezvous
│   └── mimir/         control plane
├── internal/
│   ├── identity/      keypair, peer id, sign and verify
│   ├── grant/         access grant issue and verify — shared by agent and mimir
│   ├── protocol/      wire messages
│   ├── fileapi/       the File API verbs — the stable surface (10.1)
│   ├── transport/     the Transport interface (10.9)
│   │   ├── webrtc/    Pion, ICE, channels, backpressure
│   │   └── loopback/  in-process, for tests
│   ├── discovery/     the Discovery interface and the LAN-first policy
│   │   ├── lan/       mDNS advertise, browse, local signal endpoint
│   │   └── net/       heimdall client
│   ├── signal/        heimdall server logic
│   ├── fsroot/        allowed roots, path validation
│   ├── fsops/         list, stat, read, write, mkdir, move, copy, delete
│   ├── control/       127.0.0.1 API
│   └── config/        per-OS paths, folders, trust list
├── web/               the browser app  (TypeScript, separate toolchain)
│   └── src/
│       ├── fileapi.ts     the same verbs as internal/fileapi
│       ├── transport.ts   the same interface as internal/transport
│       └── fs-adapter.ts  the only file the UI library touches (21.2)
├── Makefile · PLAN.md · TODO.md · go.mod
```

One Go module, three binaries. They share `internal/protocol`,
`internal/identity` and `internal/grant`, so the formats cannot drift.

---

## 18. Build order

| Step | Goal | Passes when |
|------|------|-------------|
| 0 ✅ | Two agents, SDP pasted by hand | a string echoes over a DataChannel |
| 1 | Device identity | `ratatoskr id` is stable across restarts on all three OSes |
| 2 | Mutual auth + local trust list | an untrusted id is refused; a tampered signature is refused |
| 3 | heimdall rendezvous | `connect --via net` links up with no pasting |
| 4 | LAN discovery | `discover` sees the other machine; `--via lan` works with the uplink unplugged |
| 5 | LAN-first policy | heimdall is never contacted on a LAN; both fallback triggers work |
| 6 | LIST, STAT, path safety | a real listing prints; every hostile path is refused |
| 7 | Agent control API | `ratatoskr status` talks to a running `ratatoskr run` |
| 8 | Download a small file | 10 MB arrives, hash matches |
| 9 | Backpressure, 10 GB | completes, memory flat on both sides |
| 10 | **mimir**: accounts, devices, pairing, grants, presence | `GET /v1/devices` lists a paired machine and its presence |
| 11 | **Web app** on `@cubone/react-file-manager` behind our own adapter: list machines, status, browse, download | requirements 1, 2, and the read half of 3 |
| 12 | Real NAT across the Internet | direct connection, proven by the logged candidate pair |
| 13 | TURN fallback | 1 GB transfers with STUN disabled |
| 14 | **Write operations**: upload, mkdir, move, copy, delete | the whole of requirement 3, with section 11 enforced |
| 15 | Survival | sleep, wake, network change, restart, cancel — never hangs |
| 16 | Mobile app | including LAN discovery, which the web cannot do |

Step 9 proves the transport works. Step 11 is the first release anyone
can use. Step 14 is where care matters most, because it is the first step
that can destroy data.

Steps 1-9 need no server at all. That is deliberate.

---

## 19. Risks

| Risk | Likely | Response |
|------|--------|----------|
| Relay share much higher than 20% | Medium | Measure from step 12. It is the number that decides whether hosting stays cheap |
| A write bug destroys user data | Medium | Section 11. Atomic rename, explicit flags, no recursion by default. Read-only until step 14 |
| Path validation hole on writes | Medium | Writes resolve the parent, not the target. `internal/fsroot` is security code with hostile-input tables |
| mimir becomes a single point of failure | High | It is. Grants are cached so an existing pairing survives an outage on the LAN. Say so in the UI |
| Web client key lost when browser data is cleared | High | Expected. Re-pair flow must be one click, not a support ticket |
| mDNS blocked on real networks | Medium | Falls through after the head start. Measure how often the LAN wins |
| Firewall prompts confuse users at first run | High | Two prompts: UDP 5353 and the signal port. A packaging problem — note it now |
| Browser cannot stream large downloads | Medium | Service worker path, built and tested at step 11, not at launch |
| The chosen file-manager component turns out wrong | Medium | Everything goes through `fs-adapter.ts`. Replacing the library is one file, not a rewrite |
| Corporate networks block UDP entirely | Certain for some | coturn on TCP and TLS 443. A config, not a rewrite |
| Agent upload speed is the real ceiling | Certain | Nothing to fix. Report it honestly |

---

## 20. Dependencies

| Project | Role | License |
|---------|------|---------|
| pion/webrtc | WebRTC in Go | MIT |
| coder/websocket | WebSocket, client and server | ISC |
| pure-Go mDNS — `libp2p/zeroconf/v2`, `grandcat/zeroconf` or `pion/mdns` | LAN discovery. Pick at step 4 | MIT / Apache-2.0 |
| zeebo/blake3 | File hashing | CC0 / Apache-2.0 |
| coturn | TURN server, step 13, not bundled | BSD-3-Clause |

Ed25519, SHA-256 and base32 come from the standard library.

Mimir's database and HTTP stack are its own choice and do not constrain
the agent.

---

## 21. Reusing an existing file manager

Worth doing. But only one of the two obvious shapes actually works.

### 21.1 Server-side file managers do not work here

Filestash, File Browser (`filebrowser/filebrowser`), Nextcloud. These are
tempting, and their plugin seams are genuinely clean — Filestash has an
`IBackend` interface with exactly our verbs, and File Browser sits on
`afero.Fs`, so a Go adapter over our protocol would be a day's work.

Do not do it.

They run **on a server**. The bytes would flow
`agent -> your VPS -> browser`. That puts your VPS on the data path and
destroys the cost model, which is the single reason this project exists.
Filestash is also AGPL-3.0, a real constraint for a hosted product.

> Rule: if the file manager runs on a server, it is wrong for this
> architecture, however good its plugin API is.

### 21.2 What does work: a presentational component in the browser

The WebRTC peer lives in the browser tab. So the file manager has to be a
component running in that tab, whose data layer we supply.

| Library | Licence | Fit | Notes |
|---------|---------|-----|-------|
| `@cubone/react-file-manager` | MIT | good | Handler-based: `onCreateFolder`, `onDelete`, `onPaste`, `onRename`, `onDownload`, `onFileUploading`. Maintained. Ships an opinionated UI |
| SVAR React File Manager | check it | good | Polished, TypeScript, list/tiles/split views. Confirm the licence terms before committing |
| Chonky | MIT | fair | The best known one. Original is archived; several community forks. Right shape, but building on a fork is a maintenance bet |
| DevExtreme FileManager | commercial | best technical fit | `CustomFileSystemProvider` is almost exactly our RPC: `getItems`, `createDirectory`, `renameItem`, `deleteItem`, `moveItem`, `copyItem`, `uploadFileChunk`, `downloadItems`. Costs money |
| `react-keyed-file-browser`, `@opuscapita/react-filemanager` | MIT | fair | Older, low activity |

**Start with `@cubone/react-file-manager` at step 11.** MIT, maintained,
and its handler set already lines up with our verbs.

**The rule that keeps this reversible:** every call into the component
goes through one adapter module, `web/src/fs-adapter.ts`, which exposes
our verbs and nothing else. The rest of the app talks to the adapter.
Swapping libraries then touches one file instead of the whole app.

Two jobs no library will do, which stay ours either way:

- Streaming a multi-GB download to disk (13.1). Every library assumes a
  URL it can hand to the browser.
- Backpressure and progress over a DataChannel. Every library assumes
  `fetch` or XHR.

### 21.3 The bigger win: speak WebDAV locally

Our verb set is already the WebDAV verb set:

```
LIST   -> PROPFIND          WRITE  -> PUT
STAT   -> PROPFIND depth 0  MKDIR  -> MKCOL
READ   -> GET               MOVE   -> MOVE
DELETE -> DELETE            COPY   -> COPY
```

That is not a coincidence, and it should stay true.

So a later `ratatoskr mount <device-id>` can run a small WebDAV server on
`127.0.0.1` that translates to our protocol over WebRTC. Then, free:

- macOS Finder — *Go → Connect to Server*
- Windows Explorer — *Map network drive*
- Cyberduck, Mountain Duck, rclone, Nautilus, Dolphin
- Every application's Open and Save dialog

A real native file manager on every desktop, for the cost of one adapter,
with the bytes still going peer to peer. Not now — but keep the RPC verbs
WebDAV-shaped so it stays a small job later.

### 21.4 Mobile

Nothing reusable worth adopting. Build it. It is the smallest of the
three surfaces.

---

## 22. Not now

`ratatoskr mount` — the local WebDAV bridge of 20.3 · version history ·
sync · file search across devices · sharing with other accounts · public
links · thumbnails and previews · multi-user ACLs on one device · a
desktop UI · a browser on a LAN with no Internet at all (needs real TLS
on a private address; Plex does it; it is a project of its own).
