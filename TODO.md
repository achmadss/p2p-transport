# p2p-transport — TODO

`SPEC.md` is the requirement. `PLAN.md` is the design. This file is the
what, in order. Steps 0 to 3 built and proved the transport; steps 4
to 9 turn it into something another repository can depend on.

Rule: do not start a step until the one above passes its check.

---

## Step 0 — libp2p echo

Replaces the Pion echo. That code is retired: the transport decision
moved to libp2p, and `internal/transport` is rebuilt on it.

- [x] Add `github.com/libp2p/go-libp2p`
- [x] Host with QUIC and TCP, Noise security, Ed25519 identity
- [x] Register `/ratatoskr/echo/1.0.0`
- [x] `ratatoskr dev-listen` prints its multiaddrs and waits
- [x] `ratatoskr dev-dial <multiaddr>` connects and echoes a string
- [x] Report the connection's transport, remote peer id and address
- [x] Confirm `CGO_ENABLED=0` still cross-builds every target

**Done 2026-09-06.** Echoed over QUIC on loopback; the peer id in
the dialled multiaddr was verified by the Noise handshake. All five
targets cross-build with `CGO_ENABLED=0`.

**Check:** the string comes back, over QUIC, with the peer id verified by
the Noise handshake.

---

## Step 1 — Identity and config

- [x] `internal/config`: per-OS dir via `os.UserConfigDir()`, created 0700
- [x] `internal/identity`: Ed25519 key on first run, `identity.key` at 0600
- [x] Refuse to start if the key file is group or world readable
- [x] Derive and cache the libp2p peer id
- [x] Short display fingerprint for the UI; full id in diagnostics only
- [x] `config.json`: relays, and — at the time — shared folders, a
      trust list and device aliases. Those three are the application's
      and leave in step 4
- [x] `ratatoskr id`
- [x] Tests: id stable across restarts; a corrupt key file fails loudly

**Done 2026-09-06** on macOS and Windows; Linux still to run. The
permission check is skipped on Windows, whose Unix mode bits are
synthetic — ACLs are a separate piece of work, not done here.

`config.json`'s format and validation exist and are tested, but no
command edits it yet, and the fields describing shares and trust are
removed in step 4 rather than given one: authorisation is not decided
in this repository.

**Check:** `ratatoskr id` prints the same id twice, on all three machines.

---

## Step 2 — mDNS discovery and LAN dial

- [x] libp2p mDNS discovery service, advertising the peer id
- [x] `internal/discovery`: the mDNS implementation. No interface yet —
      there is one implementation, and the caller supplying its own
      addresses is what would justify putting something behind it
- [x] `ratatoskr discover` lists agents on this network
- [x] Dial a discovered peer by its LAN multiaddr, with no relay in the
      dial set, so nothing leaves the network
- [x] `ratatoskr run` and `ratatoskr connect ID`. There is no `--via`
      flag yet: LAN is the only path that exists, so a flag choosing
      between one option would be a lie. It arrives with step 3
- [x] Re-advertise when the network interface changes
- [x] macOS and Windows. Linux still to run
- [ ] Firewall prompts: macOS asks once on `run`, Windows asks once and
      allowing it is enough. Windows also logs `mdns failed to set
      multicast interface ... udp6 [::]:5353` on startup; IPv4 multicast
      still works and discovery succeeds, so it is noise, not a failure
- [ ] Test with the router's uplink physically unplugged

**Check:** two machines find and connect to each other with no server and
no Internet.

---

## Step 3 — NAT spike

The step that can genuinely fail, and the one that proves the libp2p
choice. Do it before anything depends on the answer.

- [x] `cmd/heimdall`: libp2p node with circuit relay v2 hop enabled
- [x] Deploy it to a VPS with a public address
- [x] Agent: enable AutoNAT, relay client, and DCUtR hole punching
- [x] Agent takes a relay reservation and prints its circuit multiaddr
- [x] Dial that circuit address from a different network
- [x] Log whether the connection stayed relayed or upgraded to direct,
      and how long the upgrade took
- [x] **Measure and write down**: hole punch success rate, time to punch,
      and MB/s on a 1 Gbps LAN and over the Internet
- [x] Test: home Wi-Fi to phone hotspot — the peers meet and move bytes,
      over the relay. That is a working transfer and it counts as one:
      `SPEC.md` §2 allows the relay to carry file data when no direct
      path can be opened, and the owner has said plainly that using it
      is not a failure.
- [x] Test: home Wi-Fi to phone hotspot, *direct*. Passed 9 Sep 2026,
      on the owner's runs. This is the finding the whole of `NAT.md`
      was written around failing, and the numbers behind it were not
      captured — so `NAT.md` records it as passed and unquantified, and
      the next person to touch the punch should re-measure rather than
      trust a rate that was never written down.
- [x] Read Tailscale and take what applies (`NAT.md`). Read 7 Sep 2026;
      the decision is written down and built 8 Sep 2026
- [x] Publish a *set* of independently measured addresses, refreshed
      every 27 seconds, rather than one address measured at startup
- [x] Retry the punch for as long as a peer is relayed, from both ends.
      Landed the LAN upgrade in 10s on 9 Sep 2026; still unmeasured on
      the hotspot, which is the only test that has ever been able to
      fail
- [x] Test: two machines on one LAN that meet over the relay upgrade to
      the LAN, not to the Internet. Passed 9 Sep 2026: relay at 0 MB,
      moved to `lan` at 12 MB of 3000, 90.1 MB/s averaged across the
      two and 107.7 MB/s on the clean second pass. `/ratatoskr/addrs/1.0.0` exists for
      this: identify drops every private address it is told over a
      public connection, and a circuit through heimdall is a public
      connection, so libp2p alone never tells either machine the
      other's LAN address. Run it with mDNS off — discovery and path
      are different questions, and mDNS answers the first one only.
      Built 9 Sep 2026, unmeasured.
- [x] Test: a transfer already running moves itself off the relay when
      the punch lands part way through it. Passed 9 Sep 2026 on the LAN
      pair, at 12 MB of 3000. Still owed on the hotspot, and heimdall's
      counters have not been read across a move. Nothing migrates a stream,
      in libp2p or in QUIC; checking `PathTo` every few MB and finishing
      the stream is the whole mechanism, and it is what ranged reads
      the caller gets for nothing. Built 9 Sep 2026, unmeasured.
- [x] Test: macOS↔Windows↔Linux; both peers behind the same NAT.
      Passed 9 Sep 2026, on the owner's runs.
- [x] Serve over the relay immediately, upgrade in the background
- [x] `libp2p.NATPortMap()`: ask the router to forward a port, which is
      how a torrent client stays off relays. Kept even though this house
      is behind carrier NAT and it cannot help here.
- [x] Enable AutoNAT v2 (`libp2p.EnableAutoNATv2()`) in
      `internal/transport`, beside v1 rather than instead of it
- [x] heimdall answers TCP 443 as well as `HEIMDALL_PORT`, for networks
      that pass only 443. Binding it needs root or
      CAP_NET_BIND_SERVICE; without either it does not come up and the
      other listeners still do. **Not yet deployed** — the running VPS
      needs the new binary, the capability, and the 443 address added
      to `HEIMDALL_ANNOUNCE`, which replaces the whole advertised set.

**The measurements are in `NAT.md`.** It carries the numbers, the three
readings that turned out to be wrong, the Tailscale reading that decided
the design, and the procedure for the one test still owed. In short: LAN
65 MB/s and relayed 3.7 MB/s between two fixed lines; a phone hotspot
reaches a home line over the relay at 0.2 MB/s and has been punched
directly exactly once, at 251 ms, on an agent seconds old. The carrier
hands each new destination its own port, so no address a third party
observes names the door a peer must dial — which is why the agent now
publishes a set of addresses re-measured every 27 seconds and re-dials a
relayed peer every five seconds for as long as it stays relayed. Each
retry races the whole ladder rather than walking it — LAN, Internet,
and the relay already underneath — because the LAN dial finishes before
a punch has had its first round trip, so the lowest rung that exists
wins without being sequenced.

**Check:** two machines on different networks connect, and the numbers
exist on paper. If throughput or punch rate is bad, stop and reconsider
here rather than after something is built on top of it.

**Verdict: pass.** Machines on different networks connect, transfer,
and verify their byte counts. The relay carries what cannot be punched,
which `SPEC.md` §2 permits, and it is no longer the normal path: a
relayed pair on one LAN reached the LAN in ten seconds and ran at
107.7 MB/s, a transfer already in flight moved itself across without
being restarted, and the hotspot-to-home-line punch that this step
existed to doubt now lands. libp2p is the right choice on the evidence.

Two things the later steps still inherit. The hotspot number was never
written down, so the punch rate on a renumbering carrier is known to be
non-zero and not known to be anything more precise — re-measure before
building on it. And a transfer can still spend its life on the relay
when the punch does not land. That makes two things load-bearing:
reporting the path honestly, so the caller above can decide what a
relayed gigabyte is worth, and step 7's metering, which is what stops
one such transfer from spending a month of VPS egress.

---
## Step 4 — The seam

Steps 0 to 3 proved the transport works. This one makes it usable from
outside without dragging libp2p along, and it is the step that decides
whether this repository is a library or a private detail of an
application that does not exist yet.

- [ ] Promote `internal/transport` to `transport`, at the module root
- [ ] `PeerID`, `Path` and `Stream` as this package's own types.
      `Stream` is `io.ReadWriteCloser` plus `CloseWrite`, `Peer` and
      `Path`; the libp2p stream satisfies it behind a thin wrapper
- [ ] Protocol names are plain strings chosen by the caller
- [ ] Addresses are opaque strings: out of `Addrs()`, into `Connect()`,
      never parsed above. This is what keeps `multiaddr` out of layer 7
- [ ] `New(Config)` replaces `New(key, relays)`; `Config` carries the
      key, the relays and `NoLAN`
- [ ] Retire `Dial`, `DialPeer`, `DialRelayed` and `Host()` from the
      exported surface. Three dial verbs that differ by dial set are
      one `Connect` plus policy, and `Host()` hands the caller the
      entire library the seam exists to hide
- [ ] `OnLAN` replaces reaching into `internal/discovery`
- [ ] Strip `Shares`, `Trusted` and `Aliases` from `config.json`. They
      are the application's, nothing in this repository reads them, and
      leaving them is an invitation to implement authorisation here
- [ ] One `example_test.go` that uses only the exported surface
- [ ] Move the protocol identifiers this package registers for its own
      business out of the exported names

**Check:** a package that imports `transport` and nothing else compiles,
opens a stream, and reads a path — and `go list -deps` on that package
shows no libp2p import that the caller wrote. Grep the exported
signatures for the words in `SPEC.md` §4 and find none.

---

## Step 5 — Path changes are pushed, not polled

- [ ] `Watch(id) (<-chan Path, func())`, fired from the same connection
      hooks `repair` already uses
- [ ] A closed watch releases its goroutine and its channel
- [ ] `ratatoskr bench` uses it instead of checking `PathTo` every four
      megabytes, and the four-megabyte check is deleted
- [ ] Document the recipe in one place: finish the stream in flight,
      open the next one on the better path. Nothing migrates a stream
- [ ] Report a dead stream clearly enough that a caller can tell "the
      peer went away" from "the request was refused"

**Check:** a transfer moves to a better path within a second of that
path landing, without polling, and the same run with `Watch` never
called behaves exactly as before.

---

## Step 6 — Survival

The ladder is built (`PLAN.md` §6). This is where it is proved against a
real machine rather than a test.

- [ ] Sleep and wake, on all three operating systems
- [ ] Wi-Fi to Ethernet, and back, mid-transfer
- [ ] Cable pulled and replaced
- [ ] The relay restarted underneath a relayed pair
- [ ] A peer that is simply gone: the descent gives up after 30 seconds
      and says so, rather than retrying forever in silence
- [ ] No goroutine leak after a thousand connect/disconnect cycles
- [ ] No path is ever reported that the live connections do not support

**Check:** every one of the above reconnects or fails loudly. Nothing
hangs, and `Close()` returns.

---

## Step 7 — heimdall in production

- [ ] Deploy the 443 listener: new binary, `CAP_NET_BIND_SERVICE`, and
      the 443 address added to `HEIMDALL_ANNOUNCE`, which replaces the
      whole advertised set rather than adding to it
- [ ] Reservation limits that survive a machine reconnecting in a loop
- [ ] Per-peer data and duration limits, and what happens at the ceiling
- [ ] Read the counters across a path change: a transfer that moves off
      the relay part way through should stop costing egress
- [ ] Restart without stranding reserved peers

**Check:** a 1 GB relayed transfer completes, is counted, and the
counter stops rising the moment the pair moves off the relay.

---

## Step 8 — Windows and Linux

Everything above has been proved on macOS and, in places, on Windows.

- [ ] Every check in steps 4 to 7, on Windows and Linux
- [ ] Firewall prompts documented for each: which port, which prompt,
      and whether allowing it once is enough
- [ ] The unplugged-router test, still owed from step 2
- [ ] `RATATOSKR_NO_MDNS=1` on a machine where multicast is refused
- [ ] The `identity.key` permission gap on Windows: ACLs, or an honest
      note that the check does not apply there

**Check:** the same three-machine run passes from every one of the three
as the initiator.

---

## Step 9 — Freeze and tag

- [ ] `README.md`: what this is, the surface, one worked example
- [ ] `v1.0.0`, and the surface in `PLAN.md` §2.1 does not change after
      it without a major version
- [ ] `NAT.md`'s outstanding measurement taken: the hotspot punch, with
      `RATATOSKR_DIAG=1`, numbers written down rather than a verdict

**Check:** an application in another repository depends on the tag,
connects two machines, and its author never reads `PLAN.md` §3.

---

## Cross-cutting, do continuously

- [x] `Makefile`: `build`, `test`, `vet`, `cross`
- [x] `CGO_ENABLED=0` enforced in the Makefile
- [ ] `CGO_ENABLED=0` enforced in CI
- [ ] `go vet` and `staticcheck` clean
- [ ] Pin libp2p versions; review before every bump
- [ ] Unit tests beside each package
- [ ] Every measured number goes in `NAT.md` with its date and the
      machines it was taken on. A verdict is not a measurement

## Not this repository

Files, folders and metadata · a file protocol · WebDAV, SFTP, FUSE and
SMB · accounts, device registries and presence · authorisation policy ·
resume state and integrity hashes · any user interface.

`SPEC.md` §11 is the same list. They are the application's, and the
whole point of steps 4 and 9 is that the application can be written
against a frozen surface without any of it leaking down here.
