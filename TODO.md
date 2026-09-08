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
- [x] Test: home Wi-Fi to phone hotspot — the peers meet and move bytes,
      over the relay. That is a working transfer and it counts as one:
      `SPEC.md` §1 allows the relay to carry file data when no direct
      path can be opened, and the owner has said plainly that using it
      is not a failure.
- [ ] Test: home Wi-Fi to phone hotspot, *direct*. A punch lands in
      251 ms only when the agent is seconds old; minutes later the
      carrier's port is unrelated to any address a third party can
      observe, and nothing offered — a measured address, a span of five,
      a span of sixty-six — has landed since. This is the common case
      rather than the hard one, so leaving it relayed for ever is what
      §6 forbids; the transfer still works meanwhile.
- [x] Read Tailscale and take what applies (see below). Read 7 Sep 2026;
      the decision is written down and built 8 Sep 2026
- [x] Publish a *set* of independently measured addresses, refreshed
      every 27 seconds, rather than one address measured at startup
- [ ] Retry the punch for as long as a peer is relayed, from both ends.
      Built; unmeasured on the hotspot, which is the only test that
      has ever been able to fail
- [ ] Test: macOS↔Windows↔Linux; both peers behind the same NAT
- [x] Serve over the relay immediately, upgrade in the background
- [x] `libp2p.NATPortMap()`: ask the router to forward a port, which is
      how a torrent client stays off relays. Kept even though this house
      is behind carrier NAT and it cannot help here.

**Two things loopback taught before the VPS existed.** A relayed
connection is *limited* in libp2p and refuses streams unless the dial
opts in, which is why the
first relayed attempt hung rather than failed. And AutoNAT declines to
reserve until it believes it is unreachable, which on loopback needed
forcing; the same gate turned out to block every real machine too, and
is now settled by the config rather than by an environment variable.

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
  peer listens on `56954`. That was read as symmetric NAT. It is not
  evidence of one — an ordinary cone NAT rewrites the port too — and the
  measurement below shows the reading was wrong. `timeout: no recent
  network activity`.

Both ends are therefore closed, for different reasons, and the two
reasons need different fixes. The house needs an inbound path it does
not have; the phone needs a NAT it does not control.

So the honest statement of the constraint is that **this house has no
inbound path at all**, by any protocol, and no amount of NAT traversal
invents one.

**The symmetric-NAT reading was wrong, and this is the third correction
to the same paragraph.** `ratatoskr natcheck` asks three STUN servers
from one socket and compares what each one sees, which is the test RFC
4787 actually specifies. Both networks answer the same way:

| network | local port | seen by all three reflectors |
|---------|-----------|------------------------------|
| home, IndiHome  | 51059 | `180.252.216.153:51059` |
| phone, hotspot  | 60474 | `182.6.161.1:14650` |

One external port per socket, the same one for every destination. That
reads as **Endpoint-Independent Mapping** on both sides, which is the
shape DCUtR is built for. *This verdict does not survive the section
below.* Every observer asked inside one second agrees, and the question
that separates this carrier from a cone NAT can only be asked of a
destination it has never spoken to. Read on to "the mapping is not
stale, it is per-destination" before believing the table.

The reason is in our own log. Before each attempt the Mac announces:

    msg="Host now has a public address"
    addresses="[/ip4/127.0.0.1/... /ip4/192.168.2.141/... /ip6/::1/...]"

There is no public address in that list. `180.252.216.153` never enters
the set libp2p advertises, so the DCUtR CONNECT the Mac sends carries
only loopback and LAN candidates. The phone dials `192.168.2.141`,
reaches nothing, and therefore never sends a packet toward the house.
A hole punch is a *simultaneous* open: each side's outbound packet is
what opens its own NAT for the other. One side punching is not a punch,
and the Mac's dial to `182.6.161.1:14654` then times out against a
mapping that was never opened for it.

So the failure is ours, in address discovery, not the carriers'. libp2p
learns its public address from identify observations and needs several
agreeing ones from distinct peers; with a single relay as the only
direct peer it never reaches that bar, and the host punches from an
address it has not told anyone about.

**The first fix for that was also wrong, and the table above says why.**
Asking a STUN reflector on a throwaway socket learns the external port
*of that socket*. The home NAT preserves the port, so the guess happened
to be right and `tcpdump` on the VPS confirmed it; the hotspot renumbers
`60474` to `14650`, so on Windows the guess was refused and no address
was advertised at all. Endpoint-independent means one external port for
every *destination*, not one for every *socket*, and nothing about the
punching socket can be learned from another one.

`scripts/punch.py` settles the carriers. One socket asks STUN, then
punches at the address the other operator carries over by hand, and both
sides press enter together. Packets arrive in both directions at 15,
1200 and 1280 bytes. These two carriers punch; the sizes are not a
factor and never were.

**And the punch was dialling the wrong address family.** On the hotspot
the phone hands Windows real public IPv6; this fixed line has none, not
even a route. So the Mac's punch set was one IPv4 address and four IPv6
ones, and its own log says what happened to them:

    * [/ip6/2404:c0:5d10:355b:.../udp/54032/quic-v1] sendmsg: no route to host
    * [/ip6/2404:c0:5d10:355b:.../tcp/62469] connect: no route to host
    * [/ip4/182.6.161.1/udp/41654/quic-v1] hole punching attempted; no active dial

An address this machine cannot route is an address it never punches
towards, and a punch one side makes alone is not a punch. A
`holepunch.WithAddrFilter` now drops IPv6 from the remote set when no
interface holds a globally routable IPv6 address, and keeps it when one
does — two peers that both have IPv6 should meet over it and skip the
NAT entirely.

So heimdall answers `/ratatoskr/observed/1.0.0` with the remote address
of the connection the question arrived on. That is the QUIC socket's own
external address, measured rather than guessed, and it is exactly what
the relay had to know already. One observer is enough only because
`natcheck` establishes the mapping class separately: endpoint-independent
means the address heimdall sees is the address any peer may use.

Neither
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

**Verdict: pass on connectivity, open on the direct path.** Machines on
different networks connect, transfer, and verify their byte counts, and
the relay carrying them is a permitted outcome rather than a failed one.
The punch rate is zero, and the sections below trace why through three
wrong readings to a carrier that renumbers per destination. It changes
what the later steps must assume: a transfer may run at relay speed for
its whole life, so resume, progress and cancellation are load-bearing
rather than polish, and step 11's metering is what stops one such
transfer from spending a month of VPS egress.

## Step 3.5 — Relay hygiene

The side-by-side run of 2026-09-06 20:45 settles what a dozen earlier
runs could not, because for the first time both tools were on the wire at
the same second, under one capture, between the same two machines on
different networks. Counted by port, from the Mac:

    leaving   996  54879 -> 41611     punchtest
              118  63006 -> 14988     libp2p hole punch
              116  63006 -> 14964     libp2p hole punch, a stale address
    arriving  968  54879              punchtest
                0  63006

Every explanation that blames an address, a socket, a size or a carrier
dies here. The punch left the right socket for the right port. In the
seventeen seconds libp2p spent punching, 112 packets crossed between the
same two public addresses on the other socket, so the path was open while
the punch failed. Windows dialled `180.252.216.153/udp/63006`, which is
this machine's true address, and nothing of it arrived.

`punch-quic` then removed the last difference. On those same two
networks, minutes later: 74 and 75 bare packets crossed, a QUIC handshake
completed over the punched socket, and a stream carried bytes both ways
in each direction. The path passes punched QUIC.

So nothing outside this repository is at fault. The carriers punch, they
pass QUIC, both NATs are endpoint-independent — proved twice tonight by a
STUN-learned port that a third party then reached — and quic-go crosses
them unaided. What fails is how we drive libp2p.

The measurement that narrows it further is already in hand and was
misread once: in the failing run the Mac received **zero** packets from
the peer on any port, while its own punch left the right socket for the
advertised port. A home NAT that has sent to 182.6.165.5:14988 accepts
the reply from 182.6.165.5:14988. Receiving nothing means the peer's
packets arrived from some other port — that is, the port Windows
advertises is not the port Windows punches from.

That lead was wrong twice over, and both refutations are worth keeping.
Windows holds one QUIC socket, not several, so quicreuse hands the punch
the socket whose address was published. And a tap installed through
`quicreuse.OverrideListenUDP` — the socket's own account, from inside the
process, on the machine with neither root nor a capture — showed both
ends doing the same thing at the same moment: every packet sent to the
address the peer advertised, and nothing received. Two correct addresses,
two endpoint-independent mappings, four hundred packets, no arrivals.

The difference is what opens the flow. `punch-quic` with fifteen seconds
of bare datagrams in front of the handshake connects and carries a stream
both ways. The same binary, same machines, same networks, minutes later,
with `RATATOSKR_PUNCH_RAW=0` so that a QUIC Initial is the first packet
the socket ever sends to that peer: nothing, in either direction. That is
the agent's condition exactly, and it is the only condition in which this
path fails.

So the next number is how much bare traffic the path needs — `scripts/
punchpair.sh <room> <seconds>` walks it down without an operator on each
end. Then the fix goes where DCUtR can use it, because libp2p already
sends sixty-four bytes of junk and five seconds of it is not enough.

That walk-down has not started, because the carrier moved underneath it.
The phone hotspot that punched seventy-four packets sat behind
`182.6.165.5`; it now sits behind `182.6.166.95`, and that box does not
behave the same way. On one socket, in one second, the rendezvous saw
`182.6.166.95:9238` and a reflector saw `182.6.166.95:9239` — the next
port, for the next destination. Address-dependent mapping, RFC 4787
REQ-1 violated, and the end of hole punching on that network: whatever
port a third party publishes belongs to that third party, and the peer
is always a fourth. Both sides sent seventy-five packets and neither
received one, which is the only outcome available when the far side is
told a port the socket is not behind, and the near side's own NAT then
drops the reply arriving from the port it really used.

Three tools said this network was fine before the punch did not. Each
was wrong in a way worth keeping: `natcheck` asked three reflectors, and
three large providers reached the same way out of a carrier agree with
each other whatever the carrier does to a fourth destination — it now
asks the rendezvous on the same socket, which is the observer whose
answer the punch actually publishes. `punch.py observers` printed "all
observers agree" while one of them had not answered at all. And the bare
punch reported "0 packets arrived" without saying whether any had left,
so a socket refusing to write and a path swallowing everything read
identically.

None of this touches the QUIC-first finding above. That was measured on
a carrier that punched, and it stands. What it needs is that carrier
back — a network where the bare punch crosses — before the seconds can
be walked down.

**The offset is +1, and it is aimable.** Three readings, hours apart:
the rendezvous saw 9238, 9308, 9314 and a reflector saw 9239, 9309,
9315 on the same socket each time. So the published port is not the
port the carrier uses toward anyone else, but it is one below it. A
bare punch spread over nine ports above the published one crossed
immediately — 77 packets in, 74 out, and QUIC handshaked and carried a
stream both ways. The packets came back from `9315`, exactly the port
the reflector had named, and `RATATOSKR_PUNCH_SPREAD` prints that
offset rather than assuming it.

What advances the port is time, not the destination. `natcheck` asks
four observers inside a second and all four agree; the punch asks the
rendezvous, waits for a peer, then asks a reflector three seconds
later and gets the next port. Once advanced it holds: the whole
fifteen-second punch and the QUIC connection after it stayed on 9315.
An address from this carrier is therefore correct when it is measured
and stale a few seconds later, which is the one property no signalling
protocol can carry.

That is worth holding against `/ratatoskr/observed/1.0.0`. The agent
learns its public address by asking a relay and advertises it through
an `AddrsFactory`, and the peer dials it some seconds later — the
exact shape that fails here. It would explain what nothing else has:
why a punch aimed by STUN crossed on the same pair, at the same
minute, that DCUtR could not open. It is a hypothesis with a mechanism
behind it now rather than a guess, and the way to test it is to
measure what port an agent's socket really uses toward a peer versus
what its relay observed.

**Confirmed 7 Sep 2026, and step 3's hole punch now passes.** Two Macs,
one on home Wi-Fi and one on the phone hotspot, dialled twice in the
same minute with one variable between the runs.

| Serve side advertises | Result |
|---|---|
| `/ip4/182.6.166.95/udp/63189/quic-v1` | still relayed after 1m0s, three attempts, three timeouts |
| the same address and the two ports above it | **upgraded to direct after 251 ms** |

So DCUtR was never failing. It was dialling a port the carrier had
already moved off, and it had exactly one candidate to be wrong about.
Given three, it punched on the first attempt. `RATATOSKR_ADDR_SPREAD`
was the knob for it; the paragraphs below are why it no longer exists.

The `+1` held again here: the relay observed 63189 in one run and
35629 in the next, and the ports advertised around it are what carried
the connection.

**And the offset grows with uptime, which kills the workaround.** The
same pair, the same spread of three, dialled against an agent that had
been serving for three minutes rather than seconds: still relayed
after a minute, every attempt timing out on all three of
`35653/35654/35655`. So the `+1` was not a property of the carrier. It
was the age of the relay connection at the moment it was measured.

The mechanism follows from what was already recorded. This NAT keeps
an existing mapping on its original port and hands new destinations
the current value of a counter that walks forward over time. The
agent's door to the relay is opened once at startup and never moves;
the counter does. So the gap between what the relay observes and what
a fresh peer will reach is the drift accumulated since the agent
started, and it has no upper bound. A span of three covers a
seconds-old agent and nothing else, which is the worst shape a fix can
have: it passes the test and fails the use.

That settles the design rather than leaving it open.
`RATATOSKR_ADDR_SPREAD` cannot become a measured constant, because
there is no constant to measure. The address to publish has to be
taken on libp2p's own socket, toward a destination it has not spoken
to, at the moment of the punch — not read off a connection opened at
startup. `internal/transport/selfaddr.go` holds the only hook
that reaches that socket (`quicreuse.OverrideListenUDP`), which is why
it survived the clear-out. What remained was to send a reflector query
through it, intercept the reply before quic-go sees it, and hand the
answer to the DCUtR address filter that `EnableHolePunching` already
takes.

**Built, and it settles the question by failing.** The punch now offers
two addresses — the relay's view and one measured on libp2p's own
socket at the moment DCUtR asks — and an agent four minutes old still
stays relayed. The numbers say why. The relay had observed
`182.6.166.95:35749`; the reflector, asked seconds before the punch on
the socket that would do the punching, answered `61482`. The round
before gave `63369` against `35722`. Those gaps are not a counter
drifting. They are unrelated ports, tens of thousands apart, in both
directions.

So the mapping is not stale, it is per-destination. A reflector can
only name the door this socket uses toward *that reflector*, and the
peer is always a different destination behind a different door. This
carrier is symmetric in the RFC 4787 sense, and nothing a third party
observes can name the address a peer must dial. The 251 ms success
above is real and not a contradiction: a freshly started agent had its
relay door and its peer door two apart, which a span of three covered.
Minutes later they are nowhere near each other, which is exactly why
that spread passed its test and would have failed in use.

That read as the end of the road, and it was the end of *this* road: no
address a third party observes can name the door a peer must dial, so
publishing one better address cannot work here. What follows from it is
the relay, which is what `PLAN.md` put heimdall there for and what it
already carries as ciphertext — and, from 8 Sep, a set of addresses and
a punch that keeps being retried rather than a single aim taken once.

`natcheck` called this network endpoint-independent, which was narrow
rather than wrong: four observers asked inside one second do agree, and
the disagreement needs a longer window or a destination never spoken
to. It now asks over six rounds and three minutes, spending a reflector
per round; see the netcheck section below.

The measurement stays. It removed a toggle nobody could have set, costs
one reflector round trip per punch, and on any network whose published
address is merely old rather than per-destination it is the address
that works. It simply cannot rescue this one.

**And the doors are not ordered, which closes it.** The last idea worth
testing was that the ports are handed out in sequence, so the peer's is
simply a few past the one a reflector names — the reading that made an
earlier bare punch cross where a spread anchored to the relay's stale
view never did. A punch offering the measured port and the sixty-four
above it, sixty-six addresses from `36011` to `62066`, did not land
either. Four had already missed. So they are not sequential inside any
span worth advertising, and a span is not a fix: it would cost every
peer sixty-five dials that cannot arrive.

`nextDoors` is therefore zero, measured rather than chosen. What
remains is one freshly measured address instead of one stale one, which
is right wherever the published address is merely old and does not help
here.

What is left for this network is not code. Two peers with real IPv6
have no mail room between them at all, and the hotspot already has a
`2404:c0::/32` address while the home line has none — that is an ISP
question, not a libp2p one. Failing that, the birthday approach —
hundreds of sockets on each side so that some pair collides — is the
only remaining trick, and it is a coin flip costing thousands of
packets that libp2p cannot be asked to perform: DCUtR punches with one
socket, and a connection punched outside it cannot be handed back.

**Step 3 stays open for the direct path, and the relay is not what is
open about it.** A phone on a public network reaching a laptop at home
is the ordinary way this product will be used, not an edge case, and it
works today: the relay carries it, `SPEC.md` §1 permits exactly that,
and a transfer that goes over heimdall is a transfer that happened. What
§6 forbids is the relay becoming the *normal* path, and on this
carrier it currently is. So the measurements above are a problem
statement about the punch rather than about the product being unusable,
and the work is to make direct the common outcome rather than to refuse
the fallback while it is not.

### What to read next: Tailscale

Tailscale solves this case in production, on the same carriers, and
its source is open. It is cloned at
`/Users/achmad/Documents/Belajar/tailscale`, at `v1.103.0-pre`
(`5201273ae`) — a sibling of this repo, not a dependency of it, and
nothing here imports it.

Read to answer specific questions, not for inspiration. Each one is
something measured above that we could not get past.

- **`net/portmapper`** — UPnP, NAT-PMP and PCP, all three, with the
  quirks of real routers. `libp2p.NATPortMap()` is enabled here and
  achieved nothing: the router accepted an `AddPortMapping` and could
  not name its own external address, because the carrier NAT is above
  it. Does Tailscale detect that case, and does it get anything the
  hand-written attempt did not?
- **`net/netcheck`** — their NAT and latency probe. `natcheck` here
  calls this carrier endpoint-independent because four reflectors
  asked inside one second agree. Theirs runs longer and asks
  differently. What does it ask that ours does not, and would it have
  called this network what it is?
- **`wgengine/magicsock`** and **`disco`** — one UDP socket, many
  candidate paths, continuous re-probing, and an upgrade from relayed
  to direct that happens later and by itself. That last part is the
  behaviour to keep: `connect` already serves over the relay and
  upgrades in the background, and whatever replaces DCUtR here has to
  keep doing that rather than deciding once at dial time.
- **The hard-NAT path.** Their documentation describes reaching a
  symmetric NAT by sending to many ports at once, betting on a
  collision. The arithmetic is a birthday problem: the far side opens
  some number of mappings, we write to some number of ports, and the
  chance of a match is the product against 65535. Find what numbers
  they actually use, how long they spend, and what they do when it
  fails.
- **What we cannot take.** DERP is their relay and carrying data
  through it is exactly what this project refuses. Read it to
  understand the fallback they chose, and do not adopt it.

The open design question underneath all of it: libp2p's DCUtR punches
with one socket and cannot be handed a connection punched elsewhere.
Either the punching moves below libp2p — a `quicreuse` socket that has
already opened the path before QUIC uses it, which
`internal/transport/selfaddr.go` shows is reachable — or this project
learns to dial a peer without DCUtR's help. Decide that after reading,
not before.

One thing already known and worth acting on separately: two peers with
real IPv6 have no NAT between them at all. The hotspot has a
`2404:c0::/32` address and the home line has none, which is a question
for an ISP rather than for this repo, and it would remove the problem
on that pair entirely.

### Read: `net/netcheck`, 7 Sep 2026

**Their classifier asks our question and would have been wrong the same
way.** `MappingVariesByDestIP` is set in `addNodeLatency`
(`net/netcheck/netcheck.go:723`): keep the first `AddrPort` an IPv4 STUN
reply carries, and set the flag if a later reply on the same socket
carries a different one. One socket, several destinations, compare —
which is `natcheck` exactly. Their window is `ReportTimeout = 5s`
against our one second, and on this carrier every observer inside either
window agrees. So reimplementing their question does not produce a
verdict matching what was measured; it reproduces our wrong one. That is
the answer to the item, and it is not the answer it expected.

What makes their system work anyway is that **nothing downstream asks
the classifier whether to punch.** `MappingVariesByDestIP` is used for
one thing in `determineEndpoints`
(`wgengine/magicsock/magicsock.go:1336`): when it is true and a fixed
local port is configured, add one extra guess — the observed IP with the
*local* port, on the chance the user forwarded it by hand. That is all.
The punching happens regardless.

**The part worth taking is `GetGlobalAddrs`** (`netcheck.go:137`). They
do not publish an address. They publish a *set*: the best-latency
observation, plus every other observed `AddrPort` seen **more than
once**, single sightings dropped as hard-NAT noise. The comment names
our carrier without having met it — "bad NATs that start to provide new
mappings for new STUN sessions mid-expiration, even while a live mapping
for the best latency endpoint still exists ... new traffic to the old
endpoint will not succeed, but new traffic to the newly discovered
endpoints does succeed."

That is not the span of ports this step already measured and rejected.
A span is arithmetic — `+1`, `+3`, `+64` — and it failed because the
doors are not ordered. A set of *separately observed* addresses is
evidence, and every member of it was a real door at some moment. The
cost is bounded by how many the NAT actually minted, not by how far we
are willing to guess.

Two supporting facts make the set stay true. netcheck sends its STUN
through magicsock's own data socket (`sendUDPNetcheck`,
`magicsock.go:1605` → `c.pconn4`), not a throwaway, so every observation
belongs to the socket that will carry the traffic — the property
`internal/transport/selfaddr.go` had to build by hand here. And it runs
again: a full report at most every 5 minutes (`fullReportInterval`),
incremental ones on a `periodicReSTUNTimer`, each one re-running
`determineEndpoints` and republishing the set. An address is never
older than a few minutes, and there are several of them.

**And `natcheck` now asks the question one round cannot.** Round zero is
what the command always was. Then it repeats every
`RATATOSKR_NATCHECK_WAIT` (30s) for `RATATOSKR_NATCHECK_FOR` (3m), and
each round asks two things: an **anchor** reflector re-asked every time,
which says whether the mapping this socket already holds survives, and
one reflector **spent once and never asked again**, which is the only
kind that can show the allocator has moved — a destination already
spoken to answers from the port it was given then. A peer is always a
first-time destination, which is what makes that the question worth
asking. Six rounds separate three classes a single round renders
identical: the mapping held and every first-time destination got the
same port (publishable); the mapping held but a first-time destination
got a different one (this carrier — the class that defeats publishing);
or the mapping did not survive at all. RFC 4787 has no name for the
middle one because it asks its questions at a single moment.
`TestClassifySeparatesHeldMappingFromFreshDestination` pins it to the
real numbers, `35749` held beside a fresh `61482`.

Two things the first version of that got wrong, both caught by running
it. The reflector list was five `stunN.l.google.com` hosts and they all
resolve to **one address**, so five rounds asked the same destination
five times, agreed with themselves, and printed a pass. `natcheck` now
records every address it has spoken to and refuses to count a reflector
that shares one, saying so on the line; `Servers()` is eight operators
that each answered from this house on 7 Sep 2026 on eight distinct
addresses. And a reflector that is down used to cost a round its only
evidence, so a round now spends another from the pool rather than
returning empty — the round cannot be retaken, because the next one is a
different moment.

Run from home, 7 Sep 2026: six rounds, six operators, one port
(`180.252.216.153:63397`) throughout. This line is not the problem and
never was.

**Pass/fail: passed by giving the opposite result to the one the item
assumed.** Tailscale's probe would call this network endpoint-independent
too. Ours now does not, and the reason it now does not is a question
Tailscale never asks — because their design does not need the answer.

### Read: `net/portmapper`, 7 Sep 2026

All three protocols are tried, and the order is not the obvious one.
NAT-PMP and PCP share port 5351 and are attempted together in
`createOrGetMapping` (`net/portmapper/portmapper.go:550`), PMP by
default and PCP only when a recent probe saw PCP and not PMP. UPnP on
1900 is the fallback, taken when neither answered within
`portMapServiceTimeout`. Before any of it, `gatewayAndSelfIP` refuses
outright if the default gateway is not in a private range
(`ErrGatewayRange`) — a machine whose gateway is already public has
nothing to ask.

**What they do with a router that cannot name its external address is
the answer to our question, and it is: prefer, then settle, then fail.**
`selectBestService` (`net/portmapper/upnp.go:350`) scores every UPnP
device on three properties — connected, has an external IP, and that IP
is not private — and returns immediately on a device with all three. If
none has all three it falls back in order to connected-with-private-IP,
then merely connected. So Tailscale *will* use a router whose external
address is RFC 1918, and publish the resulting endpoint as one more
candidate that costs nothing when it fails.

That is not our case. Our router returned an **empty string**, not a
private address. `netip.ParseAddr("")` fails, the device scores nothing,
and `getUPnPPortMapping` (`upnp.go:630`) re-asks and returns the error
rather than mapping. Tailscale gets exactly what we got here: nothing.
The only case they handle that we did not is `0.0.0.0` and loopback,
rejected explicitly with a bug number attached.

**Pass/fail: the explanation is measured, and it is that the mapping is
useless for a reason no code can route around.** The house has one NAT
we can talk to and one we cannot. The router accepts `AddPortMapping`
and the forward does nothing, because the address the forward is
attached to is itself behind the carrier's NAT; the router cannot report
an external address because it does not have one. Tailscale would
publish that endpoint anyway on a private-IP router and it would fail
the same way — theirs is a cheap extra candidate, not a fix.

**And the phone side can never be mapped.** The phone is the gateway,
the carrier NAT is above it, and a carrier offers no PCP, NAT-PMP or
UPnP to a subscriber. There is nothing on that side to ask. Port mapping
is closed as an avenue for this pair, on both ends, for different
reasons — and that is now established rather than assumed.

### Read: `wgengine/magicsock` and `disco`, 7 Sep 2026

**The birthday attack is not in the source.** The plan above listed
"send to many ports at once, bet on a collision" as Tailscale's
hard-NAT path and asked for their numbers. There are no numbers, because
there is no such code. `hard NAT` appears twice in the whole tree: once
in `determineEndpoints` to add a single extra candidate, and once in
`GetGlobalAddrs` to *drop* endpoints seen only once as hard-NAT noise.
Nothing sprays ports. That closes the last idea this step was holding in
reserve, and it closes it by finding out that the system we were going
to copy does not do it either.

What they do instead has two halves, and both are takeable.

**Half one: an address is a set, and it is never more than 27 seconds
old.** `endpointsFreshEnoughDuration = 27 * time.Second`, with the
comment "UDP NAT mappings typically expire at 30 seconds, so this is a
few seconds shy of that". `enqueueCallMeMaybe`
(`magicsock.go:2617`) — their DCUtR CONNECT — checks that clock
*before signalling*, and if the endpoints are older it re-runs STUN and
re-enters itself when the fresh answer lands. Only then does it send
`disco.CallMeMaybe{MyNumber: eps}`, carrying every endpoint at once.
Our `/ratatoskr/observed/1.0.0` answer is minutes old by construction
and there is one of it.

**Half two: the path is chosen by what answers, not by what was
planned.** `sendDiscoPingsLocked` (`endpoint.go:1415`) pings *every*
candidate endpoint in parallel, rate-limited per endpoint by
`discoPingInterval`, and the first pong wins. `heartbeatInterval` is 3s,
`trustUDPAddrDuration` 6.5s — a direct path is trusted as exclusive only
that long without a pong, and DERP resumes if it goes quiet.
`goodEnoughLatency` is 5ms, below which no better path is sought. So the
upgrade is not a decision taken once at dial time; it is a race that
keeps being re-run for the life of the session. `connect` already serves
over the relay and upgrades in the background, which is the same shape,
but it upgrades once.

**And their current answer for a pair that cannot punch is not only
DERP.** `net/udprelay` and `wgengine/magicsock/relaymanager.go` are
Tailscale Peer Relays: one *node in the user's own tailnet*, with a
routable address, forwards UDP for two nodes that failed to meet.
`discoverUDPRelayPathsInterval` is 30s. That is worth naming here
because the bytes stay on the user's own hardware, so it costs the
operator nothing and does not make heimdall the normal data path that
`SPEC.md` §6 forbids. It is the user's desktop carrying it for the
user's phone.

### The decision, written before building it

The two shapes offered were "punch below libp2p" and "dial without
DCUtR". **Neither. Keep DCUtR and fix what is handed to it**, because
the reading says our failure is in the input and not in the mechanism.

DCUtR's CONNECT already carries an address *list*. Every attempt in this
step handed it one address — stale from the relay, then freshly measured
from a reflector, then one address plus arithmetic. Tailscale hands over
a set, and the set is built from independent observations rather than
from a rule. Three changes, all inside code that already exists:

- **Ask several reflectors through `internal/transport/selfaddr.go`,
  not one.** It already owns libp2p's QUIC socket through
  `quicreuse.OverrideListenUDP`. Keep every distinct answer, and follow
  `GetGlobalAddrs` in requiring a second sighting before believing an
  address — a single sighting is a door minted for that observer alone.
  Their rule has an exception worth copying exactly: the best-latency
  answer is kept whatever its count, and only the *others* need
  corroborating. Without it a machine behind an ordinary NAT whose
  reflectors happened to disagree would publish nothing at all, which is
  a worse answer than one uncorroborated address.
- **Re-measure on a clock shorter than the mapping lifetime**, and
  refresh before signalling rather than once at startup. 27 seconds is
  their number and there is no reason to invent another.
- **Publish the set through the `AddrsFactory` that is already there.**

The honest caveat, stated so the spike is not read as a promise: on this
carrier a reflector saw `61482` while the relay saw `35749`, tens of
thousands apart, so the ports toward observers may all be wrong about
the port toward a peer. What makes it worth measuring anyway is that
every member of the set is a door this socket really opened, and
`GetGlobalAddrs`'s own comment describes precisely this NAT — "new
traffic to the old endpoint will not succeed, but new traffic to the
newly discovered endpoints does succeed". It is three addresses in a
CONNECT, not sixty-five dials, so the cost is bounded whether or not it
lands.

**If it does not land, the answer is a peer relay and not heimdall.**
One of the user's own machines with a routable address, forwarding for
two that cannot meet — Tailscale's `net/udprelay`. It is preferable to
heimdall because the bytes never leave hardware the user owns, which
keeps §6 satisfied without spending the operator's egress. That is a
step of its own, not a fallback bolted onto this one.

### What is left: the measurement

The reading list that stood here has been read, decided and built. Its
answers are the three `Read:` sections and the decision above, and the
`Built 8 Sep 2026` section below. What it asked for and never got is the
test, which nothing has passed yet.

**Measure it on the same two machines.** Home Wi-Fi to phone hotspot,
the agent at least three minutes old, `RATATOSKR_DIAG=1` on both ends so
the measured set and its reflector counts are printed, and the line to
look for is `connection to ... went direct`. The three-minute wait is not
ceremony — every idea so far has passed at zero minutes and failed at
three.

Two things to carry into it. `RATATOSKR_NO_MDNS=1` is needed on the
hotspot Mac or the agent takes every terminal window down with it. And
`scripts/punchpair.sh` measures the network without libp2p in the way,
which is the control any new punching code needs beside it.

### Built 8 Sep 2026: a set of addresses, and a punch that keeps trying

Both halves of the decision above are in, and neither of them replaced
DCUtR. The numbers are still owed.

**The address is a set now.** `selfAddr.Addrs` asks every reflector in
`stun.Servers()` at once on libp2p's own QUIC socket, keeps the first
answer back plus every other address two reflectors agree on, and caches
the result for `endpointsFresh` — 27 seconds, their constant and their
reasoning. `Host.refreshMeasured` re-runs it on that clock and stores the
set; the `AddrsFactory` that has been there since the relay work
publishes it. That last part is what makes it reach a peer with nothing
new on the wire: basichost recomputes `Addrs()` every five seconds, an
address set that changed emits `EvtLocalAddressesUpdated`, and identify
pushes it to everyone already connected. The punch filter offers the same
set to DCUtR, where it used to offer one address.

**And the punch is a loop rather than an event.**
`internal/transport/upgrade.go`: every relayed connection, inbound or
outbound, starts a goroutine that re-dials the peer's non-circuit
addresses with `network.WithForceDirectDial` every five seconds until the
path is direct, the peer disconnects, or the host closes. libp2p's own
hole puncher gives up after three attempts against the addresses it had
when the connection opened, and it is unexported, so it cannot be asked
for a fourth — this runs beside it, not instead of it, and either one
landing ends both.

The reason it can skip DCUtR's round trip is worth writing down, because
it is the whole trick: both ends start their clock from the same event,
the relayed connection they share, so their dials land within a round
trip of each other without negotiating anything. That is the agreement
DCUtR spends a CONNECT/SYNC reaching. Each dial also opens the outbound
mapping the other end's dial needs, which is what makes two crossing
dials a hole punch. `bestConnToPeer` prefers an unlimited direct
connection over a relayed one, so every stream opened after the upgrade
takes the new path with nothing to switch over.

`RATATOSKR_UPGRADE_EVERY` (5s) and `RATATOSKR_UPGRADE_DIAL` (5s) are the
knobs. `RATATOSKR_PUNCH_WINDOW` went from 30 to 90 seconds, because a
window shorter than one 27-second re-measurement reports a failure the
next attempt would have fixed.

**What is not yet known is whether it lands**, and the honest caveat from
the decision above is unchanged: on this carrier a reflector saw `61482`
while the relay saw `35749`, so every address in the set may be wrong
about the port toward a peer. The set is cheap and bounded either way —
three addresses in a CONNECT, one dial every five seconds — and the test
is the one that has failed every previous idea.

**The QUIC-first finding is withdrawn.** Aimed at the same span of
ports the bare punch opens, a run with the bare window set to zero
connects: QUIC handshakes as the first thing the socket ever sends to
that peer, and a stream carries bytes both ways. So the payload was
never the difference. The earlier comparison put a bare punch and a
QUIC-first run in separate processes on separate sockets, each with
its own address measured at its own moment, and on a carrier whose
port creeps every few seconds that is two aims, not two payloads. It
read as a property of QUIC because both halves were wrong about the
port and only one of them was wrong quietly.

What survives it is one finding: nothing about the path resists a hole
punch, at one second of warm-up or none. What defeats it is the address
that gets published. The rest of that paragraph — that the port advances
with time, holds once advanced, and advances by one — was read from a
carrier that turned out to renumber per destination, and the sections
above retract it.

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
