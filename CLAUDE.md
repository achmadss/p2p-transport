# CLAUDE.md

Guidance for Claude Code (claude.ai/code) working in this repository.

## What this is

**A transport, and nothing above it.** It moves bytes between two
machines that belong to the same person, over the best path it can find,
and tells the caller only what the caller genuinely needs: who the peer
is, a stream to it, and which path the bytes are taking.

It is not an application. There are no files here, no folders, no
accounts, no user interface. Those belong to whatever is built on top —
a private network drive is the reason this exists, and it is a different
repository. The whole design question here is what that consumer is
allowed to see. `PLAN.md` §2 is the answer; treat it as the point of the
project rather than a detail of it.

Module `github.com/achmadss/p2p-transport`. One Go module, three
binaries and a package:

| Name | Role |
|------|------|
| `transport` | The package at the module root. The surface an application uses; everything else is `internal/`. |
| `heimdall` | A relay node. Forwards encrypted bytes it cannot read. |
| `bifrost` | The relay coordinator: places, admits and sets rates; metering and scaling are designed in `FLEET.md` and not built. |
| `ratatoskr` | The test harness — `id`, `run`, `discover`, `connect`, `bench`, NAT diagnostics. Not a product. |

The Norse names are on the wire already, in protocol identifiers like
`/ratatoskr/addrs/1.0.0`. They stay.

## Read these first, in order

- **`SPEC.md`** — the requirement. Treat it as fixed. If a change would
  contradict it, say so rather than silently diverging.
- **`PLAN.md`** — the design that satisfies the spec. Numbered sections;
  cite them (`PLAN.md §6`) rather than restating them.
- **`TODO.md`** — the ordered work, step 0 to step 9, each with a
  concrete pass/fail check and what was measured on the way. **It holds
  the state of the project and this file does not repeat it.**
- **`FLEET.md`** — the relay fleet: many relays, a coordinator that
  places and meters them, per-subject bandwidth, and autoscaling. Read it
  before touching `cmd/heimdall`.

Two deletions worth knowing about, both on 9 Sep 2026. `NAT.md`, the step
3 notebook, held every measurement taken while finding out whether two
machines can reach each other directly, and the three readings that
turned out to be wrong on the way; the conclusions survive in `PLAN.md`
§3.5, §5.2 and §6, and the narrative is in `git log` and is worth reading
before proposing anything new about hole punching. And the scope
narrowed: these documents used to specify a whole product — WebDAV, a
coordinator with accounts, thumbnails, a web UI — and now specify layer 4
only. `git log` has the original wording; do not re-add any of it here.

## Current state

Steps 0 to 5 are done. Step 6 is half done: the two boxes that are code
are ticked, and the three that need a person moving a cable are not. Step
7 has started: `FLEET.md` §9 steps 1 and 2 are built — relays register,
machines lease, a machine the coordinator did not place is refused, and
each subject's bytes pass through one bucket that a rate change reaches
mid-transfer — and none of it has carried a byte outside a test. The
allowance loop, metering and scaling are designed and not built. `TODO.md` has the
per-step detail, including why each decision went the way it did.

Numbers, because a claim without one is not allowed here. All taken 9 Sep
2026 on the owner's machines:

| What | Rate |
|------|------|
| LAN over Wi-Fi | 65 MB/s |
| Relayed through heimdall on a VPS | 3.7 MB/s |
| A relayed pair on one LAN, after it moved to the LAN | 107.7 MB/s, 10 s to move |

**The direct path across a carrier NAT works, and the number behind it is
missing.** For most of step 3 it did not: the carrier tested gives every
new destination an unrelated port, so no address a third party observes
names the door a peer must dial. On 9 Sep 2026 the owner's runs passed,
hotspot to home line included, without the output being captured. That is
a verdict rather than a measurement — re-measure before building on a
rate, because unrecorded readings are what every retraction in the old
notebook had in common.

## Environment variables

| Variable | Effect |
|---|---|
| `RATATOSKR_CONFIG_DIR` | Where state lives. Set it to run two agents on one machine; the tests rely on it. |
| `RATATOSKR_NO_MDNS` | Start without opening a multicast socket, for a network or an endpoint agent that objects to one. It is not armour — see **Commands**. |
| `RATATOSKR_DIAG` | Print everything below the seam. The one exception to the seam rule. |
| `RATATOSKR_ASSUME_PUBLIC` | Skip the forced relay reservation on a machine that really is reachable. |
| `RATATOSKR_UPGRADE_EVERY`, `RATATOSKR_UPGRADE_DIAL` | How often a relayed peer is re-dialled, and one attempt's budget. |

`RATATOSKR_RELAYS` names the relay to use and `RATATOSKR_COORDINATOR`
names the coordinator that hands one out; set one or the other, and
`config.json` holds the same two. `heimdall -h` and `bifrost -h` list
their own. The fleet's arithmetic is
all knobs with defaults: `BIFROST_HEADROOM` and `BIFROST_PACK_TO` decide
how many machines fit on a relay, `BIFROST_SUBJECT_MIN` and
`BIFROST_SUBJECT_MAX` are a subject's floor and ceiling when the
application does not say, and `HEIMDALL_BANDWIDTH` is what a relay claims
it can forward **per direction** — a relayed byte crosses the machine
twice, so it is not the provider's headline number.

## Commands

**Enterprise security agents stop this app from running.** The Mac used
for development has Palo Alto Cortex XDR and Leagsoft EPP endpoint
security extensions active, and they treat an agent that asks eight STUN
servers at once and then dials arbitrary ports on a peer as a port
scanner. What happens is not a crash and leaves no crash report: the
process is killed, `dist/ratatoskr` is deleted off disk, and the
application that launched it is killed too — so a `run` started from
Terminal.app takes every Terminal window with it, while a different
terminal app is untouched.

So on this machine the binaries are started by the person at the
keyboard, never by an agent working on the repo. `make build`, `make vet`
and the test list below are safe; `run`, `bench`, `connect` and
everything in `scripts/` are not. On a managed laptop the fix is a signed
binary and an exclusion by hash from whoever runs the agent, and that is
a packaging problem rather than a local one.

**`RATATOSKR_NO_MDNS=1` is not armour, so do not paste it onto
everything.** Runs that set it have been killed just the same. It exists
so the agent can start on a network where multicast is unwelcome, and
that is the only thing it does. Put it on `run`, `discover` or `connect`
when a network objects to multicast, and leave it off otherwise.

The test list below does not need it. `internal/discovery` is the only
package that opens a real multicast socket and it is not in the list:
`transport/example_test.go` sets `NoLAN` in code, and every other test
builds a host with no discovery on it. That is also why `make test` and
`go test ./...` stay off this machine — they include
`internal/discovery`, which the variable cannot rescue, since setting it
denies those tests the socket they are testing. So they do not get run
here at all.

```bash
make build                      # -> dist/ratatoskr
make vet
make cross                      # all five targets from one machine
make clean

# tests, without internal/discovery
go test ./transport/ ./internal/config/ ./internal/identity/ ./internal/shape/ ./cmd/... -count=1
```

## Constraints that are not negotiable

Each one exists for a reason recorded in `PLAN.md`. Breaking any of them
is a design change, not a refactor.

- **The seam holds.** Nothing that layer 7 touches may name QUIC, Noise,
  multiaddrs, circuits, DCUtR, AutoNAT, STUN, reservations, hole punches
  or mDNS — not in an exported identifier, not in a returned error, not
  in output a person reads. `RATATOSKR_DIAG=1` is the one exception and
  it prints everything. `SPEC.md` §4, `PLAN.md` §2.3. The compiler
  enforces the shape — anything a consumer must not name is in
  `internal/` — and `go doc ./transport` is the audit.
- **`CGO_ENABLED=0`.** Enforced in the Makefile. It is what lets one
  machine cross-build every target, and the consumer inherits it through
  the module. This rules out every Go GUI toolkit, `mattn/go-sqlite3`
  (use `modernc.org/sqlite`) and anything wrapping a C library.
  `PLAN.md` §9.
- **No server on the data path.** Bytes go peer to peer, or through the
  relay as ciphertext. Any design that routes content through a server
  you control is wrong here.
- **Nothing above layer 4 gets added.** No file verbs, no path handling,
  no WebDAV, no accounts, no authorisation policy, no UI. If a change
  needs one of those, it belongs in the consumer. `SPEC.md` §11. The
  fleet is the near miss: relays, placement and bandwidth *are* layer 4,
  and they are expressed against opaque subject ids so that accounts stay
  out.
- **Every performance claim is a number someone measured**, with its date
  and the machines it was taken on. A verdict is not a measurement. This
  was forgotten three times and retracted three times.
- **The build order is the risk order.** Do not skip ahead.

## Architecture, in the parts that span files

**The surface.** `PLAN.md` §2.1 is the whole contract, `transport/api.go`
is where it lives, and `transport/example_test.go` is the check: its
import block is `bufio context errors fmt io os time` plus this module,
and if libp2p ever appears there the seam has leaked. Three facts cannot
be hidden from the caller and the design says so out loud — identity is
not authorisation, a stream dies with its connection, and bytes cost
differently on different paths.

Two things about it are easy to undo by accident. `Config` carries `Dir`,
**not a key** — a private key is a libp2p type, so naming one would force
every caller to import libp2p. And `internal/wire` holds the protocol ids
heimdall and the transport must agree on, because a wire constant written
twice is one that will one day differ.

**Identity vs authorisation.** The Noise handshake proves *who a peer
is*, so a caller needs no challenge of its own. It says nothing about
what they may do, and this repository does not ask.

**A stream is raw bytes** with a protocol name the caller picks and no
framing imposed from below. Backpressure is `io.CopyBuffer` — QUIC's
stream flow control blocks the writer, so there is no credit window to
build.

**Discovery is LAN-first with a head start.** mDNS at t=0; the caller's
own addresses at t=400 ms, only if the LAN stayed quiet. A LAN session
stays on the LAN because the caller hands `Connect` the LAN addresses and
nothing else: given addresses, those are the dial set. That is
load-bearing rather than tidiness — a machine found here must be reached
here or not at all — and `cmd/ratatoskr`'s `open()` is the worked
example.

**The path is never decided once.** LAN if the peer is here; otherwise
the relay carries the session while `transport/upgrade.go` re-dials the
peer directly, from both ends, every five seconds for as long as it stays
relayed. Both ends time from the same event — the relayed connection — so
their dials cross, and two crossing dials are a hole punch. Nothing
switches over: libp2p prefers the direct connection for every stream
opened after it lands, and `PathTo` reports the change because it reads
the live connections. This is Tailscale's shape, not DCUtR's, which still
runs beside it and tries three times at connection time.

`repair` is the one place that decides which way the ladder is walked.
Both notifee hooks call it on every connect and disconnect, and it reads
the *best* path to the peer rather than the connection that fired: on
`relay` it climbs with the punch loop; on `unknown` it descends with
`restore`, which tries all known direct addresses in one dial and only
then whatever relay this machine has right now, for 30 seconds. Landing on the relay is not
the end of it, since that arrival starts the punch loop again.

Each retry races the whole ladder rather than walking it, because a LAN
dial finishes in a millisecond or two while a punch is still on its first
round trip, so the lowest rung that exists wins on its own. The LAN rung
needs `/ratatoskr/addrs/1.0.0` to exist at all: identify drops every
private address it is told over a public connection, a circuit through a
VPS is a public connection, and so two machines on one LAN that meet over
heimdall are never told each other's LAN address by libp2p. `askAddrs`
asks the peer directly each tick.

`lan` and `direct` are both direct connections and the difference is
where: `lan` is a private address on this network, `direct` is a public
one across the Internet. Never print the word "direct" to mean "not
relayed" — one of the three paths is called that.

**A machine learns its public address two ways**, and the difference is
the whole of step 3. Asking a relay over `/ratatoskr/observed/1.0.0`
names the port of a connection opened at startup, which on a carrier that
renumbers is stale within minutes. `transport/selfaddr.go` wraps libp2p's
own QUIC socket through `quicreuse.OverrideListenUDP`, asks every
reflector in `internal/stun` on it, and claims the replies before quic-go
sees them — so what is offered is measured on the socket that punches,
never older than 27 seconds, and a set rather than a guess. It is still
not proof: the port toward a peer is not the port toward a reflector,
which is why the punch is retried rather than aimed once.

**A stream never moves.** It is bound to the connection it was opened on,
and neither libp2p nor QUIC offers a migration that would change that
(QUIC's moves one connection between *local* addresses; the relay and the
peer are two remote endpoints). So a caller moving bulk data waits on
`Watch`, and when a better path lands it finishes the stream it is on and
sends the rest on a new one — `Path.BetterThan` holds the order, and
`ratatoskr bench` is the worked example. What the transport cannot do is
save the request that was in flight: reissuing it is resume, and resume
is the caller's, because only the caller knows what an offset means.

**Rates are shaped by subject, and the hook is a host.** `relay.New`
takes a `host.Host`, which is an interface, and every byte a relay
forwards arrives through two of its methods: the handler registered for
machines dialling in, and the stream opened to the machine being dialled.
`cmd/heimdall/shape.go` embeds the real host and overrides exactly those
two, so `internal/shape` sees the whole byte path with no fork of
libp2p's relay. Reads are shaped and writes are not, on purpose: a
forwarded byte is read from one leg and written to the other, so shaping
reads charges it once. One bucket per subject and no per-circuit limit —
equal division is the one split that wastes whatever a slow machine
cannot use — with the relay's own ceiling underneath.

## Traps

Things that cost hours once and will again.

- libp2p marks a relayed connection *limited* and refuses streams on it
  unless the dial passes `network.WithAllowLimitedConn`.
- AutoNAT wants several independent peers to agree before it rules a
  machine unreachable, and a private drive never has that many — so
  configuring a relay forces the reservation.
- A circuit address does not appear in `host.Addrs()` on loopback, so
  `run` prints the peer id to copy rather than an address it cannot
  promise.
- The `identity.key` permission check is a no-op on Windows, whose Unix
  mode bits are synthetic and whose access control lives in ACLs.
- `scripts/punch.py` is the same punch with no libp2p in it, and is the
  control every DCUtR failure needs beside it. `scripts/punchpair.sh`
  runs it on both machines against a rendezvous on the VPS.
- Tailscale is cloned at `/Users/a2193/Documents/Personal/tailscale` — a
  sibling to read, not a dependency. Read `derp` to understand the
  fallback they chose, and do not adopt it.

## Writing style for this repo

Commit messages and the Markdown files are prose, not bullet dumps. State
what changed and why the alternative was rejected. The existing git
history is the reference.

**Code comments are the opposite.** They say what the code does and how
to use it, briefly, and they never cite a document: no `PLAN.md §2.1`, no
`SPEC.md §4`, no `TODO.md step 7`. A comment that points at a section
number rots the moment the section moves, and it sends a reader out of
the file to learn something the comment should have said. Keep the reason
a line of code exists when forgetting it would break the code again — one
sentence, in the comment itself.

Never surface `QUIC`, `DCUtR`, `AutoNAT`, `multiaddr` or `circuit` in
user-facing output outside a diagnostics view (`SPEC.md` §4).
