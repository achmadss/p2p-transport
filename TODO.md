# p2p-transport — TODO

Steps 0 to 3 built the transport and measured it. Steps 4 on turn it
into something another repository can depend on.

Rule: do not start a step until the one above passes its check.

---

## Step 0 — libp2p echo ✅ 6 Sep 2026

Host with QUIC and TCP, Noise, Ed25519. A string echoes over a stream,
with the peer id proven by the handshake. All five targets cross-build
with `CGO_ENABLED=0`.

## Step 1 — Identity and config ✅ 6 Sep 2026

`identity.key` at 0600 in a 0700 config directory, refused if other
users can read it. `ratatoskr id` is stable across restarts. The
permission check is a no-op on Windows, whose mode bits are synthetic.
`RATATOSKR_CONFIG_DIR` runs two agents on one machine.

## Step 2 — mDNS discovery and LAN dial ✅ 6 Sep 2026

Two machines find and connect with no server and no Internet. A
LAN-discovered peer is dialled with no relay in the dial set. macOS and
Windows only; Linux, the firewall prompts and the unplugged-router test
move to step 8.

## Step 3 — NAT spike ✅ 9 Sep 2026

The step that could have failed. Two machines on different carriers
connect, transfer, and verify their byte counts.

- LAN 65 MB/s, relayed 3.7 MB/s, and 107.7 MB/s after a relayed pair on
  one LAN moved to the LAN in 10 seconds
- A transfer already running moved itself off the relay at 12 MB of 3000
- Hotspot to home line punched directly, macOS↔Windows↔Linux passed —
  on the owner's runs, with the output not captured. Treat the punch
  rate on a renumbering carrier as non-zero and otherwise unknown
- Built for it: an address set re-measured every 27 seconds on the
  socket that punches, a punch retried every 5 seconds from both ends
  for as long as a peer stays relayed, and `/ratatoskr/addrs/1.0.0` to
  recover the LAN address identify discards over a public connection

**Verdict: pass.** libp2p is the right choice on the evidence.

Still open: heimdall's TCP 443 listener is built but not deployed — the
VPS needs the new binary, `CAP_NET_BIND_SERVICE`, and the 443 address
added to `HEIMDALL_ANNOUNCE`.

---

## Step 4 — The seam ✅ 9 Sep 2026

- [x] Promote `internal/transport` to `transport` at the module root
- [x] `PeerID`, `Path` and `Stream` as this package's own types
- [x] Protocol names are plain strings; addresses are opaque strings
- [x] `New(Config)` carrying the config directory, relays and `NoLAN`
- [x] Retire `Dial`, `DialPeer`, `DialRelayed`, `Describe` and `Host()`
- [x] `OnLAN` replaces reaching into `internal/discovery`
- [x] Strip `shares`, `trusted` and `aliases` from `config.json`
- [x] One `example_test.go` using only the exported surface

**Check: pass.** `transport/example_test.go` is `package transport_test`
and its import block is `bufio context fmt io os time` plus this module.
It connects, echoes and reads a path.

`Config` carries `Dir` rather than a key. A private key is a libp2p type
and naming one would have made every caller import libp2p to fill it in,
which is the one thing this step exists to prevent; `PLAN.md` §2.1 says
so now. The transport loads or generates `identity.key` in `Dir`.

Two things moved rather than being written: `RATATOSKR_DIAG` output is
`transport/diag.go`, because what it prints is below the seam and
reaching it from the harness would have meant exposing the host to do
it; and the protocol ids heimdall shares are `internal/wire`, because a
wire constant written twice is one that will one day differ.

## Step 5 — Path changes are pushed, not polled ✅ 9 Sep 2026

- [x] `Watch(id) (<-chan Path, func())`, off the hooks `repair` uses
- [x] `ratatoskr bench` uses it; the four-megabyte poll is deleted
- [x] A dead stream is reported clearly enough to tell "peer gone" from
      "request refused"

**Check:** a transfer moves within a second of a better path landing.

`transport/watch.go` hangs off the notifee `repair` already runs on, so
no goroutine and no timer were added: the events that change a path are
exactly the connect and disconnect the transport was watching anyway.
`changed` is the one place the two meet — it repairs the peer and
publishes the path repair read, so publishing costs no second look at the
connections.

The order inside `watch` is register, then read the path, and it is not
tidiness: reading first leaves a window in which a connection opens and
is published to a watcher not yet in the map, and the caller then holds a
stale path until the next change. The channel is seeded with `unknown`
and the real path goes through `publish` like any other.

The channel buffers one path and a stale one is dropped before a new one
is written. That is not a shortcut: the writer is libp2p's own connection
hook, which must never block on an application that has stopped reading,
and a reader that fell behind wants where the machine is now rather than
the sequence it took to get there. The current path is delivered before
`Watch` returns, so nothing is missed between asking and listening.

`bench` no longer holds a path poll. `moveCheck`'s four megabytes was a
fifth of a second on the local network and twenty seconds on the relay —
the slow path being the one that noticed late. `writeChunk` is 512 KB and
is now only how long a write can keep the transfer from reading an answer
that has already arrived: five milliseconds on the local network, a
seventh of a second on the slowest relay measured. `watchUpgrade` lost
its 250 ms ticker for a blocking receive, and `zeros` went with the
`io.CopyN` it fed.

`ErrUnreachable` and `ErrNotHandled` are the third box. Both failures
arrive as a failure to open a stream, and only the error tells a caller
whether to wait on `Watch` and retry or to stop. `Read` and `Write` tag a
stream that died mid-transfer the same way, leaving `io.EOF` alone, since
a completed transfer ends by reading one.

**Not measured yet.** The check wants a transfer moving within a second
of a better path landing, on two machines. What is proven here is that
the change is pushed and that the transfer acts on the push — the
timing on a real pair is a `bench` run for the owner to make.

## Step 6 — Survival

- [ ] Sleep and wake, on all three operating systems
- [ ] Wi-Fi to Ethernet and back, mid-transfer; cable pulled and replaced
- [ ] The relay restarted underneath a relayed pair
- [ ] A peer that is simply gone: give up and say so
- [ ] No goroutine leak after a thousand connect/disconnect cycles

**Check:** every one reconnects or fails loudly. Nothing hangs.

## Step 7 — The relay fleet

`FLEET.md` is the design. Bifrost places, meters and scales; heimdall
shapes.

- [ ] `bifrost`: relay registry, placement, leases, quota push, metering
- [ ] Vendored relay hop: one bucket per subject, no per-circuit limiter
- [ ] The allowance loop: demand reported per second, max-min allocated
      across the relays carrying a subject, pushed back
- [ ] Agents lease a relay instead of reading one from `config.json`,
      and re-lease without a deadline when a relay dies
- [ ] Autoscale within `min`/`max`; pack tight, shed the idle first,
      drain the emptiest relay before destroying it

**Check:** a relay is killed mid-transfer and the pair is back on
another one; two machines on one subject behind a fast and a slow link
measure 9 and 1 rather than 5 and 1, on the same relay or on two; a
limit changed mid-transfer takes effect mid-transfer.

## Step 8 — Windows and Linux

- [ ] Every check in steps 4 to 7, on Windows and Linux
- [ ] Firewall prompts documented per OS
- [ ] The unplugged-router test, owed since step 2
- [ ] The `identity.key` permission gap on Windows: ACLs, or an honest
      note that the check does not apply there

**Check:** the same three-machine run passes from every one as initiator.

## Step 9 — Freeze and tag

- [ ] `README.md`: what this is, the surface, one worked example
- [ ] `v1.0.0`; `PLAN.md` §2.1 does not change without a major version
- [ ] Re-measure the hotspot punch with `RATATOSKR_DIAG=1` and write the
      numbers down

**Check:** an application in another repository depends on the tag and
its author never reads `PLAN.md` §3.

---

## Continuously

- [x] `Makefile`: `build`, `test`, `vet`, `cross`, `CGO_ENABLED=0`
- [ ] `CGO_ENABLED=0` and `go vet` enforced in CI
- [ ] Pin libp2p versions; review before every bump
- [ ] Every performance claim is a number someone measured, with its
      date and the machines. A verdict is not a measurement

## Not this repository

Files, folders and metadata · a file protocol · WebDAV, SFTP, FUSE and
SMB · accounts, sign-in and billing · authorisation policy · resume
state and integrity hashes · any user interface. `SPEC.md` §11.
