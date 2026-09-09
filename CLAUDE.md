# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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

Module `github.com/achmadss/p2p-transport`. One Go module, two binaries
and a package:

| Name | Role |
|------|------|
| `transport` | The package at the module root. The surface an application uses; everything else is `internal/`. |
| `heimdall` | A relay node. Forwards encrypted bytes it cannot read. |
| `bifrost` | The relay coordinator: places, meters and scales the fleet. Designed in `FLEET.md`, not built. |
| `ratatoskr` | The test harness — `id`, `run`, `discover`, `connect`, `bench`, NAT diagnostics. Not a product. |

The Norse names are on the wire already, in protocol identifiers like
`/ratatoskr/addrs/1.0.0`. They stay.

## Read these first, in order

- **`SPEC.md`** — the requirement. Treat it as fixed. If a change would
  contradict it, say so rather than silently diverging.
- **`PLAN.md`** — the design that satisfies the spec. Numbered sections;
  cite them (`PLAN.md §6`) rather than restating them.
- **`TODO.md`** — the ordered work, step 0 to step 9, each with a
  concrete pass/fail check. Steps, not findings: what was measured on
  the way lives beside it.
- **`FLEET.md`** — the relay fleet: many relays, a coordinator that
  places and meters them, per-subject bandwidth, and autoscaling. Read
  it before touching `cmd/heimdall`.

The step 3 notebook, `NAT.md`, was deleted on 9 Sep 2026. It held every
measurement taken while finding out whether two machines can reach each
other directly and the three readings that turned out to be wrong on the
way. The operative conclusions survive in `PLAN.md` §3.5, §5.2 and §6
and below; the narrative is in `git log` and is worth reading before
proposing anything new about hole punching.

The scope narrowed on 9 Sep 2026. These documents used to specify a
whole product — WebDAV, a coordinator with accounts, thumbnails, a web
UI — and now specify layer 4 only. `git log` has the original wording if
it is ever wanted; do not re-add any of it here.

## Current state

**Steps 0 to 5 are done.** The transport works, has been measured, and
has the surface an application depends on.

**Step 0** — `transport` is a libp2p host with QUIC and TCP,
Noise security and an Ed25519 identity.

**Step 1** — `internal/config` owns the per-OS config directory (0700)
and `config.json`; `internal/identity` owns `identity.key` (0600) and
refuses to start when it is readable by other users. `ratatoskr id`
prints the short fingerprint, `--full` the peer id. Both take a
directory, with `""` meaning the environment override or the per-OS
default, so an embedding application says where its state lives rather
than inheriting a variable it did not set. Easy to miss: the permission
check is a no-op on Windows, whose Unix mode bits are synthetic and
whose access control lives in ACLs.

Set `RATATOSKR_CONFIG_DIR` to run two agents on one machine. The tests
rely on it.

**Step 2** — `internal/discovery` advertises and finds peers over mDNS
(`_ratatoskr._udp`), re-advertising when this machine's addresses
change. It pushes each answer to a callback, which is what `OnLAN` hands
up. `run`, `discover` and `connect` work with no server and no Internet.
Still open: the Windows and Linux runs, the firewall prompts, and the
unplugged-router test — all now step 8.

**A LAN session stays on the LAN because the caller hands over the LAN
addresses and nothing else.** Step 4 moved that rule out of `DialPeer`'s
circuit-stripping and into `Connect(ctx, id, addrs)`: given addresses,
those are the dial set. It is load-bearing rather than tidiness — a
machine found here must be reached here or not at all — and
`cmd/ratatoskr`'s `open()` is the worked example.

**Step 3 was the step that could have failed.** `cmd/heimdall` is a
circuit relay v2 node on a real VPS. The agent always enables AutoNAT
and DCUtR, and adds the relay client and a forced reservation when
`config.json` names a relay; `connect --via lan|relay|auto` picks a
path, and `auto` gives the LAN a 400 ms head start, per `PLAN.md` §5.
LAN and relay are measured: 65 MB/s over Wi-Fi, 3.7 MB/s relayed.

**The direct path across a carrier NAT works, and the number behind it
is missing.** For most of this step it did not: the carrier tested gives
every new destination an unrelated port, so no address a third party
observes names the door a peer must dial, and every aim tried failed. On
9 Sep 2026 the owner's runs passed, hotspot to home line included,
without the output being captured. That is a verdict rather than a
measurement — re-measure before building on a rate, because unrecorded
readings are what every retraction in the old notebook had in common.

What was built in between, and is all in place: a *set* of addresses
re-measured every 27 seconds rather than one taken at startup, a punch
retried every five seconds for as long as a peer stays relayed rather
than DCUtR's three attempts, `/ratatoskr/addrs/1.0.0` to recover the LAN
address identify discards over a public connection, and a transfer that
moves itself onto a better path while it is still running. The relay
remains the fallback and carrying real data over it is not a failure:
`SPEC.md` §2 permits it, and only forbids it being the *normal* path.

**Step 7 is designed and not built.** `FLEET.md` replaces the single
relay named in `config.json` with a fleet: ephemeral relays, a
coordinator that places and meters them, bandwidth divided per *subject*
by demand, and autoscaling between a minimum and a maximum VPS count.

Three things in it are easy to get wrong and are written down for that
reason. **Demand does the dividing, not a divisor**: one shared bucket
per subject, no per-circuit limiter, so a machine on a slow link takes
what it can and leaves the rest — an equal split wastes whatever the
slow machines cannot use. A subject's circuits **spread across relays**,
so its rate is re-divided between them every second by max-min
allocation; an earlier draft pinned a subject to one relay and that was
wrong twice over. And go-libp2p's relay has **no per-connection rate
hook** — `WithLimit` is a byte cap and `BytesTransferred` carries no
peer id — so the shaper needs a vendored copy of the hop.

**Step 4 — the seam — is done.** `transport` is the module root's public
package: `New(Config)`, `ID`, `Addrs`, `Connect`, `Open`, `Handle`,
`Peers`, `PathTo`, `Watch`, `OnLAN`, `Close`, the `PeerID`, `Stream` and
`Path` types, and the `ErrUnreachable` and `ErrNotHandled` sentinels. `PLAN.md` §2.1 is the whole of it, `transport/api.go` is
where it lives, and everything else in that package is below the seam
and unexported. `transport/example_test.go` is the check and it is
mechanical: its import block is `bufio context fmt io os time` plus this
module, and if libp2p ever appears there the seam has leaked.

Four things about it to know before touching it. `Config` carries `Dir`,
**not a key** — a private key is a libp2p type, so naming one would
force every caller to import libp2p, and the transport loads or
generates `identity.key` itself. `RATATOSKR_DIAG` output lives in
`transport/diag.go` and starts from `New`, because what it prints is
below the seam and reaching it from outside meant exposing the host.
`internal/wire` holds the protocol ids heimdall and the transport must
agree on, because a wire constant written twice is one that will one day
differ. And a protocol name is now a plain string the caller picks: the
harness declares its own `echoProto` and `benchProto` the way any
application would.

**Step 5 — path changes are pushed.** `Watch(id) (<-chan Path, func())`
hangs off the same notifee `repair` runs on, so no goroutine and no timer
were added: connect and disconnect are every event that can change a
path. `changed` repairs the peer and publishes what repair read. The
channel buffers one path and drops a stale one, because the writer is
libp2p's connection hook and must never block on an application that
stopped reading; the current path is delivered before `Watch` returns, so
nothing is missed between asking and listening. `bench` lost its
four-megabyte poll — `writeChunk` is 512 KB and is now only write
granularity — and `watchUpgrade` lost its ticker.

`ErrUnreachable` and `ErrNotHandled` split the one failure a caller has
to act on differently: wait on `Watch` and retry, or stop. `Read` and
`Write` tag a stream that died the same way and leave `io.EOF` alone.

**Step 6 is next**: survival — see `TODO.md`.

Tailscale is cloned at `/Users/a2193/Documents/Personal/tailscale` — a
sibling to read, not a dependency; nothing here imports it and
`CGO_ENABLED=0` and the layout in `PLAN.md` §10 still bind. Read `derp`
to understand the fallback they chose and do not adopt it.

Four traps step 3 exposed. libp2p marks a relayed connection *limited*
and refuses streams on it unless the dial passes
`network.WithAllowLimitedConn`. AutoNAT wants several independent peers
to agree before it rules a machine unreachable, and a private drive
never has that many, so configuring a relay forces the reservation and
`RATATOSKR_ASSUME_PUBLIC` opts a genuinely reachable machine out. A
circuit address does not appear in `host.Addrs()` on loopback, so `run`
prints the peer id to copy rather than an address it cannot promise. And
`RATATOSKR_NO_MDNS` exists to run the agent without opening a multicast
socket at all, which some networks and some endpoint security agents
object to. One machine here runs such an agent, and it deletes the
binary and kills the terminal that started it; see **Commands** before
running anything there.

A machine learns its public address two ways, and the difference is the
whole of step 3. Asking a relay over `/ratatoskr/observed/1.0.0` names
the port of a connection opened at startup, which on a carrier that
renumbers is stale within minutes. `transport/selfaddr.go`
wraps libp2p's own QUIC socket through `quicreuse.OverrideListenUDP`,
asks every reflector in `internal/stun` on it, and claims the replies
before quic-go sees them — so what is offered is measured on the socket
that punches, never older than 27 seconds, and a set rather than a
guess. It is still not proof: the port toward a peer is not the port
toward a reflector, which is why the punch is retried rather than aimed
once.

`scripts/punch.py` is the same punch with no libp2p in it, and is the
control every DCUtR failure needs beside it. `scripts/punchpair.sh` runs
`punch-quic` on both machines against a rendezvous on the VPS, so
neither needs an operator waiting on the other.

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
keyboard, never by an agent working on the repo. `make build`, `make
vet` and the test list below are safe; `run`, `bench`, `connect` and
everything in `scripts/` are not. On a managed laptop the fix is a
signed binary and an exclusion by hash from whoever runs the agent, and
that is a packaging problem rather than a local one — every user on a
corporate machine meets the same wall.

**`RATATOSKR_NO_MDNS=1` still goes on every binary this repo builds and
every script in `scripts/`**, whichever one it is, including ones added
later. Opening a multicast socket is one more unusual behaviour on a
machine that is being watched for them, and the flag costs nothing. Put
it on the command itself rather than in a parent shell a later command
may not inherit. It is a precaution and not protection: runs with the
flag set have been killed anyway.

That extends to the tests, because `internal/discovery` opens a real
multicast socket. `make test` and `go test ./...` are therefore unsafe
here; run an explicit package list instead. The discovery tests cannot
be rescued by setting the variable — it denies them the socket they are
testing — so on this machine they do not get run at all.

```bash
make build                      # -> dist/ratatoskr
make vet
make cross                      # all five targets from one machine
make clean

# tests, without internal/discovery
RATATOSKR_NO_MDNS=1 go test ./transport/ ./internal/config/ \
    ./internal/identity/ ./cmd/... -count=1
```

## Constraints that are not negotiable

Each one exists for a reason recorded in `PLAN.md`. Breaking any of them
is a design change, not a refactor.

- **The seam holds.** Nothing that layer 7 touches may name QUIC, Noise,
  multiaddrs, circuits, DCUtR, AutoNAT, STUN, reservations, hole punches
  or mDNS — not in an exported identifier, not in a returned error, not
  in output a person reads. `RATATOSKR_DIAG=1` is the one exception and
  it prints everything. `SPEC.md` §4, `PLAN.md` §2.3. Since step 4 the
  compiler enforces the shape of it — anything a consumer must not name
  is in `internal/` — and `go doc ./transport` is the audit.
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
  and they are expressed against opaque subject ids so that accounts
  stay out.
- **Every performance claim is a number someone measured**, with its
  date and the machines it was taken on. A verdict is not a measurement.
  This was forgotten three times and retracted three times.
- **The build order is the risk order.** Do not skip ahead.

## Architecture, in the parts that span files

**The surface.** `PLAN.md` §2.1 is the whole contract: an identity, a
stream, a path, and addresses that are opaque strings out of one machine
and into another. Three facts cannot be hidden from the caller and the
design says so out loud — identity is not authorisation, a stream dies
with its connection, and bytes cost differently on different paths.
Everything else about layer 4 is concealed.

**Two things, not one channel.** A protocol name is a string the caller
picks; a stream is raw bytes with no framing imposed from below.
Backpressure is `io.CopyBuffer` — QUIC's stream flow control blocks the
writer, so there is no credit window to build.

**Identity vs authorisation.** The Noise handshake proves *who a peer
is* — no separate challenge-response is needed. It says nothing about
what they may do, and this repository does not ask.

**Discovery is LAN-first with a head start.** mDNS at t=0; the caller's
own addresses at t=400ms, only if the LAN stayed quiet. A LAN-discovered
session dials the LAN address with no relay in the dial set, so nothing
leaves the network. Falls back on either trigger: mDNS timed out, *or*
the peer was found but the dial failed.

**The path is never decided once.** LAN if the peer is here; otherwise
the relay carries the session while `transport/upgrade.go`
keeps re-dialling the peer directly, from both ends, every five seconds
for as long as it stays relayed. Both ends time from the same event —
the relayed connection — so their dials cross, which is what makes two
dials a hole punch. libp2p prefers the direct connection for every
stream opened after it lands, so nothing switches over; `PathTo` reports
the change because it reads the live connections. This is Tailscale's
shape, not DCUtR's: DCUtR still runs, tries three times at connection
time, and whichever of the two lands first ends both.

The ladder is walked in both directions, and `repair` is the one place
that decides which. Both notifee hooks call it on every connect and
disconnect, and it reads the *best* path to the peer rather than the
connection that fired: on `relay` it climbs, with the punch loop; on
`unknown` — every path gone — it descends, with `restore`, which tries
all known direct addresses in one dial and only then the configured
relay, for 30 seconds. Landing on the relay is not the end of it, since
that arrival starts the punch loop again.

Each retry races the whole ladder — LAN, then the Internet, then the
relay it is already running on — rather than walking it, because the LAN
dial finishes in a millisecond or two while a punch is still on its
first round trip, so the lowest rung that exists wins on its own. The
LAN rung needs `/ratatoskr/addrs/1.0.0` to exist at all: identify drops
every private address it is told over a public connection, and a circuit
through a relay on a VPS is a public connection, so two machines on one
LAN that meet over heimdall are never told each other's LAN address by
libp2p. `askAddrs` asks the peer directly each tick. Measured on 9 Sep
2026: a relayed pair on one LAN moved to the LAN in 10 seconds and ran
at 107.7 MB/s.

`lan` and `direct` are both direct connections and the difference is
where: `lan` is a private address on this network, `direct` is a public
one across the Internet. Never print the word "direct" to mean "not
relayed" — one of the three paths is called that.

**A stream never moves.** It is bound to the connection it was opened
on, and neither libp2p nor QUIC offers a migration that would change
that (QUIC's moves one connection between *local* addresses; the relay
and the peer are two remote endpoints). So a caller moving bulk data
checks the path, and when the answer beats the path its stream is on,
finishes that stream and sends the rest on a new one —
`Path.BetterThan` holds the order. `ratatoskr bench` waits on `Watch` and
writes in 512 KB chunks between looks. What the transport
cannot do is save the request that was in flight: reissuing it is
resume, and resume is the caller's, because only the caller knows what
an offset means.

## Writing style for this repo

Commit messages and the Markdown files are prose, not bullet dumps.
State what changed and why the alternative was rejected. The existing
git history is the reference.

**Code comments are the opposite.** They say what the code does and how
to use it, briefly, and they never cite a document: no `PLAN.md §2.1`,
no `SPEC.md §4`, no `TODO.md step 7`. A comment that points at a section
number rots the moment the section moves, and it sends a reader out of
the file to learn something the comment should have said. Keep the
reason a line of code exists when forgetting it would break the code
again — one sentence, in the comment itself.

Never surface `QUIC`, `DCUtR`, `AutoNAT`, `multiaddr` or `circuit` in
user-facing output outside a diagnostics view (`SPEC.md` §4).

## Other agent configs present

Codex (`~/.codex/`) and Gemini (`~/.gemini/`) configs exist on this
machine. To pull their MCP servers, commands, subagents or skills into
Claude Code, reply `/import` to see what is importable, then
`/import --yes=<digest>` to apply. If `/import` is unavailable here, run
`claude import` from a terminal.
