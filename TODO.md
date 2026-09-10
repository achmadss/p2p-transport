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
- [x] A peer that is simply gone: give up and say so
- [x] No goroutine leak after a thousand connect/disconnect cycles

**Check:** every one reconnects or fails loudly. Nothing hangs.

The two boxes that are a piece of code are ticked; the three that are a
person moving a cable are not, and cannot be from here.

Giving up was already built. `restore` chases a peer whose every path has
gone for `restoreFor`, and returns whether it came back, so `repair`
either climbs the ladder again or lets the peer go. Saying so needed
nothing new either: the disconnection publishes `unknown` to everyone
watching, and the next `Open` returns `ErrUnreachable` rather than
hanging. `TestRepairGivesUpOnAPeerThatIsGone` holds the first half and
`error_test.go` the second. The leak check is
`TestManyCyclesLeakNoGoroutines` — a thousand real connect and disconnect
cycles, then a wait for the goroutine count to come back — and it runs in
under a second, so it stays in the ordinary test run.

What the three remaining boxes needed in code was a way to notice the
world had moved, and two ways were missing.

The relay was dialled once, at startup. `holdRelays` now re-runs on
`EvtLocalAddressesUpdated`, because a machine that moves from Wi-Fi to a
cable has a new address to be observed at and, if the move dropped the
relay connection, no reservation left to be reached through. It also
re-runs every minute, because a relay that reboots is a change to nothing
on this machine and so raises no event on it — a minute rather than the
five seconds a peer is chased at, since a restarting relay is back almost
immediately and one gone for good should not cost a dial every five
seconds forever. An outage prints one line and the recovery prints one
more, instead of one a minute for as long as it lasts.

The other was shutdown. Closing the host disconnects every peer, and each
of those disconnections arrived at the hook that starts a chase, so the
last act of a close was to start work for peers it had just dropped.
`Close` now stops the background work before the host rather than after,
`repair` refuses to start once the context is cancelled, and the dials
hang off a host context instead of `context.Background()`.

**Honest about that last one:** it is the right shape and it is not a
leak that was measured. A test that closed the host during a dial passed
against the old code too, because libp2p tears its transports down and
that aborts the dial on the way out. The test was deleted rather than
kept as a check that cannot fail.

**Not measured yet**, and each needs a person at a keyboard: sleep and
wake on all three operating systems, a cable pulled and replaced
mid-transfer, and heimdall restarted under a relayed pair. Run the third
first — it is the only one whose fix is new code rather than machinery
that was already there.

## Step 7 — The relay fleet

`FLEET.md` is the design. Bifrost places, meters and scales; heimdall
shapes.

- [x] `bifrost`: relay registry, placement, leases and admission push
- [x] Heimdall joins a fleet: registers, follows the pushed access list,
      and refuses everyone else
- [x] `bifrost`: metering — per-subject counters and the usage endpoint
- [x] The shaper: one bucket per subject, no per-circuit limiter, and a
      rate change that reaches a transfer already running
- [x] The allowance loop: demand reported per second, max-min allocated
      across the relays carrying a subject, pushed back
- [x] Agents lease a relay instead of reading one from `config.json`,
      and re-lease without a deadline when a relay dies
- [x] A heartbeat: a relay reports every period even when idle, and the
      coordinator drops one that has gone quiet
- [x] Draining by hand: `GET /v1/relays`, `POST` and `DELETE
      /v1/relays/{id}/drain`, and a machine that moves itself off at its
      next renewal
- [ ] Autoscale within `min`/`max`; pack tight, shed the idle first,
      and call a provider to create and destroy the VPS

**Check:** a relay is killed mid-transfer and the pair is back on
another one; two machines on one subject behind a fast and a slow link
measure 9 and 1 rather than 5 and 1, on the same relay or on two; a
limit changed mid-transfer takes effect mid-transfer.

`FLEET.md` §9 step 1 is built end to end: relays register, machines
lease, and the access list refuses everyone the coordinator did not
place. What it has not done is carry a byte — the three parts have only
ever met in tests, and the check at the top of this step needs three
machines and someone to kill a relay. The state that survives a restart
is the subjects file and nothing else: relays re-register and machines
re-lease, so a coordinator that restarts costs one round of each rather
than a recovery.

The relay set is now a set that can be replaced rather than a list built
at startup, and everything that reaches for a relay reads it there: the
last rung of the dial ladder, the reservation that makes this machine
reachable from outside, and the addresses handed to a peer. That is what
lets a machine be moved. The reachability machinery is asked for
candidates on every call instead of being handed a fixed list, and it is
told one candidate is enough — the default is to collect four before
reserving with any, and a machine given one relay would have waited out
a three-minute boot delay before becoming reachable at all.

The lease loop never gives up and never tears anything down. A
coordinator that has gone quiet leaves the machine on the relay it
already has, past the lease's end, because a working path is worth more
than an expired promise; what stops is moving to a better one. The one
thing that does not wait for the clock is the relay dying: the
disconnection wakes the loop, since until another relay is placed
nothing outside the local network can reach here.

Two decisions in it are worth the sentence. The subject store is a JSON
file rather than the SQLite `FLEET.md` §8 names, because at this step
the durable state is a handful of subjects and their machines: the
placements and the registry are rebuilt from what reconnects. Metering
has landed and it stayed a file: a counter is one integer and one
timestamp per subject, written beside the subjects rather than in a
database. SQLite when a whole-file rewrite costs more than it saves,
which is a size this fleet is nowhere near. And a relay that loses the
coordinator keeps its
access list exactly as it was, so an outage stops new machines arriving
and does not evict the ones already relaying.

The allowance loop is what makes a subject's rate one number when its
machines are on several relays. Each relay reports, once a period, what
each subject moved and whether it spent any of the period waiting on its
bucket; bifrost turns those into demands, divides the subject's rate
max-min fair between the relays carrying it, and pushes back the shares
that moved. Two machines behind a fast and a slow link land on nine and
one whether they share a relay or not, which is the property the whole
loop exists for: placement changes how long the answer takes and never
what it is.

Three parts of it came out smaller than `FLEET.md` §4.2 drew them, and
that section now records why. A relay's report names only the subjects
that moved something, because a share nobody has mentioned for three
periods falls back to a floor on its own and silence is cheaper than a
zero. The circuit count is not reported, because the division never
reads it. And only a share whose number actually changed is pushed, so a
fleet where nothing is happening puts nothing on the wire.

Leaving the *message* out on an idle period was the fourth, and it was
wrong. It made a relay carrying nothing look exactly like a relay whose
power had been cut, and the coordinator had no other way to tell: a
wedged process never closes its stream, so a dead VPS was counted as
capacity until libp2p gave up on the connection. The report now goes out
every period whether or not anything moved, and the coordinator drops
the stream of a relay it has not heard from for three periods — never
sooner than fifteen seconds, because dropping a relay evicts every
machine on it.

Measuring the wait is what made the demand signal possible, and it cost
one change in the byte path: `pay` reserves and sleeps rather than
calling `WaitN`, which is the same thing with the delay hidden. Here the
delay is the whole point — it is the only evidence a subject would have
taken more than it was given. That is the blocked-time signal the earlier
note said was missing, and it is now there.

Metering turned out to be an addition rather than a subsystem, and
`FLEET.md` §5 now records why. That section has the relays exporting
Prometheus metrics on a private port and bifrost scraping them, and none
of that was built: the relays already say what every subject moved, once
a period, because the allowance loop cannot divide a rate without it.
Adding those numbers up is the meter. A scrape would have been a second
port, a second protocol and a second copy of the same count, arriving at
an interval nobody chose — and the reason §5 wanted a rolling counter at
all was so `GET /v1/subjects/{id}/usage` could answer without a
Prometheus query, which the counter alone does.

The counter only grows and the time it started travels with it, because
an application that wants a month reads it twice and subtracts, and the
start time is the only way to tell a quiet month from a coordinator that
lost the file. It is held in memory and written to `usage.json` every
thirty seconds and once more on the way out, so a crash costs at most
thirty seconds of counting — the same bargain §5 already makes for a
relay that dies holding a period. Bytes are counted whether or not the
coordinator still has a share for that subject, because a placement that
expires mid-period does not make the last period of a transfer free.

The shaper did not need the fork `FLEET.md` §4.5 called for, and that
section now says why. Every byte a relay forwards arrives through one of
two calls on the host it was built with, and `relay.New` takes a
`host.Host` interface — so the hook is a host that embeds the real one
and overrides those two methods, twenty lines against a thousand-line
copy of somebody else's package. Reads are shaped and writes are not,
which is not an omission: a forwarded byte is read from one leg and
written to the other, so shaping reads charges it exactly once.

What the fork would have given, the blocked time §4.2 runs on, turned
out to be a line in `pay` rather than anything the fork was needed for:
it reserves and sleeps, and the sleep is the measurement.

Every number the arithmetic turns on is an environment variable with a
default: `BIFROST_HEADROOM` (0.70) and `BIFROST_PACK_TO` (0.85) decide
how many machines fit on a relay, `BIFROST_SUBJECT_MIN` (2M) and
`BIFROST_SUBJECT_MAX` (50M) are what a subject gets when the application
does not say, and `HEIMDALL_BANDWIDTH` (50M) is what a relay claims it
can forward. That last one is per direction and it is a guess until
somebody measures the machine — a relayed byte crosses it twice.

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
