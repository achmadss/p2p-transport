# p2p-transport — Plan

Module path: `github.com/achmadss/p2p-transport`

`SPEC.md` is the requirement. This is the design that satisfies it.
`TODO.md` is the ordered work. `FLEET.md` is the design of the relay
fleet that `PLAN.md` §8 only sketches.

---

## 1. What this is

A Go package and a relay binary, plus a command-line tool that exists
only to exercise and diagnose them.

| Name | What it is | Runs on |
|------|-----------|---------|
| `transport` | The package. Everything an application above it uses. | Windows, macOS, Linux |
| `heimdall` | A relay node. Forwards encrypted bytes it cannot read. | Linux VPS |
| `ratatoskr` | The test harness: `id`, `run`, `discover`, `connect`, `bench`, and the NAT diagnostics. Not a product. | anywhere |

Ratatoskr is the squirrel that carries messages up and down the world
tree; Heimdall is the watchman at the bridge. The names stay because
they are already on the wire, in protocol identifiers like
`/ratatoskr/addrs/1.0.0`. Renaming those would break every deployed
binary to gain nothing.

The application this transport was built for — a private network drive
over a person's own machines — is not here. It is the consumer, and the
whole design question of this repository is what it is allowed to see.

---

## 2. The seam

The one thing this repository exists to get right.

### 2.1 The surface

```go
package transport

type PeerID string                    // stable; survives restarts and address changes
func (id PeerID) Short() string        // for people

type Config struct {
    Dir    string           // where identity.key lives; "" is the per-OS default
    Relays []string         // opaque tokens, from whoever runs the relay
    NoLAN  bool             // skip local discovery entirely
}

func New(cfg Config) (*Host, error)
func (h *Host) ID() PeerID
func (h *Host) Addrs() []string        // opaque; pass to a peer however you like
func (h *Host) Close() error

// Serving. The peer on the far end is already authenticated.
func (h *Host) Handle(proto string, fn func(Stream))

// Reaching. Addresses are whatever Addrs() returned over there.
func (h *Host) Connect(ctx context.Context, id PeerID, addrs []string) error
func (h *Host) Open(ctx context.Context, id PeerID, proto string) (Stream, error)

// Knowing.
func (h *Host) Peers() []PeerID
func (h *Host) PathTo(id PeerID) Path
func (h *Host) Watch(id PeerID) (<-chan Path, func())

// Finding, without a server.
func (h *Host) OnLAN(fn func(PeerID, []string))

type Stream interface {
    io.ReadWriteCloser
    CloseWrite() error       // "I am done sending"; keep reading
    Peer() PeerID
    Path() Path
}

type Path string             // lan · direct · relay · unknown
func (p Path) BetterThan(other Path) bool

// Why a stream is not carrying bytes. Test with errors.Is.
var ErrUnreachable = errors.New("machine unreachable")
var ErrNotHandled  = errors.New("protocol not handled")
```

That is the whole contract. It fits on a screen on purpose: a surface
this small can be frozen, and a frozen surface is what lets the layer
above be written against it without reading a line of what is below.

There is no private key on this surface, and that is the one place this
differs from the first draft of §2.1. A key is a libp2p type; naming one
here would make every caller import libp2p to fill it in, which is the
single thing step 4 exists to prevent. So the transport loads or
generates the key in `Dir` and never hands it out, and `identity` stays
`internal/`.

`Watch` is how the third fact in §2.2 stops being a poll. Every event
that can change a path is a connection opening or closing, and the
transport already watches for both to drive its own repair, so a watch is
those hooks forwarded and nothing polls. The current path is delivered
before `Watch` returns, so a caller cannot miss a change between asking
where a machine is and starting to listen; the channel then holds one
value, because a reader that falls behind wants where the machine is now
rather than every rung it passed.

The two errors are there because a caller acts differently on each. A
machine that cannot be reached is worth trying again when `Watch` says
the path changed. A machine that answered and has no handler for that
protocol name will never answer, and retrying is a loop. Both arrive as a
failure to open a stream, and without the distinction the caller has only
a string to match on. What actually went wrong stays wrapped inside for a
person to read.

An address is a **string the application never parses**. It comes out of
`Addrs()` on one machine and goes into `Connect` on another, through
whatever channel the application already has — a coordinator, a QR code,
a copy-paste. The transport is the only thing that knows what is inside
it. This is what keeps `multiaddr` out of layer 7 while leaving the
application free to invent any rendezvous it likes.

### 2.2 Three things that cannot be hidden

Everything else about layer 4 is concealable. These are not, and
pretending otherwise produces a caller that is subtly wrong.

**Identity is not authorisation.** The transport proves who; the
application decides what they may do. Hiding the peer id would make the
decision impossible; making the decision here would put policy in the
wrong repository.

**A stream dies with its connection.** Neither libp2p nor QUIC offers a
migration that would save it — QUIC's moves one connection between
*local* addresses, and the relay and the peer are two different remote
endpoints, two handshakes. So the transport reconnects the peer and
reports the loss; reissuing the request is the application's, because
only it knows what half a file means. This is the one place where the
layer below genuinely constrains the layer above, and the honest answer
is to say so rather than to fake durability.

**Bytes cost differently on different paths.** A metadata request is a
kilobyte and should never look at the path. A ten-gigabyte transfer
should: it can finish its current stream and open the next one on a
better path when one appears. `Watch` exists for that caller and no
other.

### 2.3 The words that never cross

`SPEC.md` §4 lists them: QUIC, TCP, Noise, multiaddr, circuit, DCUtR,
AutoNAT, STUN, reservation, hole punch, NAT, mDNS. None appears in an
exported name, a returned error, or ordinary output.

The escape hatch is `RATATOSKR_DIAG=1`, which prints all of it. A bug
report needs the mechanism; a file manager does not.

---

## 3. Why libp2p

libp2p provides peer identity, encryption, multiplexed streams, NAT
traversal, hole punching and relaying. Building those again would be the
bulk of this project.

### 3.1 Configuration

```text
transports    QUIC (UDP, preferred) · TCP
security      Noise
muxer         QUIC's own; yamux over TCP
identity      Ed25519
services      AutoNAT v1 and v2 · Identify · DCUtR · mDNS
              circuit relay v2 — client here, hop on heimdall
```

QUIC first because it gives real streams with built-in flow control, and
because SCTP-over-DTLS — what a WebRTC DataChannel runs on — has a
throughput ceiling that would miss the near-line-rate goal on a 1 Gbps
LAN. TCP exists for networks that block UDP.

### 3.2 What this deletes from the design

| Would have had to build | libp2p gives |
|-------------------------|--------------|
| A custom signaling protocol | Peers dial addresses. No SDP |
| A mutual challenge-response handshake | Noise authenticates the peer id during the handshake |
| STUN configuration and candidate handling | AutoNAT and Identify — with one large exception, §5.2 |
| Hole punching | DCUtR, over an existing relay connection |
| coturn, TURN credentials, credential minting | Circuit relay v2, with reservations and limits |
| mDNS advertise and browse | `p2p/discovery/mdns` |
| A credit-window flow control scheme | QUIC stream flow control. Writing to a stream blocks |

The last one is worth dwelling on. Under WebRTC, keeping a 10 GB
transfer from eating memory needed a send-buffer watermark plus an
application-level credit window. Over a QUIC stream it is
`io.CopyBuffer`: the writer blocks when the reader is behind. An entire
build step disappears, and the guarantee `SPEC.md` §7 asks for is free.

### 3.3 Streams

A protocol name is a string the application chooses. The transport
registers its own under `/ratatoskr/` for its own business — echo,
observed addresses, address exchange, the benchmark — and never opens
one on the application's behalf.

There is no framing, no envelope, no length prefix imposed from below. A
stream is bytes. Whatever structure the application wants is the
application's, which is what keeps a file protocol out of this
repository.

### 3.4 The relay cannot read anything

`SPEC.md` §6 requires end-to-end encryption even over the relay. Circuit
relay v2 satisfies it by construction: the Noise session runs between
the two peers, and heimdall forwards an already-encrypted byte stream.
It terminates nothing, holds no session key, and cannot tell a directory
listing from a photograph.

What heimdall does learn, and cannot avoid learning: which two peer ids
are talking, when, and how many bytes. That is the honest limit of any
relay, and it belongs written down rather than implied.

### 3.5 Known rough edges

Hole punching is not instant and does not always work; libp2p's own
tracker has open reports of multi-second punches. Two consequences, both
in the design already: the relay carries the session immediately while
the punch runs behind it, so the caller waits for the relay rather than
for the punch; and the punch is retried on a clock rather than three
times at dial, because on a carrier that renumbers, one aim is a guess.

---

## 4. Identity

An Ed25519 keypair, generated on first run. The peer id is a multihash
of the public key, so the id proves itself: the handshake cannot succeed
against a peer that does not hold the matching private key.

```text
identity.key   config dir, mode 0600, never leaves the machine
peer id        derived from the public key
```

Peer ids are long, so `Short()` returns a fingerprint for display and
the full id appears in diagnostics only.

**Not a hardware fingerprint.** MAC addresses, disk serials and machine
UUIDs break when a VM is cloned or a disk is swapped, macOS randomises
MACs, and they leak facts about the machine.

The permission check on `identity.key` is a no-op on Windows, whose Unix
mode bits are synthetic and whose real access control is in ACLs. That
is a known gap, not an oversight.

libp2p answers *who is this peer*. It does not answer *may this peer
read my Documents*. That question is not asked in this repository at
all — the application receives an authenticated `PeerID` on every stream
and decides for itself.

---

## 5. Discovery

Given a peer id, find an address and dial it.

### 5.1 LAN first, with a head start

```text
connect <peer id>
  |
  +-- t=0      mDNS on the local network          no Internet needed
  |            answers in 50-200 ms -> dial it, and ask nothing else
  |
  +-- t=400ms  addresses from the application     only if the LAN is quiet
```

A strict sequence would make every remote connection wait out the mDNS
timeout. A plain race would go to the network even for a machine on the
same switch. The head start avoids both.

Fall back on either trigger: mDNS timed out, **or** the peer was found
on the LAN but the dial failed. A local firewall can block the transport
port while multicast still works. Finding is not reaching.

`OnLAN` hands the application what mDNS found. `NoLAN` turns the whole
thing off, because some networks and some endpoint security products
object to a multicast socket existing at all.

### 5.2 Learning this machine's own address

A machine learns where it appears from outside in two ways, and the
difference is the whole of the NAT problem.

Asking a relay names the port of a connection opened at startup, which
on a carrier that renumbers is stale within minutes. Instead
`transport/selfaddr.go` wraps libp2p's own QUIC socket, asks
every reflector in `internal/stun` on it, and claims the replies before
quic-go sees them. What `Addrs()` offers is therefore measured on the
socket that actually punches, is never older than 27 seconds, and is a
*set* rather than a single guess.

It is still not proof. The port toward a peer is not necessarily the
port toward a reflector, which is why the punch is retried rather than
aimed once. Three earlier readings of this said otherwise and were
wrong; the notebook that recorded them is in git history.

### 5.3 A LAN session stays on the LAN

Discovering on the LAN is not enough by itself. A connection dialled
from mDNS uses the LAN address only, with no relay in the dial set, so
nothing about that session leaves the network. `DialPeer` strips relay
addresses from the dial set, and that is load-bearing rather than
tidiness.

This makes "works with the Internet unplugged" testable rather than
accidental.

---

## 6. The ladder, walked in both directions

`SPEC.md` §5 requires LAN, then direct, then relay — and requires it
without the caller asking.

One function decides. Both connection-change hooks call `repair`, which
reads the *best* path to the peer rather than the connection that fired:

```text
relay    -> climb.    upgrade(): re-dial directly every 5 seconds,
                      from both ends, for as long as the peer stays relayed
unknown  -> descend.  restore(): every known direct address in one dial,
                      then the relay, for 30 seconds
```

They compose. Landing on the relay fires a connection event, which calls
`repair`, which starts the climb again. Nothing above the transport is
involved in either direction.

Each retry **races** the whole ladder rather than walking it — LAN, the
Internet, and the relay together — because a LAN dial finishes in a
millisecond or two while a punch is still on its first round trip, so
the lowest rung that exists wins on its own.

Both ends time from the same event, the relayed connection, so their
dials cross. That is what makes two dials a hole punch. This is
Tailscale's shape rather than DCUtR's: DCUtR still runs, tries three
times at connection time, and whichever lands first ends both.

The LAN rung needs `/ratatoskr/addrs/1.0.0` to exist at all. Identify
discards every private address it is told over a public connection, and
a circuit through a relay on a VPS is a public connection — so two
machines on one LAN that meet over heimdall are never told each other's
LAN address by libp2p. `askAddrs` asks the peer directly on each tick.
Measured 9 Sep 2026: a relayed pair on one LAN moved to the LAN in 10
seconds and then ran at 107.7 MB/s.

What the ladder cannot do is save the stream that was in flight. §2.2.

---

## 7. The path, reported honestly

Two questions that look alike and are not. Mixing them produces a UI
that lies.

**Whether a peer is reachable at all** is the application's to answer,
from whatever registry it keeps. The transport will not guess it.

**Which path a live connection took** is measured here, from the
connection itself:

```text
lan       a private address on this network
direct    a public address, across the Internet
relay     through heimdall; these bytes cost someone money
unknown   no connection
```

`lan` and `direct` are both direct connections and the difference is
*where*. Never write "direct" to mean "not relayed" — one of the three
paths is called that, and a log line that confuses them turns a
ten-second LAN upgrade into what reads like an Internet round trip.

`PathTo` returns the best path among the live connections, not the first
one it finds. `BetterThan` holds the order, and is the only ranking in
the repository.

Whether to care is a question of size. A metadata request is a kilobyte;
relaying it is free and consulting the path for it is wasted code. A
bulk transfer is the one caller that should watch, and the recipe is in
§2.2: finish the current stream, open the next on the better path.
`ratatoskr bench` demonstrates it, checking every four megabytes.

`relay` is a state to report and get out of, and getting out of it is
continuous rather than a single attempt at dial time. `SPEC.md` §2 is
why.

---

## 8. heimdall

A circuit relay v2 node on a public host. It holds reservations, applies
limits, and forwards.

```text
UDP  <port>       QUIC
TCP  <port>       TCP
TCP  443          for networks that allow nothing else
```

Binding 443 needs root or `CAP_NET_BIND_SERVICE`; when the process has
neither, that listener does not come up and the others still do, because
libp2p tolerates a partial listen. `HEIMDALL_ANNOUNCE` replaces the
announced set when the public address differs from the bound one.

An agent enables the relay client and forces a reservation whenever a
relay is configured. Forcing it is deliberate: AutoNAT wants several
independent peers to agree before it rules a machine unreachable, and a
private drive never has that many. `RATATOSKR_ASSUME_PUBLIC` opts a
genuinely reachable machine out.

Two traps this cost real time. libp2p marks a relayed connection
*limited* and refuses streams on it unless the dial passes
`network.WithAllowLimitedConn`. And a circuit address does not appear in
`host.Addrs()` on loopback, so `run` prints the peer id to copy rather
than an address it cannot promise.

**One relay named in a file is the development shape, not the deployed
one.** `FLEET.md` is the design that replaces it: many relays with
ephemeral addresses, a coordinator called `bifrost` that places and
meters them, bandwidth divided per subject by demand rather than per
machine by a divisor, and
a VPS count that follows committed demand. It changes nothing in §2.1 —
the application above still hands the transport opaque strings and never
learns what a relay is.

---

## 9. Build and platform rules

**No cgo, ever.** go-libp2p is pure Go. Keep it that way and one machine
builds every target:

```text
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/ratatoskr.exe ./cmd/ratatoskr
```

That single constraint is what makes `SPEC.md` §10 true, and it rules
out every Go GUI toolkit, every tray-icon library, `mattn/go-sqlite3`,
and anything wrapping a C image or compression library. None of those
belong here anyway; the rule is recorded because the consumer inherits
it through the module.

---

## 10. Repository layout

```text
p2p-transport/
├── cmd/
│   ├── ratatoskr/     test harness and diagnostics
│   ├── heimdall/      relay node
│   └── bifrost/       the coordinator — FLEET.md
├── transport/         the package: the surface, the ladder, the
│                      self-address measurement, the diagnostics view
├── internal/
│   ├── discovery/     mDNS
│   ├── identity/      keypair, peer id
│   ├── config/        per-OS paths, environment overrides, relays
│   ├── stun/          address reflectors
│   ├── wire/          protocol ids heimdall and the transport share
│   ├── relay/         the vendored hop, with a per-subject shaper
│   └── provision/     one adapter per VPS provider
├── scripts/           punch experiments with no libp2p in them
├── Makefile · SPEC.md · PLAN.md · TODO.md · FLEET.md · go.mod
```

`transport/` is the module root's public package as of step 4; §2.1 is
its whole exported surface and `example_test.go` beside it is the check,
because its import block is every package a consumer has to name.
Everything else is `internal/`, which is what makes the seam a rule the
compiler enforces rather than a convention.

`internal/wire` is the small exception the fleet will grow: the protocol
identifiers two binaries in this module must agree on. Heimdall answers
one of the transport's own protocols, and a wire constant written twice
is a wire constant that will one day differ.

`scripts/punch.py` is the same punch with no libp2p in it, and is the
control every failure needs beside it. `scripts/punchpair.sh` runs both
ends against a rendezvous so neither needs an operator waiting on the
other.

---

## 11. Build order

| Step | Goal | Passes when |
|------|------|-------------|
| 0 | libp2p echo, addresses pasted by hand | a string echoes over a stream |
| 1 | Identity and config | `ratatoskr id` is stable across restarts on all three OSes |
| 2 | mDNS discovery, LAN dial | `discover` finds the other machine and connects with no server |
| 3 | **NAT spike**: relay, punching, throughput | two machines on different networks connect directly; the rate and MB/s are written down |
| 4 | **The seam**: the surface of §2.1, public | a consumer package compiles against it without importing libp2p |
| 5 | Path changes are pushed, not polled | a transfer moves to a better path without asking every four megabytes |
| 6 | Survival | sleep, wake, network change, cable pull, restart — reconnects, never hangs, leaks no goroutines |
| 7 | **The relay fleet** (`FLEET.md`) | a relay is killed mid-transfer and the pair lands on another; a subject's rate divides by demand across its machines wherever they are placed, and follows a limit changed while it runs |
| 8 | Windows and Linux | every check above passes on all three, firewall prompts documented |
| 9 | Freeze and tag | v1.0.0, a README for the consumer, one worked example |

Steps 0 to 3 are done. Step 3 was the one that could have failed.

The order is the risk order. Step 3 came fourth because it is where the
libp2p choice was proved or disproved, and nothing above it should have
been built on an unmeasured assumption about throughput.

---

## 12. Risks

| Risk | Likely | Response |
|------|--------|----------|
| Hole punch rate is poor, so relay use is high | Medium | Measured at step 3, before anything depends on it. Relay use decides the bandwidth bill |
| Throughput misses the 1 Gbps LAN goal | Low | Measured: 107.7 MB/s on Wi-Fi after a LAN upgrade. It is why libp2p was chosen over WebRTC |
| Hole punching takes seconds | Certain sometimes | Serve over the relay immediately, climb behind it |
| A carrier renumbers every destination | Met | The address set is re-measured on the punching socket every 27 seconds and the punch is retried, not aimed |
| libp2p is a large dependency to debug | Medium | Accepted. It replaces signalling, punching, relaying and mDNS. Pin versions; keep §2.1 narrow enough that it stays replaceable |
| The seam leaks and the consumer imports libp2p | Medium | The check in step 4 is exactly this, and it is mechanical |
| mDNS blocked on real networks | Medium | Falls through after the head start |
| Firewall prompts confuse users at first run | High | The multicast port and the transport port. A packaging problem — noted now |
| Endpoint security kills the agent on a managed machine | High | Met on the development Mac: eight address queries followed by dials to arbitrary peer ports score as a scanner, so the process is killed, the binary deleted, and the launching terminal killed with it. Signing and notarisation are the floor; a fleet needs an exclusion by hash |
| Upload speed at the serving end is the real ceiling | Certain | Nothing to fix. Report it honestly |

---

## 13. Dependencies

| Project | Role | Licence |
|---------|------|---------|
| go-libp2p | transport, identity, NAT traversal, relay, mDNS | MIT / Apache-2.0 |

Ed25519, SHA-256 and base32 come from the standard library. That is the
entire list, and keeping it that short is a feature: a consumer takes on
this module's dependency tree whole.

---

## 14. What the consumer builds

Not here, and named so nobody adds them here by accident: files and
folders, a file protocol, WebDAV or any other adapter, accounts and
device registries, authorisation policy, resume state and integrity
hashes, any user interface.

`SPEC.md` §11 is the same list, from the requirement's side.
