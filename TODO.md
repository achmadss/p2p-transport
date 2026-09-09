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

## Step 4 — The seam

- [ ] Promote `internal/transport` to `transport` at the module root
- [ ] `PeerID`, `Path` and `Stream` as this package's own types
- [ ] Protocol names are plain strings; addresses are opaque strings
- [ ] `New(Config)` carrying key, relays and `NoLAN`
- [ ] Retire `Dial`, `DialPeer`, `DialRelayed` and `Host()`
- [ ] `OnLAN` replaces reaching into `internal/discovery`
- [ ] Strip `shares`, `trusted` and `aliases` from `config.json`
- [ ] One `example_test.go` using only the exported surface

**Check:** a package that imports `transport` and nothing else opens a
stream and reads a path, with no libp2p import the caller wrote.

## Step 5 — Path changes are pushed, not polled

- [ ] `Watch(id) (<-chan Path, func())`, off the hooks `repair` uses
- [ ] `ratatoskr bench` uses it; the four-megabyte poll is deleted
- [ ] A dead stream is reported clearly enough to tell "peer gone" from
      "request refused"

**Check:** a transfer moves within a second of a better path landing.

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
- [ ] Vendored relay hop with a per-subject shaper and per-subject bytes
- [ ] Agents lease a relay instead of reading one from `config.json`,
      and re-lease without a deadline when a relay dies
- [ ] Autoscale within `min`/`max`, drain before destroy

**Check:** a relay is killed mid-transfer and the pair is back on
another one; an account's share divides across its active machines and
follows a limit changed while it runs.

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
