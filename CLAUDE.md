# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Ratatoskr is a private, peer-to-peer network drive for a user's own
machines. Files stay on their hardware; the bytes travel directly between
their devices. Three binaries, one Go module —
`github.com/achmadss/p2p-transport`, which is the repository name; the
product and its agent binary are both called Ratatoskr:

| Binary | Role |
|--------|------|
| `ratatoskr` | The agent **and** the client. `run` serves files; `connect`/`ls`/`get`/`put` consume them. One keypair, one peer id, both roles. |
| `heimdall` | A libp2p relay node. Forwards Noise-encrypted bytes it cannot read. |
| `mimir` | Accounts, device registry, presence, addresses, grants. The only database. |

## Read these first, in order

- **`SPEC.md`** — the requirement. Treat it as fixed. If a change would
  contradict it, say so rather than silently diverging.
- **`PLAN.md`** — the design that satisfies the spec. Numbered sections;
  cite them (`PLAN.md §11`) rather than restating them.
- **`TODO.md`** — the ordered work, step 0 to step 14, each with a
  concrete pass/fail check.

## Current state

**TODO step 0 is done.** `internal/transport` is a libp2p host with QUIC
and TCP, Noise security and an Ed25519 identity; `cmd/ratatoskr` has
`dev-listen` and `dev-dial` speaking `/ratatoskr/echo/1.0.0`. The Pion
prototype and its dependencies are gone.

Those two dev subcommands are scaffolding. They exist to prove a stream
carries bytes, and they are deleted once `run` and `connect` (`PLAN.md`
§16) replace them — do not build features on them.

**TODO step 1 is done.** `internal/config` owns the per-OS config
directory (0700) and `config.json`; `internal/identity` owns
`identity.key` (0600) and refuses to start when it is readable by other
users. `ratatoskr id` prints the short fingerprint, `--full` the peer id.

Two things that are true and easy to miss: the permission check is a
no-op on Windows, because its Unix mode bits are synthetic and its access
control lives in ACLs; and `config.json` has a format and validation but
no command that edits it yet.

Set `RATATOSKR_CONFIG_DIR` to run two agents on one machine. The tests
rely on it.

**TODO step 2 is mostly done.** `internal/discovery` advertises and finds
peers over mDNS (`_ratatoskr._udp`), re-advertising when this machine's
addresses change. `ratatoskr run`, `discover` and `connect` work with no
server and no Internet. Still open: the Windows and Linux runs, the
firewall prompts, and the unplugged-router test.

`run` answers the echo protocol only; the File API is step 4. `connect`
gained `--via lan|relay|auto` with step 3.

`transport.DialPeer` strips circuit addresses from the dial set. That is
load-bearing, not tidiness: a peer found on the LAN must be reached on
the LAN or not at all.

**TODO step 3 is open, and it is the step that matters.** `cmd/heimdall`
is a circuit relay v2 node on a real VPS. The agent enables AutoNAT,
DCUtR and the relay client when `config.json` names a relay, and
`connect --via lan|relay|auto` picks a path — `auto` gives the LAN a
400 ms head start, per `PLAN.md` §6. LAN and relay are measured: 65
MB/s over Wi-Fi, 3.7 MB/s relayed.

**What is not solved is a phone on a public network reaching a laptop
at home, and that is the ordinary case rather than an edge one.** The
carrier tested gives every new destination an unrelated port, so no
address a third party observes names the door a peer must dial. A
punch lands in 251 ms when the agent is seconds old and never once it
is minutes old; a measured address, a span of five and a span of
sixty-six have all failed. The requirement is no file bytes through the
VPS — no egress, not a little — so "fall back to the relay" is not an
answer here. `TODO.md` step 3 carries the numbers and the plan, which
is to read Tailscale's `net/portmapper`, `net/netcheck`,
`wgengine/magicsock` and `disco` and take what applies. It is cloned at
`/Users/achmad/Documents/Belajar/tailscale` — a sibling to read, not a
dependency; nothing here imports it and `CGO_ENABLED=0` and the
package layout in `PLAN.md` §17 still bind. Read `derp` to understand
the fallback they chose and do not adopt it.

Four traps this step exposed. libp2p marks a relayed connection
*limited* and refuses streams on it unless the dial passes
`network.WithAllowLimitedConn`. AutoNAT wants several independent peers
to agree before it rules a machine unreachable, and a private drive
never has that many, so configuring a relay now forces the reservation
and `RATATOSKR_ASSUME_PUBLIC` opts a genuinely reachable machine out. A
circuit address does not appear in `host.Addrs()` on loopback, so `run`
prints the peer id to copy rather than an address it cannot promise.
And one machine takes every terminal window down the moment the agent
opens a multicast socket — a content filter below the socket fails
every packet to 224.0.0.251 — so `RATATOSKR_NO_MDNS` exists to run the
agent without one.

A machine learns its public address two ways, and the difference is the
whole of step 3. Asking a relay over `/ratatoskr/observed/1.0.0` names
the port of a connection opened at startup, which on a carrier that
renumbers is stale within minutes. `internal/transport/selfaddr.go`
wraps libp2p's own QUIC socket through
`quicreuse.OverrideListenUDP`, asks a reflector on it at the moment
DCUtR needs an address, and claims the reply before quic-go sees it —
so the address offered is measured on the socket that punches, when it
punches. That is correct and still insufficient here, because the port
toward a peer is not the port toward a reflector.

`scripts/punch.py` is the same punch with no libp2p in it, and is the
control every DCUtR failure needs beside it. `scripts/punchpair.sh`
runs `punch-quic` on both machines against a rendezvous on the VPS, so
neither needs an operator waiting on the other.

Nothing else in `PLAN.md`'s package layout exists yet.

## Commands

```bash
make build          # -> dist/ratatoskr
make test           # go test ./...
make vet
make cross          # all five targets from one machine
make clean

go test ./internal/fsroot/ -run TestSymlinkEscape -v   # one test
go test ./... -race
```

## Constraints that are not negotiable

Each one exists for a reason recorded in `PLAN.md`. Breaking any of them
is a design change, not a refactor.

- **`CGO_ENABLED=0`.** Enforced in the Makefile. It is what lets one
  machine cross-build every target. This rules out every Go GUI toolkit,
  tray-icon library, `mattn/go-sqlite3` (use `modernc.org/sqlite`), and
  any image decoder wrapping libvips or ImageMagick. `PLAN.md` §17.
- **`internal/fileapi` must not import libp2p.** The File API is the
  stable surface that WebDAV, the CLI, the control API and any future
  SFTP/FUSE adapter sit on. `PLAN.md` §8.
- **No server on the data path, and since 7 Sep 2026 no relayed file
  bytes either.** Mimir and heimdall coordinate: presence, addresses,
  signalling, metadata in kilobytes. File data goes peer to peer or it
  does not go — a peer with no direct path is reported unreachable, not
  served through the relay. `SPEC.md` §24 and §6 carry the amendment.
  Any design that routes file content through a server you control is
  wrong here, and that now includes the relay as well as server-side
  file managers like Filestash or File Browser.
- **Every filesystem call goes through `internal/fsroot`.** Clean, join to
  the root, `EvalSymlinks`, then verify the **resolved** path is still
  inside the root. Writes are different: the target does not exist yet, so
  resolve the **parent** and check the final element has no separator.
  `PLAN.md` §12. Treat this package as security code.
- **The build order is the risk order.** Do not skip ahead. Step 3 is a
  NAT spike that measures hole-punch rate and throughput; those numbers
  are the justification for choosing libp2p, and steps 4-9 must not be
  built on an unmeasured assumption.

## Architecture, in the parts that span files

**Layers.** Frontend → adapter (WebDAV / control API / CLI) → File API →
Transport → Discovery. A frontend never learns which transport answered.
This is what makes `ratatoskr webdav` on `127.0.0.1:9832` serve Finder
over a libp2p link to a laptop in another country.

**Two libp2p streams, not one channel.** `/ratatoskr/ctrl/1.0.0` carries
JSON requests and replies. `/ratatoskr/xfer/1.0.0` is one stream per
transfer, raw bytes, no framing — QUIC already delivers an ordered byte
stream. Backpressure is `io.CopyBuffer`; there is no credit window,
because QUIC's stream flow control blocks the writer.

**Operation classes decide where cost lives** (`PLAN.md` §7). Account
queries never touch the machine. Metadata calls need a peer but are ~1 KB,
so relaying them is free. Only bulk transfer cares whether the path is
direct or relayed. Two consequences that are easy to get wrong: `COPY`
and `MOVE` are **server-side** (duplicating 4 GB costs ~200 bytes), and
`THUMB` returns a rendered preview so a gallery never downloads full
images.

**Identity vs authorisation.** libp2p's Noise handshake proves *who a peer
is* — no separate challenge-response is needed. It says nothing about
*what they may read*. That is the trust list (local, no server) or a
mimir-signed grant (`PLAN.md` §5.2).

**Discovery is LAN-first with a head start.** mDNS at t=0; mimir at
t=400ms only if the LAN stayed quiet. A LAN-discovered session dials the
LAN multiaddr with no relay in the dial set, so nothing leaves the
network. Falls back on either trigger: mDNS timed out, *or* the peer was
found but the dial failed.

**Presence and path are different questions.** Presence comes from mimir
before connecting (`online`/`offline`/`unknown`). The path (`lan`/
`direct`/`relay`) is measured from the real connection afterwards, and
must never be guessed.

## Writing style for this repo

Commit messages and docs are prose, not bullet dumps. State what changed
and why the alternative was rejected. The existing git history is the
reference.

Never surface `QUIC`, `DCUtR`, `AutoNAT`, `multiaddr` or `circuit` in
user-facing output outside a diagnostics view (`SPEC.md` §30.4).

## Other agent configs present

Codex (`~/.codex/`) and Gemini (`~/.gemini/`) configs exist on this
machine. To pull their MCP servers, commands, subagents or skills into
Claude Code, reply `/import` to see what is importable, then
`/import --yes=<digest>` to apply. If `/import` is unavailable here, run
`claude import` from a terminal.
