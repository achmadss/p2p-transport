# Ratatoskr — Plan

Module path: `github.com/achmadss/ratatoskr`

`SPEC.md` is the requirement. This is the design that satisfies it.
`TODO.md` is the ordered work.

---

## 1. What this is

A private, peer-to-peer network drive for your own machines. Files stay
on your hardware. The bytes travel directly between your devices. The
central infrastructure introduces peers and relays only when a direct
path is impossible.

### The pieces

| Name | Spec name | What it is | Runs on |
|------|-----------|-----------|---------|
| **ratatoskr** | Agent + Client | One Go binary. Serves files in agent mode, consumes them in client mode. | Windows, macOS, Linux |
| **mimir** | Coordinator | Accounts, device registry, presence, addresses, grants. The only database. | Linux VPS |
| **heimdall** | Coordinator (relay) | A libp2p relay node. Forwards encrypted bytes it cannot read. | Linux VPS |

Mimir and heimdall together are the spec's **Coordinator**. They are
split because they scale on different things: mimir on rows, heimdall on
open connections.

Ratatoskr is the squirrel that carries messages up and down the world
tree. Heimdall is the watchman at the bridge. Mimir is the well of
knowledge: he remembers who you are and what is yours.

### The core rule

> Mimir remembers. Heimdall forwards when he must. Ratatoskr carries the
> files.

Neither server can read a file. Neither is on the normal data path.

### Words used precisely

| Word | Means |
|------|-------|
| **account** | A person's login on mimir. Owns devices. |
| **device** | A machine running ratatoskr. Identified by a peer id. |
| **agent mode** | `ratatoskr run` — the side that **has** the files. |
| **client mode** | `ratatoskr connect` and the WebDAV gateway — the side that **asks**. |
| **peer id** | A libp2p identity: a multihash of the device's public key. |
| **root** / **share** | A folder the agent offers, with mode `ro` or `rw`. |
| **grant** | Mimir's signed note: this device may use these folders on that device, until this time. |

A device is usually both. Your laptop serves its own files and browses
your desktop's, using one keypair and one peer id for both roles.

---

## 2. Who talks to whom

```
                 ┌────────────────────────────────────┐
                 │              mimir                 │
  login,         │  accounts · devices · presence     │
  device list,   │  known addresses · grants          │
  addresses,     │  the only thing with a database    │
  grants  ┌─────►│                                    │
          │      └──────────────────┬─────────────────┘
          │                         │ presence + reservations
          │      ┌──────────────────▼─────────────────┐
          │      │            heimdall                │
          │      │  libp2p relay node · AutoNAT       │
          │      │  forwards encrypted bytes only     │
          │      └──────────────────┬─────────────────┘
          │                         │ used only if direct fails
┌─────────┴──────────┐              │        ┌──────────────────┐
│ ratatoskr          │              └───────►│ ratatoskr        │
│ client mode        │                       │ agent mode       │
│                    │                       │                  │
│ WebDAV :9832       │                       │ File API server  │
│ local web UI       │                       │ filesystem       │
│ File API client    │                       │                  │
└─────────┬──────────┘                       └────────┬─────────┘
          │                                           │
          │════════ libp2p: QUIC or TCP, Noise ═══════│
          │   /ratatoskr/ctrl/1   requests and replies │
          │   /ratatoskr/xfer/1   one stream per transfer
          └───────────────────────────────────────────┘
```

Three facts to hold on to:

1. A frontend never speaks to mimir or heimdall. It speaks to the local
   ratatoskr, over `127.0.0.1`.
2. Neither server holds a key that opens a file, and neither is on the
   data path unless the relay is in use — and then it forwards ciphertext.
3. The heavy line at the bottom is the only path file bytes take.

---

## 3. Where the client lives

The spec's model, and the reason it is right.

```
Finder · Explorer · rclone · Cyberduck
        │  WebDAV
        ▼
   127.0.0.1:9832  ──┐
                     ├──►  ratatoskr, client mode
   local web UI    ──┤         │
   CLI             ──┘         │  File API
                               ▼
                          libp2p, encrypted
                               │
                               ▼
                     ratatoskr, agent mode
                               │
                               ▼
                          filesystem
```

**Software must be installed on every machine you browse from.** That is
the trade. In exchange:

- LAN discovery works from every client. A browser cannot do mDNS; a
  local process can.
- A 10 GB download is written straight to disk. No service worker, no
  blob, no memory ceiling.
- The client key is a file, not something a cache clear destroys.
- Transfer state survives a reload, a restart, and a network change,
  which is what makes resume real.
- WebDAV works, so Finder, Explorer, rclone and Cyberduck are frontends
  on day one.

The local web UI is a static page served by ratatoskr on `127.0.0.1`. It
is an ordinary HTTP client of the control API — it does no peer-to-peer
work itself.

Zero-install browser access is given up. It can be added later as an
extra client kind without changing the File API or the agent, and it is
not in scope.

---

## 4. Transport — libp2p

libp2p provides peer identity, encryption, multiplexed streams, NAT
traversal, hole punching and relaying. Building those again would be the
bulk of this project.

### 4.1 Configuration

```
transports    QUIC (UDP, preferred) · TCP · WebSocket over TLS
security      Noise
muxer         QUIC's own; yamux over TCP
identity      Ed25519
services      AutoNAT · Identify · hole punching (DCUtR) · mDNS discovery
              circuit relay v2 — client on agents, hop on heimdall
```

QUIC first because it gives real streams with built-in flow control, and
because SCTP-over-DTLS — what a WebRTC DataChannel runs on — has a
throughput ceiling that would miss the spec's near-line-rate goal on a
1 Gbps LAN. TCP and WebSocket exist for networks that block UDP.

### 4.2 What this deletes from the design

| Would have had to build | libp2p gives |
|-------------------------|--------------|
| A custom SDP signaling protocol | Not needed. Peers dial multiaddrs |
| A mutual challenge-response handshake | Noise authenticates the peer id during the handshake |
| STUN configuration and candidate handling | AutoNAT and Identify |
| Hole punching | DCUtR, over an existing relay connection |
| coturn, TURN credentials, credential minting | Circuit relay v2, with reservations and limits |
| mDNS advertise and browse | `p2p/discovery/mdns` |
| A credit-window flow control scheme | QUIC stream flow control. Writing to a stream blocks |

The last one is worth dwelling on. Under WebRTC, keeping a 10 GB transfer
from eating memory needed a send-buffer watermark plus an
application-level credit window. Over a QUIC stream it is
`io.CopyBuffer`: the writer blocks when the reader is behind. Most of a
build step disappears.

### 4.3 Streams

Two protocol IDs. No shared channel, no framing games.

```
/ratatoskr/ctrl/1.0.0    long-lived. JSON requests and replies
/ratatoskr/xfer/1.0.0    one stream per transfer. Raw bytes, no framing
```

A transfer opens its own stream, writes a small header, then streams the
body. Cancelling is closing the stream. A slow reader is handled by QUIC.

### 4.4 The relay cannot read anything

Spec §6 and §30.3 require end-to-end encryption even over the relay.
Circuit relay v2 satisfies this by construction: the Noise session runs
between the two peers, and heimdall forwards an already-encrypted byte
stream. It terminates nothing, holds no session key, and cannot tell a
directory listing from a photograph.

What heimdall does learn, and cannot avoid learning: which two peer ids
are talking, when, and how many bytes. That is the honest limit of any
relay, and it is worth writing down rather than implying otherwise.

### 4.5 Known rough edges

Hole punching is not instant and does not always work — libp2p's own
issue tracker has open reports of multi-second punches. This is the same
reality any NAT traversal faces. Two consequences, both already in the
plan:

- A relayed connection is used immediately while the punch is attempted
  in the background, then upgraded. The user waits for the relay, not the
  punch.
- The punch success rate is measured from step 3, because it decides the
  bandwidth bill.

---

## 5. Identity and authorisation

### 5.1 Device identity

An Ed25519 keypair, generated on first run. The libp2p peer id is a
multihash of the public key, so the id proves itself: the Noise handshake
cannot succeed against a peer that does not hold the matching private
key.

```
identity.key   config dir, mode 0600, never leaves the machine
peer id        libp2p PeerID, derived from the public key
```

Peer ids are long. The UI shows a user-given name plus a short
fingerprint; the full id appears in diagnostics only.

**Not a hardware fingerprint.** MAC addresses, disk serials and machine
UUIDs break when a VM is cloned or a disk is swapped, macOS randomises
MACs, and they leak facts about the machine. Spec §13 makes the same
argument.

### 5.2 What libp2p does not do

libp2p answers *who is this peer*. It does not answer *may this peer read
my Documents folder*. Authorisation stays ours.

Two mechanisms, and a peer is allowed if either passes.

**The trust list.** A hand-managed list of peer ids, in the agent's
config. This is the self-hosting and development path, and it works with
no server at all.

```
ratatoskr trust <peer id> --name "my laptop"
ratatoskr untrust <peer id>
ratatoskr trusted
```

**Grants.** Mimir signs a short document that the client presents on the
control stream. The agent pins mimir's public key at pairing time.

```json
{
  "v": 1,
  "account": "acc_7f3a",
  "device":  "<agent peer id>",
  "client":  "<client peer id>",
  "scopes":  { "/Documents": "rw", "/Videos": "ro" },
  "issued":  "2026-09-06T10:00:00Z",
  "expires": "2026-09-06T11:00:00Z",
  "sig":     "<mimir's signature>"
}
```

Checked in order: signature against the pinned key; `device` is this
agent; not expired, allowing small clock skew; `account` matches the
pairing; and `client` equals the peer id libp2p already authenticated.
That last check is why no separate challenge-response is needed — the
transport has proved the peer id.

Any failure closes the stream. Fail closed, always.

**Offline.** A grant is cached until it expires. A device that was online
an hour ago still works on a LAN with the Internet down. A first-ever
connection cannot be authorised offline, and that is the honest limit.

---

## 6. Discovery

Given a peer id, find an address and dial it.

### 6.1 LAN first, with a head start

```
connect <peer id>
  |
  +-- t=0      mDNS on the local network         no Internet needed
  |            answers in 50-200 ms -> dial it, and never ask mimir
  |
  +-- t=400ms  ask mimir for known addresses     only if the LAN is quiet
```

A strict sequence would make every remote connection wait out the mDNS
timeout first. A plain race would query mimir even for the machine on the
same switch. The head start avoids both.

Fall back on either trigger: mDNS timed out, **or** the peer was found on
the LAN but the dial failed. A local firewall can block the QUIC port
while multicast still works. Finding is not reaching.

### 6.2 Internet discovery

Mimir stores each agent's current addresses, refreshed by the agent
whenever they change:

```
/ip4/203.0.113.7/udp/4001/quic-v1                       if publicly reachable
/ip4/<heimdall>/udp/4001/quic-v1/p2p/<heimdall id>/p2p-circuit/p2p/<agent id>
```

The client asks mimir for the list and dials them. libp2p tries them in
parallel, prefers the direct one, and if only the circuit works it
connects through the relay and then attempts a DCUtR upgrade to direct.

There is no custom signaling protocol. This is the largest single
simplification libp2p brings.

### 6.3 A LAN session stays on the LAN

Discovering on the LAN is not enough by itself: the host would still be
holding a relay reservation and talking to mimir. A connection dialled
from mDNS uses the LAN multiaddr only, with no relay address in the dial
set, so nothing about that session leaves the network.

This makes "works with the Internet unplugged" testable rather than
accidental.

### 6.4 One data path

mDNS and mimir are two ways to learn an address. Both then dial the same
libp2p host and use the same streams. There is exactly one transfer
implementation, ever.

---

## 7. Operation classes

What needs a peer connection, and what does not. This is what makes it
obvious where the money goes.

| Class | Talks to | Size | Needs a peer? |
|-------|----------|------|---------------|
| **A. Account** | mimir, plain HTTPS | ~1 KB | **No.** Never touches the machine |
| **B. Session** | the agent | ~5 KB | Yes, once per session |
| **C. Metadata** | the agent | ~1 KB per call | Yes, but cheap on any path |
| **D. Bulk** | the agent | **MB to GB** | Yes, and the path decides the cost |

The line people expect is "HTTP versus P2P". That is not it. A laptop
behind CGNAT cannot be reached by an HTTP call, so B, C and D all need a
peer connection. The real lines are:

> **A never touches your machine. Only D cares which path it got.**

**A — account plane.** Log in, list devices, presence, shared folders,
pair, unpair, revoke, fetch a grant, fetch addresses.

**B — session.** Discovery, dial, stream open, grant check, list roots.
50-200 ms on a LAN, 0.3-2 s over the Internet, longer if a hole punch is
in progress. Keep the session open while the user browses.

**C — metadata.** `LIST` ~200 B plus ~120 B per entry. `STAT`, `MKDIR`,
`MOVE`, `COPY`, `DELETE`, `DF`, `HASH` all a few hundred bytes. `SEARCH`
up to tens of kilobytes. A thousand listings over a relay is a few
megabytes. Let them relay without a second thought.

Two of those look like class D and are not:

- **COPY is server-side.** Duplicating a 4 GB file inside a share costs
  about 200 bytes. If a frontend implements copy as download-then-upload,
  the whole benefit is gone.
- **MOVE is a rename.** Moving 100 GB between folders on one disk must
  not move a single byte across the network.

**D — bulk.** `READ`, `READ` with offset and length, `WRITE`, `WRITE` at
an offset, `THUMBNAIL`.

```
lan or direct   free, fast, and no server knows it happened
relay           every byte is paid for twice on your bandwidth bill
```

**THUMBNAIL is class D behaving like class C.** The agent renders a
256 px preview and returns 20 KB instead of a 12 MB photo. A gallery that
fetches full images is a bandwidth disaster on a relay and slow
everywhere.

**Ranged READ is not optional.** Without it there is no resume, no video
seeking and no cheap type sniffing.

**Class E — not operations at all.** Breadcrumbs, sorting, filtering a
fetched listing, icons, selection state. All in the frontend.

Consequence for the product: a relay connection is perfectly usable for
browsing. "Relayed" should read as *slower downloads*, not *broken*.
Warnings and metering belong on class D alone.

---

## 8. Layers

```
┌──────────────────────────────────────────────────────┐
│ Frontend    WebDAV clients · local web UI · CLI      │
├──────────────────────────────────────────────────────┤
│ Adapters    WebDAV · HTTP control API · CLI          │  swappable
├──────────────────────────────────────────────────────┤
│ File API    the verbs. Knows nothing about networking│  ← stable surface
├──────────────────────────────────────────────────────┤
│ Transport   libp2p streams · loopback for tests      │
├──────────────────────────────────────────────────────┤
│ Discovery   mDNS · mimir                             │
└──────────────────────────────────────────────────────┘
```

Spec §10 and §30.5: WebDAV is an adapter, never the internal protocol.
SFTP, FUSE or SMB can be added as more adapters later without the File
API changing.

```go
type Transport interface {
    Call(ctx context.Context, req Request) (Response, error)  // class B, C
    OpenStream(ctx context.Context, id TransferID) (Stream, error) // class D
    Path() Path      // lan | direct | relay — measured, never guessed
    Close() error
}
```

`loopback` is a real implementation, not a stub: it makes the whole file
layer testable with no network at all.

---

## 9. The File API

### 9.1 Control stream — `/ratatoskr/ctrl/1.0.0`

JSON, one object per message. Every request carries `version`,
`request_id`, `type`. Every reply echoes `request_id`.

```
session
  HELLO -> HELLO_OK      version negotiation and the grant
  PING  -> PONG          liveness and round-trip time
  ROOTS -> ROOTS_RESULT  shared folders and their modes

read
  LIST  -> LIST_RESULT   directory listing, paged
  STAT  -> STAT_RESULT   one entry
  DF    -> DF_RESULT     free space on a share
  HASH  -> HASH_RESULT   checksum without transferring
  READ  -> READ_OK       open a download; returns transfer id, size, hash
  THUMB -> THUMB_OK      a small rendered preview

write
  WRITE -> WRITE_OK      open an upload, optionally at an offset
  MKDIR -> OK
  MOVE  -> OK            rename, or move between writable roots
  COPY  -> OK            server-side
  DELETE-> OK

both
  CANCEL                 abandon a transfer
  ERROR                  code and a safe message
```

### 9.2 Entry metadata

```json
{
  "name": "movie.mkv",
  "path": "/Videos/movie.mkv",
  "kind": "file",
  "size": 4294967296,
  "modified": "2026-09-05T08:00:00Z",
  "created":  "2026-01-02T11:30:00Z",
  "mode": "rw-r--r--",
  "symlink": false
}
```

`path` is always root-relative and always the path the client asked
through — never a resolved absolute path, which would leak the layout of
the machine.

`created` and `mode` are optional. Creation time is not portable across
filesystems, and it must be allowed to be absent rather than faked.

`DELETE` covers both files and directories; a directory needs
`recursive: true` unless it is already empty. Spec §11 lists them
separately; one verb with an explicit flag is safer than two verbs, one
of which is quietly destructive.

### 9.3 Transfer stream — `/ratatoskr/xfer/1.0.0`

Open the stream, write a small header naming the transfer id, then stream
raw bytes until EOF. No chunk framing: QUIC already delivers an ordered
byte stream, and re-implementing framing on top would add nothing.

Backpressure is `io.CopyBuffer` with a 64 KB buffer. The stream write
blocks when the far side is behind. There is no credit window and no
watermark to tune.

`READ_OK` carries the size and a BLAKE3 hash of the whole file. The
receiver verifies before declaring success.

### 9.4 Limits, enforced by the agent

- Control message over 64 KB — close the stream
- More than 4 concurrent transfers per peer — `ERROR`
- More than 8 connected peers — refuse
- Unknown message type — `ERROR`, do not close
- `ERROR` never contains a real filesystem path or a stack trace

---

## 10. Resumable transfers

Spec §20 requires this. It is designed here rather than left to chance.

### 10.1 State

The client persists a record per transfer, in its config directory:

```json
{
  "id": "tx_01J...",
  "device": "<peer id>",
  "remote": "/Videos/movie.mkv",
  "direction": "download",
  "size": 10737418240,
  "hash": "<blake3 of the whole remote file>",
  "bytes_done": 4509715660,
  "local": "/Users/me/Downloads/movie.mkv.rtpart",
  "updated": "2026-09-06T10:44:00Z"
}
```

The record outlives the connection, the process and a reboot. That is
what makes resume real rather than a retry button.

### 10.2 Resuming a download

1. Re-`STAT` the remote file. If size, modified time or hash differ from
   the record, the source changed: discard and start over. Silently
   appending to a stale partial produces a corrupt file, which is worse
   than starting again.
2. `READ` with `offset = bytes_done`.
3. Append to the `.rtpart` file.
4. On completion, verify the whole-file hash, then rename into place.

### 10.3 Resuming an upload

1. `STAT` the remote temp file to learn how much arrived.
2. `HASH` that remote prefix and compare with the same prefix locally.
   This catches a partial write that was truncated or corrupted.
3. `WRITE` at that offset.

### 10.4 Automatic recovery

On a dropped connection, the client retries with backoff and resumes from
the record. It does not ask the user. A transfer only surfaces as failed
after the retry budget is spent, or if the source changed.

```
ratatoskr transfers            list, including interrupted ones
ratatoskr transfers resume ID
ratatoskr transfers cancel ID
```

Stale `.rtpart` files and stale server-side temp files are cleaned up on
startup.

---

## 11. Write operations

Where a bug destroys data instead of merely failing. These rules are not
optional.

**Permission.** Every root has a mode, `ro` or `rw`. A write to an `ro`
root is refused before any path work happens. A grant may narrow a root's
mode, never widen it.

**Atomicity.** Never write in place.

```
upload to  <dir>/.ratatoskr-tmp-<transfer id>
fsync the file
fsync the directory
rename over the target        atomic on the same filesystem
```

A dropped connection must never leave a half file where a good one used
to be.

**Per verb:**

- **MKDIR** — no `-p` by default. Refuse if the parent is missing, so a
  typo cannot silently build a tree.
- **MOVE** — validate source and destination independently. Both roots
  must be `rw`. No overwrite without `overwrite: true`. A move that
  crosses a filesystem boundary becomes copy, verify hash, then delete.
- **DELETE** — never recursive unless `recursive: true` is explicit.
  Never follow a symlink out of a root: delete the link, not its target.
  Refuse to delete a share root.
- **WRITE** — check free space and enforce a size cap before starting.
  Refuse an existing target without `overwrite: true`.
- **COPY** — server-side, with MOVE's destination rules.

---

## 12. Path safety

Every filesystem call, no exceptions:

```
requested path
   -> reject if it contains a null byte
   -> filepath.Clean
   -> join to the allowed root
   -> filepath.EvalSymlinks        resolves symlinks and junctions
   -> verify the result is still inside the root
   -> open
```

Check the **resolved** path, not the requested one.

Writes need a variant: the target usually does not exist yet, so
`EvalSymlinks` cannot resolve it. Resolve the **parent directory**,
verify that, then join the final element and confirm it contains no
separator.

Windows: `C:` prefixes, `\\?\` paths, alternate data streams
(`file.txt:stream`), reserved names (`CON`, `NUL`, `COM1`), trailing dots
and spaces. Reject anything that is not a plain relative path.

There is no default root. The agent shares only what was explicitly
added. Spec §14 does not mention path traversal; it is the most likely
way this system gets breached.

---

## 13. Mimir — the coordinator's memory

### 13.1 Stores

```
accounts     id, email, auth
devices      id (peer id), account, public key, name, os, created, last_seen
addresses    device, multiaddr, kind (direct|circuit), transport
             (quic|tcp|ws), updated
shares       device, path, mode          advertised, so the UI can preview
grants       issued grants, for revocation and audit
```

It stores **devices, not files**. No filenames, no directory contents,
no sizes. Spec §24.

### 13.2 API

```
POST   /v1/auth/...              login
GET    /v1/devices               list my machines, with presence
GET    /v1/devices/{id}          one machine
GET    /v1/devices/{id}/addrs    current multiaddrs
POST   /v1/devices/{id}/grant    a grant for this client
DELETE /v1/devices/{id}          unpair
POST   /v1/pair                  redeem a pairing code
PUT    /v1/self/addrs            an agent publishes its addresses
```

### 13.3 Revoking a device

Spec §14 requires it. Two things happen, and they take effect at
different speeds.

1. **Immediately.** Mimir stops issuing grants for that device, drops its
   addresses, and heimdall refuses its relay reservation. It disappears
   from every device list and can no longer be found.
2. **Within the grant lifetime.** Agents that already hold a valid grant
   naming that device keep honouring it until it expires.

That lag is the price of letting an agent authorise without phoning home,
which is what makes offline LAN use work. Keep grant lifetimes short —
one hour — so the window is small, and offer `ratatoskr untrust` as the
immediate local override for an agent that is reachable.

An account-wide "revoke everything" must therefore also push to any
agent that is currently online, and be honest in the UI about offline
agents: *revoked; will take effect when the machine next comes online, or
within the hour.*

### 13.4 Presence

Heimdall knows who holds a relay reservation. Agents also heartbeat to
mimir directly. Presence is `online` when either says so, `unknown` when
mimir cannot tell — never a silent guess.

---

## 14. Status, honestly reported

Two different questions. Mixing them produces a UI that lies.

**Before connecting — presence,** from mimir:

```
online · offline · unknown
```

Plus a hint, and only a hint: `same_network` when the public IP mimir
sees for both matches. It usually means a LAN path is available. Never
show it as a fact.

**After connecting — the measured path,** from the actual libp2p
connection:

```
lan       dialled from mDNS, no relay in the dial set
direct    a direct address, possibly after a successful hole punch
relay     going through heimdall; these bytes cost money
```

The UI:

```
Home Laptop   ● Online                        before connecting
Home Laptop   ● Online · Local network
Home Laptop   ● Online · Direct
Home Laptop   ● Online · Relayed — slower
Home Laptop   ○ Offline
```

Never show the words QUIC, DCUtR, AutoNAT, multiaddr or circuit outside
a diagnostics screen. Spec §30.4.

---

## 15. Local control API

Ratatoskr runs an HTTP server on `127.0.0.1` on a random free port, and
writes the port and a random token to `control.json`, mode 0600.

| OS | Config dir |
|----|-----------|
| Linux | `~/.config/ratatoskr/` |
| macOS | `~/Library/Application Support/ratatoskr/` |
| Windows | `%AppData%\ratatoskr\` |

`os.UserConfigDir()`. Holds `identity.key` (0600), `config.json`,
`control.json`, and `transfers/`.

Every CLI subcommand and the local web UI are HTTP clients of this API.
Bind `127.0.0.1` only, never `0.0.0.0`. Require the token.

```
GET  /v1/status  /v1/peers  /v1/discover  /v1/events
GET|POST|DELETE  /v1/folders  /v1/trusted
GET  /v1/devices                       proxied from mimir
GET  /v1/fs/{device}/...               the File API, for the web UI
GET  /v1/transfers  POST /v1/transfers/{id}/resume
POST /v1/pair
```

The WebDAV gateway on `127.0.0.1:9832` is a second adapter over the same
File API, with one path prefix per device, exactly as spec §26 describes:

```
http://127.0.0.1:9832/home-laptop/Documents/
http://127.0.0.1:9832/desktop/Projects/
```

Binding to loopback is what keeps the design honest. A WebDAV endpoint on
a public URL would put that server on the data path, which is the thing
this project exists to avoid.

---

## 16. CLI surface

Spec §27's syntax.

```
ratatoskr run                          agent mode, foreground
ratatoskr id                           this device's peer id
ratatoskr status                       presence and measured path per peer
ratatoskr devices                      my machines, from mimir
ratatoskr discover                     agents visible on this network
ratatoskr pair CODE
ratatoskr folders list | add PATH [--rw] | remove PATH
ratatoskr trusted | trust ID [--name N] | untrust ID

ratatoskr ls    home:/Documents
ratatoskr get   home:/Documents/report.pdf ./report.pdf
ratatoskr put   ./photo.jpg home:/Pictures/
ratatoskr mkdir home:/Documents/New
ratatoskr mv    home:/a.txt home:/Archive/a.txt
ratatoskr rm    home:/old.pdf
ratatoskr transfers [resume ID | cancel ID]

ratatoskr webdav --addr 127.0.0.1:9832
ratatoskr connect ID --via lan|net      force one discovery path

heimdall serve --addr /ip4/0.0.0.0/udp/4001/quic-v1
mimir    serve --addr :8081 --db ...
```

`home` is a local alias for a peer id, so nobody types a multihash twice.
`--via lan` fails rather than falling back, which is what makes the
Internet-unplugged test mean anything.

---

## 17. Build and platform rules

**The agent stays free of cgo.** go-libp2p is pure Go. Keep it that way
and one machine builds every target:

```
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/ratatoskr.exe ./cmd/ratatoskr
```

- No Go GUI toolkit in the binary; every one needs cgo. The UI is a web
  page on the control API.
- No tray icon in the binary, for the same reason. A separate launcher
  can have one later.
- If SQLite is needed locally, `modernc.org/sqlite`, not
  `mattn/go-sqlite3`. Mimir runs on a server and may use Postgres freely.
- Thumbnail rendering must use a pure-Go image decoder. Rules out
  anything wrapping libvips or ImageMagick.

---

## 18. Repository layout

```
ratatoskr/
├── cmd/
│   ├── ratatoskr/     the agent and client binary
│   ├── heimdall/      relay node
│   └── mimir/         coordinator
├── internal/
│   ├── identity/      keypair, peer id, config paths
│   ├── grant/         issue and verify — shared by mimir and the agent
│   ├── protocol/      File API wire messages
│   ├── fileapi/       the verbs. Must not import libp2p
│   ├── transport/     the Transport interface
│   │   ├── p2p/       libp2p host, streams, dialling
│   │   └── loopback/  in-process, for tests
│   ├── discovery/     mDNS, mimir lookup, the LAN-first policy
│   ├── fsroot/        allowed roots, path validation
│   ├── fsops/         list, stat, read, write, mkdir, move, copy, delete
│   ├── transfer/      resume records, retry, verification
│   ├── webdav/        the WebDAV adapter
│   ├── control/       127.0.0.1 HTTP API
│   └── config/        per-OS paths, folders, trust list, aliases
├── web/               the local UI, served from 127.0.0.1
├── Makefile · SPEC.md · PLAN.md · TODO.md · go.mod
```

One module, three binaries, sharing `internal/protocol`,
`internal/identity` and `internal/grant` so formats cannot drift.

---

## 19. Build order

| Step | Goal | Passes when |
|------|------|-------------|
| 0 | libp2p echo between two hosts, addresses pasted by hand | a string echoes over a stream |
| 1 | Identity and config | `ratatoskr id` is stable across restarts on all three OSes |
| 2 | mDNS discovery, LAN dial | `ratatoskr discover` finds the other machine and connects with no server |
| 3 | **NAT spike**: heimdall as a bare relay, DCUtR, throughput | two machines on different networks connect; hole punch rate and MB/s are measured and written down |
| 4 | File API: ROOTS, LIST, STAT, DF, path safety, trust list | a real listing prints; every hostile path is refused |
| 5 | Control API and CLI | `ratatoskr ls home:/Documents` works end to end |
| 6 | Download, ranged read, 10 GB | completes, hash matches, memory flat on both sides |
| 7 | Write operations | upload, mkdir, move, copy, delete, with §11 enforced |
| 8 | WebDAV gateway | Finder mounts `127.0.0.1:9832` and can browse, download and upload |
| 9 | Resume and recovery | a 10 GB transfer survives an unplugged cable and finishes correctly |
| 10 | mimir: accounts, devices, addresses, pairing, grants, presence | `ratatoskr devices` lists a paired machine over the Internet |
| 11 | heimdall in production: reservations, limits, relay metering | a relayed 1 GB transfer works and is counted |
| 12 | Local web UI | browse, transfer and see status in a browser on `127.0.0.1` |
| 13 | Survival | sleep, wake, network change, restart, cancel — never hangs |
| 14 | Mobile app | embeds the client, does its own mDNS |

Steps 0 to 9 need **no server at all**. That is deliberate: a LAN-only
product with WebDAV and resumable transfers is already useful, and it
front-loads every risk that is not about NAT.

Step 3 is early on purpose. It is the step that can genuinely fail, and
it is where the libp2p choice is proved or disproved. Do not build steps
4 to 9 on an unmeasured assumption about throughput.

Step 7 is where a bug can destroy data.

### 19.1 Where the spec's MVP lands

Spec §28 defines the MVP. It is complete at **step 11**.

| Spec §28 requirement | Step |
|----------------------|------|
| Coordinator: user authentication | 10 |
| Coordinator: device registration | 10 |
| Coordinator: device discovery | 2 (LAN), 10 (Internet) |
| Coordinator: presence | 10 |
| Coordinator: signaling | not needed — libp2p dials addresses |
| Agent: device identity | 1 |
| Agent: filesystem access | 4, 7 |
| Agent: P2P listener | 0, 2 |
| Agent: file protocol | 4, 6, 7 |
| Agent: basic authorization | 4 (trust list), 10 (grants) |
| Client: device list | 5 (local), 10 (account) |
| Client: device connection | 2, 3 |
| Client: directory browsing | 4, 5 |
| Client: download | 6 |
| Client: upload | 7 |
| Client: rename, delete, create directory | 7 |
| WebDAV: local endpoint and translation | 8 |
| Networking: LAN direct | 2 |
| Networking: Internet direct | 3 |
| Networking: NAT traversal | 3 |
| Networking: relay fallback | 3, 11 |

Steps 12 to 14 are beyond the spec's MVP.

### 19.2 One spec goal the MVP does not reach

Spec §12 wants installation to feel like:

```
Install -> Sign in -> Device appears online
```

Through step 11 the agent is started from a terminal. Installers,
autostart and a tray launcher are in §22, after the MVP. This is a
packaging problem rather than an architectural one, and none of the work
above assumes a terminal — `ratatoskr run` is already a well-behaved
background process with a control API — but the goal is not met until
that packaging exists. Worth stating rather than quietly missing.

---

## 20. Risks

| Risk | Likely | Response |
|------|--------|----------|
| Hole punch rate is poor, so relay use is high | Medium | Measured at step 3, before anything depends on it. Relay use decides the bandwidth bill |
| QUIC throughput misses the 1 Gbps LAN goal | Low | Also measured at step 3. It is the reason libp2p was chosen over WebRTC |
| Hole punching takes seconds | Certain sometimes | Serve over the relay immediately, upgrade in the background. The user waits for the relay, not the punch |
| A write bug destroys user data | Medium | §11. Atomic rename, explicit flags, no recursion by default. Reads are solid before step 7 |
| Path validation hole on writes | Medium | Writes resolve the parent, not the target. `fsroot` is security code with hostile-input tables |
| Resume corrupts a file after the source changed | Medium | §10.2 re-verifies size, mtime and hash before appending. Discard beats silent corruption |
| libp2p is a large dependency to debug | Medium | Accepted. It replaces signalling, hole punching, relaying and mDNS. Pin versions and keep the Transport interface so it stays replaceable |
| mimir becomes a single point of failure | High | It is. Grants are cached and mDNS needs no server, so LAN use survives an outage. Say so in the UI |
| mDNS blocked on real networks | Medium | Falls through after the head start. Measure how often the LAN wins |
| Firewall prompts confuse users at first run | High | UDP 5353 and the QUIC port. A packaging problem — note it now |
| Users will not install software on every client | Medium | The trade the spec chose. Browser-as-peer stays possible later, at real cost |
| Agent upload speed is the real ceiling | Certain | Nothing to fix. Report it honestly |

---

## 21. Dependencies

| Project | Role | Licence |
|---------|------|---------|
| go-libp2p | transport, identity, NAT traversal, relay, mDNS | MIT / Apache-2.0 |
| golang.org/x/net/webdav | the WebDAV adapter | BSD-3-Clause |
| zeebo/blake3 | file hashing | CC0 / Apache-2.0 |
| a pure-Go image decoder | thumbnails | pick at step 6 |

Ed25519, SHA-256 and base32 come from the standard library. Mimir's
database and HTTP stack are its own choice and do not constrain the agent.

---

## 22. Later

Zero-install browser access · SFTP, FUSE and SMB adapters · file search
across devices · version history · sync · sharing between accounts ·
public links · desktop tray launcher · installers and autostart.
