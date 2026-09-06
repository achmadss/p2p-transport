# Ratatoskr — TODO

`SPEC.md` is the requirement. `PLAN.md` is the design. This file is the
what, in order. The spec's MVP (§28) is complete at step 11.

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
- [x] `config.json`: shared folders with modes, trust list, device aliases
- [x] `ratatoskr id`
- [x] Tests: id stable across restarts; a corrupt key file fails loudly

**Done 2026-09-06** on macOS and Windows; Linux still to run. The
permission check is skipped on Windows, whose Unix mode bits are
synthetic — ACLs are a separate piece of work, not done here.

`config.json`'s format and validation exist and are tested, but no
command edits it yet; `trust`/`untrust`/`trusted` land with the
authorisation work.

**Check:** `ratatoskr id` prints the same id twice, on all three machines.

---

## Step 2 — mDNS discovery and LAN dial

- [x] libp2p mDNS discovery service, advertising the peer id
- [x] `internal/discovery`: the mDNS implementation. No interface yet —
      there is one implementation, and mimir lookup in step 10 is what
      would justify putting something behind it
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
- [ ] Test: home Wi-Fi to phone hotspot; macOS↔Windows↔Linux; both peers
      behind the same NAT
- [x] Serve over the relay immediately, upgrade in the background
- [x] `libp2p.NATPortMap()`: ask the router to forward a port, which is
      how a torrent client stays off relays. Kept even though this house
      is behind carrier NAT and it cannot help here.

**Code is done, the measurement is not.** heimdall relays, the agent
takes a reservation, and `connect --via lan|relay|auto` works: verified
on loopback with three processes, where `auto` chose the LAN and
`--via relay` was reported as `relay` at both ends. Loopback proves the
plumbing and nothing about NAT, which is the whole point of the step,
so the remaining boxes stay open until heimdall runs on the VPS.

Two things loopback taught anyway. A relayed connection is *limited* in
libp2p and refuses streams unless the dial opts in, which is why the
first relayed attempt hung rather than failed. And AutoNAT correctly
declines to reserve when it believes it is reachable, so
`RATATOSKR_FORCE_PRIVATE=1` exists to force the relay path in testing
and must never be set in production.

**Measured, 6 Sep 2026.** heimdall on a VPS at 103.181.143.222, a
MacBook and a Windows box on the same home Wi-Fi. The relay sits behind
a cloud NAT and sees only `10.41.250.254`, so `HEIMDALL_ANNOUNCE` names
the public address; without it every agent is told to dial an address
that reaches nothing.

| Path | Throughput |
|------|-----------|
| LAN, mDNS-discovered, Wi-Fi | 65.2 MB/s (200 MB in 3.07 s) |
| Relayed through the VPS | 3.7 MB/s (50 MB in 13.7 s) |

`--via auto` chose the LAN, as designed.

**Then the Windows box moved to a phone hotspot**, which is the case the
step existed to test: home Wi-Fi on one side, a mobile carrier on the
other.

| Path | Throughput | Punch |
|------|-----------|-------|
| Relayed, home Wi-Fi to phone hotspot | 0.2 MB/s | 0 of 6 |

Six connections, three DCUtR attempts each, eighteen failures and no
successes. The debug log says exactly why, and it is worth writing down
because the number on its own would be read as a libp2p failure.

The hotspot offers one IPv4 candidate and four IPv6 ones. Every IPv6
dial dies with `no route to host` — the home network has no route to
the carrier's IPv6. That leaves a single IPv4 QUIC address, and the port
it advertises (`59936`) is not the port it listens on (`59937`). A NAT
that hands out a different external port per destination is symmetric,
and a symmetric NAT is the one shape DCUtR cannot open: the address the
relay observed is not the address the far peer will accept packets on.

**Correction, same day.** The first reading of that log said symmetric
NAT and stopped there. It was half the answer and the smaller half. The
home line's public address is `180.252.216.153` — the same address the
hotspot advertised, so both sides leave through one carrier NAT, and the
punch was never between two networks in the way that matters.

What settled it was a port-mapping test rather than more log reading.
`libp2p.NATPortMap()` now asks the router to forward a port, which is
the mechanism a torrent client uses and the one this project was
missing. The router accepts the request: a hand-written UPnP
`AddPortMapping` succeeds and reads back correctly. Then two facts
land on top of each other. `GetExternalIPAddress` returns an empty
string, and a UDP packet sent from the VPS to `180.252.216.153:41234`
never arrives. A router that cannot name its own external address and
whose forwards do nothing is not the edge of the network. The carrier's
NAT is, and it holds the only public address here.

There is no IPv6 to escape through either: no global address on the Mac,
none on the VPS, and `ndp -pn` reports **no advertising router** on the
home link, so the line is not offering IPv6 at all.

**The valid two-network test, finally.** Home on `180.252.216.153`, the
Windows box on a hotspot on `182.6.161.1` — two carriers, two addresses,
the test the earlier one only looked like. Three DCUtR attempts, no
punch, and this time the log is worth trusting:

- Every IPv6 candidate the phone offers dies with `no route to host`.
  The phone has a real public IPv6 and the house has no IPv6 to reach it
  with. This is the closest thing to a working path in the whole
  measurement, and one router setting away from existing.
- The single IPv4 candidate is `182.6.161.1/udp/16295`, while the same
  peer listens on `56954`. A NAT that assigns a fresh external port per
  destination is symmetric, and the address the relay observed is not an
  address anyone else may use. `timeout: no recent network activity`.

Both ends are therefore closed, for different reasons, and the two
reasons need different fixes. The house needs an inbound path it does
not have; the phone needs a NAT it does not control.

So the honest statement of the constraint is that **this house has no
inbound path at all**, by any protocol, and no amount of NAT traversal
invents one.

The symmetric-NAT reading still holds for the mobile side, and neither
finding is the design's fault. Every
system in this class meets it and every one answers the same way, with
a relay: Tailscale has DERP, Syncthing has relay pools. What it settles
is that **the relay is not a rare fallback and cannot be treated as
one** — on a phone hotspot it is the only path that exists. The 0.2 MB/s
is the mobile uplink, not heimdall; the same relay moved 3.7 MB/s
between two fixed lines minutes earlier.

Two numbers are still unmeasured, and both need hardware not present
here: home NAT to a *different* home NAT, which is the common case and
the one DCUtR is good at, and a wired 1 Gbps LAN, since 65.2 MB/s is a
Wi-Fi ceiling rather than a protocol one.

Windows cost two hours that were not code. Defender deleted the binary
on arrival, git-bash rewrote `/ip4/...` into `C:/Program Files/Git/ip4/...`
until `MSYS_NO_PATHCONV=1` stopped it, and the firewall dropped every
inbound dial silently until a rule named the executable. None of it is
Ratatoskr's fault and all of it is Ratatoskr's problem, because a user
on Windows meets the same three walls.

**Check:** two machines on different networks connect, and the numbers
exist on paper. If throughput or punch rate is bad, stop and reconsider
here rather than at step 9.

**Verdict: pass, with one requirement added.** Machines on different
networks connect, transfer, and verify their byte counts. The punch rate
against mobile CGNAT is zero, which is bad and expected, and it does not
change the choice of libp2p because no alternative punches a symmetric
NAT either. It does change what the later steps must assume: a transfer
may run at relay speed for its whole life, so resume, progress and
cancellation are load-bearing rather than polish, and step 11's metering
is what stops one such transfer from spending a month of VPS egress.

## Step 3.5 — Relay hygiene

Two fixes that fell out of the punch measurement, both small, both done
before anything builds on the transport.

- [ ] Enable AutoNAT v2 (`libp2p.EnableAutoNATv2()`) in
      `internal/transport`. v0.49 ships it opt-in and the host runs
      without it, so reachability rests on v1 probes and observed
      addresses from identify.
- [ ] Move heimdall's TCP listener to port 443 (`cmd/heimdall`). Some
      networks drop 4001 and pass 443; Tailscale's DERP relays sit on
      443 for the same reason.

---

## Step 4 — File API: read side

- [ ] `internal/protocol`: envelope, version, request and reply types
- [ ] `internal/transport`: the `Transport` interface
- [ ] `internal/transport/p2p`: streams over libp2p
- [ ] `internal/transport/loopback`: in-process, so the file layer tests
      with no network
- [ ] `internal/fileapi`: the verbs. It must not import libp2p
- [ ] `internal/fsroot`: clean, join, `EvalSymlinks`, verify inside root
- [ ] `internal/fsroot`: the write variant — resolve the parent, then
      check the final element has no separator
- [ ] `internal/fsroot` tests: `..`, absolute paths, symlink escape, null
      bytes, Windows reserved names, alternate data streams, `\\?\`
      prefixes, trailing dots and spaces
- [ ] `HELLO`, `PING`, `ROOTS`, `LIST` (paged), `STAT`, `DF`
- [ ] Entry metadata: name, root-relative `path`, kind, size, modified,
      optional `created` and `mode`, `symlink`
- [ ] `path` is always what the client asked through, never a resolved
      absolute path, which would leak the machine's layout
- [ ] Trust list enforced: an unknown peer id is refused
- [ ] `ERROR` codes; never leak a real path or a stack trace
- [ ] Enforce the 64 KB control message cap

**Check:** a real directory listing crosses the wire. Every hostile path
in the table is refused, over both transports.

---

## Step 5 — Control API and CLI

- [ ] `internal/control`: HTTP on `127.0.0.1`, random free port
- [ ] Random token; `control.json` at 0600; bearer check on every route
- [ ] `GET /v1/status`, `/v1/peers`, `/v1/discover`, `/v1/events`
- [ ] `GET|POST|DELETE /v1/folders`, `/v1/trusted`
- [ ] Device aliases, so `home:` resolves to a peer id
- [ ] `ratatoskr ls home:/Documents`
- [ ] Every subcommand becomes an HTTP client of the control API
- [ ] Clear message when no agent is running
- [ ] Verify the config dir on all three OSes

**Check:** `ratatoskr ls home:/Documents` prints a real listing.

---

## Step 6 — Download

- [ ] `READ` / `READ_OK` with size and BLAKE3 hash
- [ ] `READ` with offset and length — needed for resume, seek and sniffing
- [ ] `/ratatoskr/xfer/1.0.0`: header, then raw bytes to EOF
- [ ] Backpressure is `io.CopyBuffer` with a 64 KB buffer. No credit
      window — QUIC's stream flow control does the work
- [ ] `CANCEL`, and closing the stream, both clean up on each side
- [ ] Receiver verifies the whole-file hash before declaring success
- [ ] Cap concurrent transfers per peer at 4
- [ ] Handle a file that shrinks, grows or vanishes mid-transfer
- [ ] `THUMB`: a 256 px preview rendered on the agent, pure-Go decoder
- [ ] `HASH`: checksum a file without transferring it
- [ ] `ratatoskr get home:/big.iso ./big.iso` with progress
- [ ] Test: 10 GB, watching RSS on both sides
- [ ] Test: 4 simultaneous transfers stay stable

**Check:** 10 GB completes, hash matches, memory flat on both sides.

---

## Step 7 — Write operations

The first step that can destroy data. `PLAN.md` §11 is the spec.

- [ ] `ro` roots refuse every write before any path work happens
- [ ] A grant may narrow a root's mode, never widen it
- [ ] `internal/fsops`: atomic write — temp file in the destination dir,
      fsync file, fsync dir, rename over the target
- [ ] Clean up stale temp files on startup
- [ ] `WRITE` / `WRITE_OK`, including at an offset
- [ ] Free-space check and size cap before an upload starts
- [ ] `MKDIR` — no `-p` by default; refuse if the parent is missing
- [ ] `MOVE` — validate source and destination separately; both `rw`; no
      overwrite without the flag; cross-filesystem becomes copy, verify,
      delete
- [ ] `COPY` — server-side. Prove a 4 GB copy moves ~200 bytes
- [ ] `DELETE` — files and directories; a non-empty directory needs
      `recursive: true`; never follow a symlink out of a root; refuse to
      delete a share root
- [ ] `ratatoskr put | mkdir | mv | rm`
- [ ] Destructive tests: kill mid-upload, disk full, permission denied,
      target vanished, symlinked destination, traversal on every verb

**Check:** every write verb works, and no half-written file survives a
kill.

---

## Step 8 — WebDAV gateway

- [ ] `internal/webdav` on `golang.org/x/net/webdav`, backed by the File API
- [ ] Map PROPFIND, GET, PUT, MKCOL, MOVE, COPY, DELETE, HEAD, OPTIONS
- [ ] `ratatoskr webdav --addr 127.0.0.1:9832`, one path prefix per device
      (`http://127.0.0.1:9832/home-laptop/Documents/`)
- [ ] Bind loopback only. A public WebDAV URL would put that server on the
      data path
- [ ] Ranged GET, so media players and resume work
- [ ] Locking: null-lock only unless a client proves it needs more
- [ ] Test with Finder, Windows Explorer, rclone and Cyberduck
- [ ] Document the mount instructions for each

**Check:** Finder mounts it, browses, downloads and uploads.

---

## Step 9 — Resume and recovery

- [ ] `internal/transfer`: persisted records in `transfers/`
- [ ] Download resume: re-`STAT`, compare size, mtime and hash, then
      `READ` at the offset; discard rather than append to a stale partial
- [ ] Upload resume: `STAT` the remote temp, `HASH` the prefix, compare,
      then `WRITE` at the offset
- [ ] Automatic retry with backoff on a dropped connection, no prompt
- [ ] Surface a failure only after the retry budget is spent
- [ ] Clean up stale `.rtpart` files on startup
- [ ] `ratatoskr transfers [resume ID | cancel ID]`
- [ ] Test: unplug the cable at 40% of a 10 GB transfer, replug, verify
- [ ] Test: restart the process mid-transfer, verify
- [ ] Test: change the source file mid-transfer, confirm it restarts
      rather than corrupting

**Check:** a 10 GB transfer survives an unplugged cable and the resulting
file's hash is correct.

---

## Step 10 — mimir

- [ ] `cmd/mimir`: HTTP service, database, sessions
- [ ] Schema: accounts, devices, addresses, shares, grants
- [ ] Mimir signing keypair; publish its public key
- [ ] `internal/grant`: issue and verify, shared with the agent
- [ ] Pairing: agent shows a short-lived single-use code; `POST /v1/pair`
      redeems it; the agent pins mimir's public key
- [ ] `PUT /v1/self/addrs` — the agent publishes its multiaddrs on change,
      each tagged with its transport (quic|tcp|ws)
- [ ] `GET /v1/devices`, `/v1/devices/{id}`, `/v1/devices/{id}/addrs`
- [ ] `POST /v1/devices/{id}/grant`
- [ ] Presence from agent heartbeats and heimdall reservations;
      `unknown` when mimir cannot tell
- [ ] Agent verifies grants: signature, device, expiry, account, and that
      `client` equals the peer id libp2p already authenticated
- [ ] Agent caches the last grant so LAN use survives a mimir outage
- [ ] `network_hint` from comparing public IPs — a hint, never a fact
- [ ] `ratatoskr pair CODE`, `ratatoskr devices`
- [ ] Revocation: mimir stops issuing grants, drops addresses, heimdall
      refuses the reservation — and the UI states honestly that an offline
      agent takes effect within the grant lifetime
- [ ] Keep grant lifetime at one hour, so the revocation window is small
- [ ] Tests: expired grant, wrong device, forged signature, revoked device

**Check:** `ratatoskr devices` lists a paired machine, and `ls` works
against it from a different network.

---

## Step 11 — heimdall in production

- [ ] Relay reservations with sensible limits per account
- [ ] Rate limit and meter relayed bytes
- [ ] Report relay use to mimir, so the bill can be predicted
- [ ] TLS-terminated WebSocket transport for UDP-blocked networks
- [ ] Confirm heimdall cannot decrypt anything it forwards, and record
      what it unavoidably does learn: which peer ids talked, when, and how
      many bytes

**Check:** a 1 GB relayed transfer works, is counted, and is visibly
slower than direct in the status UI.

---

## Step 12 — Local web UI

- [ ] Static page served by ratatoskr on `127.0.0.1`
- [ ] Device list with presence and measured path
- [ ] `web/src/fs-adapter.ts` — our verbs only; the only file the
      file-manager library touches
- [ ] Mount `@cubone/react-file-manager` on the adapter
- [ ] Thumbnails from `THUMB`, never from a full download
- [ ] Transfer list with progress, cancel and resume
- [ ] Status wording: Online / Local network / Direct / Relayed / Offline.
      No QUIC, DCUtR, multiaddr or circuit anywhere in the UI

**Check:** browse, download and upload from a browser on `127.0.0.1`.

---

## Step 13 — Survival

- [ ] Sleep and wake the agent machine
- [ ] Switch Wi-Fi to hotspot mid-connection
- [ ] Move between LAN and Internet; discovery re-picks the right path
- [ ] Restart the agent; addresses republish; peers reconnect
- [ ] Restart heimdall; reservations are retaken
- [ ] mimir down: existing grants still work on the LAN; the UI says so
- [ ] Kill the client mid-transfer; the agent frees the file handle
- [ ] Every failure path ends in a working connection or an honest error.
      Never a hang.

**Check:** the whole list, on all three machines.

---

## Step 14 — Mobile app

- [ ] Embed the client library
- [ ] Identity in Keychain / Keystore
- [ ] mDNS: iOS local network permission and multicast entitlement;
      Android NSD
- [ ] File manager UI, built rather than adopted
- [ ] Background transfer behaviour on both platforms

**Check:** browse and transfer over the LAN with the phone offline apart
from Wi-Fi.

---

## Cross-cutting, do continuously

- [x] `Makefile`: `build`, `test`, `vet`, `cross`
- [x] `CGO_ENABLED=0` enforced in the Makefile
- [ ] `CGO_ENABLED=0` enforced in CI
- [ ] `go vet` and `staticcheck` clean
- [ ] Pin libp2p versions; review before every bump
- [ ] Structured logging; `--verbose` for dial, discovery and hole-punch detail
- [ ] Unit tests beside each package. `identity`, `fsroot`, `protocol`,
      `grant` and `transfer` must be thorough
- [ ] `README.md` once step 8 passes

## Deliberately not now

Zero-install browser access · SFTP, FUSE and SMB adapters · search across
devices · version history · sync · sharing between accounts · public
links · tray launcher · installers and autostart
