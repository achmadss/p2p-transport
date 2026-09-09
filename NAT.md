# NAT.md — the step 3 notebook

Everything measured while finding out whether two of the owner's machines
can reach each other directly across the Internet. It is kept because the
answer arrived through three readings that were wrong, and a reader who
sees only the conclusion will propose the ideas that already failed.

It is a notebook, not a plan. `TODO.md` step 3 holds the work and the
pass/fail; this file holds why each box is ticked and what was learned
between them. Entries are in the order they happened, so an earlier
paragraph may be corrected by a later one — where that happens it says
so rather than being quietly rewritten.

The state as of 8 Sep 2026: two machines on different carriers connect,
transfer and verify their bytes over heimdall, which `SPEC.md` §1 permits
and the owner has confirmed is not a failure. A direct path between a
phone hotspot and a home line has been opened once, at 251 ms, on an
agent seconds old, and never on one minutes old. Both halves of the
Tailscale answer — an address set re-measured every 27 seconds, and a
punch retried for as long as a peer stays relayed — are built and
unmeasured on that pair. What closes step 3 is the test described under
[What is left](#what-is-left-the-measurement).

---

## The spike, 6 Sep 2026 onward

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

---

## Relay hygiene, and the address that was always stale

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
the reflector had named. (The spread was a knob for a while; the
paragraphs below retract the finding, and it was removed with the rest
of the idea on 9 Sep 2026.)

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

So the span went, and `quicAddr` offers the one address it measured.
What remains is a fresh address instead of a stale one, which is right
wherever the published address is merely old and does not help here.

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

**Not from the development Mac**, which cannot run the test at all. Its
endpoint security kills the agent, deletes `dist/ratatoskr` and takes
the launching terminal with it, and Cloudflare WARP gives it a different
port per destination, so even a run that survived would be measuring
WARP rather than a carrier. `CLAUDE.md` under **Commands** has the
detail. `RATATOSKR_NO_MDNS=1` still goes on every run, and
`scripts/punchpair.sh` measures the network without libp2p in the way,
which is the control any punch failure needs beside it.

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

### Built 9 Sep 2026: the LAN rung, which libp2p had quietly removed

Two machines on one LAN that meet over heimdall could not upgrade to
the LAN. Not "usually did not" — could not, structurally, and the
reason is one function in a dependency.

go-libp2p's identify filters the addresses a peer tells it *on the
receiving side*, by the address of the connection that carried them
(`p2p/protocol/identify/id.go`, `filterAddrs`, called from
`consumeMessage`). The rule is: a public connection may only teach you
public addresses. A circuit multiaddr through a relay on a VPS begins
with the VPS's public IP, and `manet.IsPublicAddr` reads exactly that,
so a relayed connection is a public one and every `192.168.x.x` the
far end offered is dropped before it reaches the peerstore. DCUtR
declines the same addresses one layer up: `holepuncher.go` only ever
direct-dials a peerstore address it considers public.

So the peerstore of a relayed peer holds precisely the addresses that
need a hole punched through a carrier, and never the one address that
needs nothing at all. `dialDirect` was reading that peerstore. The LAN
rung existed in the design and in none of the dials.

The fix is a peer asking a peer instead of asking libp2p:
`/ratatoskr/addrs/1.0.0` answers with `host.Addrs()`, circuits removed,
one per line, and `askAddrs` reads it on every tick of the upgrade loop
— every tick rather than once, because the far end re-measures its
public address on the 27-second clock and a laptop that changes network
changes its LAN address too. It is a kilobyte over a relay, which
`PLAN.md` §7 already counts as free; only bulk transfer cares about the
path.

**The ladder is raced, not walked.** The rungs are the LAN, then the
Internet, then the relay already underneath — and `dialDirect` hands
all of them to one `Connect` rather than trying them in turn. libp2p's
dial ranker groups candidates into private, public and relay and starts
the first two together (`swarm/dial_ranker.go`), and `directOnly` has
already removed the third. A LAN dial completes in a millisecond or
two; a punch across the Internet is still waiting on its first round
trip. The lowest rung that exists wins on latency alone, and walking
the ladder would only add five seconds to the common case, where the
private address the peer advertises belongs to a network this machine
is not on and will time out.

One thing this does not do: if a public dial somehow lands first — two
machines on one LAN would need a router that hairpins — the loop stops
there and the LAN path is never tried again. Worth an hour only if a
measurement finds it.

Unmeasured, like everything else built in this section. The test is in
`TODO.md` step 3: two machines on one LAN, `--via relay`, and the
second pass must run over a private address with heimdall's counters
flat.

### Built 9 Sep 2026: moving a transfer that is already running

The second pass in `bench` exists because the first one could not move.
A libp2p stream is bound to the connection it was opened on — there is
no stream migration in libp2p, and QUIC's own migration is not the
thing it sounds like: RFC 9000 §9 moves one connection between *local*
addresses, client-initiated only (`quic-go/connection.go:1261`, and
`:3085` refuses it from a server). The relay and the peer are two
remote endpoints with two handshakes and two Noise sessions. There is
no path to migrate along.

So the transfer moves and the connection does not, and it does it
without being asked. Every four megabytes `transfer` looks up from
sending and asks `PathTo` what the best path to this peer is now. When
that beats the path the current stream is on — `Path.BetterThan`, which
is where the ladder LAN, Internet, relay is finally written down — it
finishes that stream, opens a new one, and sends the rest down it;
`bestConnToPeer` hands the new stream the better connection. The check
reads live connections and costs nothing, so a transfer with nowhere
better to go stays on one stream from beginning to end and never pays
for the ability.

```
connected to VqfZ-X78a over relay
moved from relay to lan after 24 MB
100 MB over lan, after 1 move(s) in 11.3s = 8.8 MB/s
```

The rate over a run that moved is an average of both paths and not
either of them; the number worth reading is the move line.

Granularity is one interval, and that is not a limitation of this
design so much as a preview of the real one: the File API's ranged
reads are already one request per range, so a transfer that survives a
path change is what steps 4 and 5 get without asking. What this adds is
the measurement, now, on the diagnostic that already exists.


### Measured 9 Sep 2026: the LAN rung works

Two machines on one LAN, mDNS off, meeting over heimdall on purpose:

```
connected to HtZh-LKTg over relay
moved from relay to lan after 12 MB
connection to HtZh-LKTg left the relay for lan after 10s
3000 MB over lan in 33.304s = 90.1 MB/s, averaged across relay then lan
second pass, all of it over lan
3000 MB over lan in 27.843s = 107.7 MB/s
```

So the addr exchange found the LAN address identify had thrown away,
the ladder picked the lowest rung rather than hairpinning out to the
Internet, and a transfer already running moved itself across at 12 MB
of 3000 without being restarted. 107.7 MB/s is a gigabit line saturated
either way, and the mixed first pass averaging 90.1 tells you how
little of it went the slow way.

Three log lines were wrong and are fixed here, because two of them
would have misled the next reading:

- `went direct after 10s` said "direct" meaning "not relayed", but
  `direct` is also the name of the path *through the Internet*. A LAN
  upgrade announced itself as a trip out and back. It names the path
  now.
- `upgraded to lan after 251ms` was not a measurement. `watchUpgrade`
  starts after the first pass returns, the path was already `lan` by
  then, and 251 ms is its 250 ms ticker firing once. That is also the
  answer to the 251 ms that appeared in an earlier run and read as a
  punch time: it was never one. The watch is now skipped when the
  transfer has already moved.
- `3000 MB over lan ... = 90.1 MB/s` named the path it ended on for a
  number that averaged two. It lists them.

And `PathTo` returned the first non-relay connection it found rather
than the best, which after an upgrade leaves two. It uses
`Path.BetterThan` now, so a machine holding both a LAN and an Internet
connection reports the LAN.

What is still owed is the same test between networks: a hotspot and a
home line, where the carrier renumbers and nothing has ever landed.

### 9 Sep 2026: the punch lands, and the number is missing

The owner reports that the remaining runs passed, including the one
this notebook was written around failing: home Wi-Fi to a phone
hotspot, direct. Also macOS↔Windows↔Linux with both peers behind one
NAT.

The output was not captured, so this entry records a verdict and not a
measurement — which is the one thing every retracted section above has
in common. It is written down that way on purpose. What is known is
that a direct path opened on a carrier that renumbers per destination.
What is not known is how long it took, how often it succeeds, or how
old the serving agent was — and those are exactly the variables that
turned three earlier readings into retractions.

So: the punch works here, and nobody should quote a rate for it.
Anyone building on the direct path should re-run the pair with
`RATATOSKR_DIAG=1` and paste the result in below this line.

What was built between the failures and this: a set of addresses
re-measured every 27 seconds instead of one taken at startup, a punch
retried every five seconds for as long as a peer stays relayed instead
of DCUtR's three attempts, an address exchange that recovers the LAN
address identify discards over a public connection, and a transfer that
moves itself onto a better path while it is running. Which of them made
the difference on the hotspot is not known either.

### 9 Sep 2026: the ladder is climbed in both directions

`watchForRelayed` hooked `ConnectedF` only, which is half a ladder. A
direct connection that dies leaves the relayed one beside it still
carrying the session — libp2p goes back to it for every stream opened
afterwards — and no *new* connection arrives to say so. The punch loop
had already exited when the path went direct, so a peer that fell back
to the relay stayed there with nobody trying to climb again.

Both hooks now call `punchIfRelayed`, which asks for the best path
rather than looking at the connection it was handed: a second relayed
connection to a peer already reached directly is not a reason to punch,
and a direct connection closing is not a reason not to.

That still left the case where nothing is left at all — a LAN-only
session whose Wi-Fi drops has no second connection to fall back to, so
`repair` saw `unknown`, and nothing happened. `restore` covers it: for
30 seconds it dials every direct address the peerstore kept from
identify, all of them in one call so the LAN wins on latency where it
exists, and only when that finds nothing does it dial the relay.
Landing on the relay is not the end, because that arrival starts the
punch loop and the ladder is climbed again from below.

Two things it deliberately cannot do. It cannot ask the peer where it
lives — `AddrsProto` needs a connection, and this runs when there is
none — so it works from the peerstore, which identify filled while the
connection was up. And it cannot save the request that was in flight:
a stream dies with its connection, and reissuing it is resume, which
arrives with the File API's ranged reads in step 5.
