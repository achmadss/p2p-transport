# FLEET.md — many relays, one coordinator

`PLAN.md` §8 describes one relay named in a config file. This describes
the fleet that replaces it: relays that come and go, a coordinator that
places and meters them, bandwidth divided per subject rather than per
machine, and a VPS count that follows demand.

Nothing here changes the seam. `transport`'s surface (`PLAN.md` §2.1)
gains no method, and the application above still hands the transport
opaque address strings and never learns what a relay is.

---

## 1. The pieces

| Name | Role | Where |
|------|------|-------|
| `bifrost` | The coordinator. Relay registry, placement, leases, quotas, metering, scaling. | Its own VPS, fixed address |
| `heimdall` | The relay. Many of them, one per VPS, ephemeral addresses. | Created and destroyed by bifrost |
| `ratatoskr` | The agent. Leases a relay instead of reading one from a file. | The user's machines |

Bifröst is the bridge; Heimdall guards it. The thing that decides which
bridge you cross is Bifrost.

**Bifrost's address is the one fixed thing in the system.** Everything
else may be replaced at any moment. Agents and relays are compiled or
configured with bifrost's address and peer id, and learn everything else
at runtime.

---

## 2. Identity, and why there are no tokens

Bifrost is a libp2p host. Agents and relays reach it over the same
transport this repository already builds, which means the Noise
handshake has already proved who is asking before bifrost reads a byte
of the request.

So there is no bearer token, no API key, no secret to rotate or leak. A
peer id *is* the credential.

```text
/ratatoskr/lease/1.0.0    agent  -> bifrost    where do I relay?
/ratatoskr/fleet/1.0.0    relay <-> bifrost    registration, quotas, drain
```

### The word bifrost does not know is "account"

`SPEC.md` §11 keeps accounts out of this repository, and this design
does not put them back. Bifrost knows **subjects**: an opaque id with a
`min` and a `max` rate, and a set of device peer ids. Who owns a
subject, how they signed up, and what they pay is the application's,
and it tells bifrost over a small admin API:

```text
PUT    /v1/subjects/{id}         {min: 2MB/s, max: 10MB/s}
PUT    /v1/subjects/{id}/devices {peer_ids: [...]}
DELETE /v1/subjects/{id}
GET    /v1/subjects/{id}/usage
```

That is the whole coupling. Changing a plan is a `PUT`, and §5 says what
happens next.

---

## 3. The lease

```text
agent starts
  │
  ├─► bifrost   /ratatoskr/lease/1.0.0
  │             (peer id already proven by the handshake)
  │
  │   bifrost:  find subject for this peer id
  │             pick a relay with room for subject.min      §6
  │             push (peer id, subject, min, max) to that relay
  │             record the placement
  │
  ◄─┤ {relay: {peer_id, addrs}, ttl: 120s}
  │
  ├─► connect to the relay, force the reservation
  │
  └─► renew at ttl/2, forever
```

Bifrost pushes the admission to the relay **before** replying, so the
reservation is never refused by a race between the two.

The relay's `ACLFilter.AllowReserve` — which go-libp2p already exposes,
no fork needed — checks the pushed table and fails closed. An agent that
learns a relay address some other way cannot use it.

### When a relay dies

Two parties notice independently, which is the point.

**The agent** sees its reservation or its connection to the relay drop.
It removes that relay from its circuit set and asks bifrost again,
backing off 1s, 2s, 4s … capped at 30s with jitter, **for as long as the
process runs**. There is no deadline. `restore()`'s 30-second window is
right for direct addresses, where a peer that has not answered in 30
seconds is probably off; it is wrong for "I need some relay, any relay",
which is never a question that should be given up on.

**Bifrost** misses a heartbeat, marks the relay dead, and reassigns
every subject placed on it. The agents are already asking.

Recovery is roughly: detection (up to a few seconds) + one lease round
trip + a reservation + the peer re-dialling. Call it 3–10 seconds, and
measure it rather than trusting that.

> **A relayed connection dies with its relay, and so does every stream
> on it.** The session is encrypted peer to peer, but it rides that
> relay's hop; there is no migration in libp2p or QUIC that saves it.
> Failover reconnects the *peer*. Reissuing the request that was in
> flight is the application's, exactly as `PLAN.md` §2.2 says.
>
> If 3–10 seconds is too long, the next move is for an agent to hold
> reservations on two relays at once so the second is already warm. That
> doubles reservation load and therefore changes §6's arithmetic, so do
> it after measuring, not before.

---

## 4. Bandwidth

The requirement: a subject has an aggregate rate; it divides between
that subject's machines **by demand**, so a machine on a 1 MB/s link
takes 1 and leaves the other 9 to a machine that can use them; a floor
is guaranteed; a ceiling is enforced; and all of it can change while a
transfer runs.

That is what two downloads in one browser do, and it is not what an
equal divisor does. Ten megabytes over five machines is 2 each, and if
four of them can only pull 1, six megabytes a second evaporate. Demand
has to do the dividing.

### 4.1 Demand divides it, and a single bucket already does that

For each subject S, **one token bucket at S's current rate, shared by
every circuit S has on that relay.** There is no per-circuit limiter at
all.

That is the whole mechanism. A reader pulls tokens as fast as it can
consume them, so a machine whose link tops out at 1 MB/s takes 1 MB/s
worth and no more, and the machine beside it takes everything left. Two
greedy machines land near half each because they contend for the same
bucket. Five machines where two are idle leave the two active ones the
whole rate.

An earlier draft of this document put a second limiter under the bucket,
set to `R / active`, to make the division equal. That was wrong: equal
is the one split that guarantees waste whenever machines differ, and
machines always differ. Deleting it makes the code smaller and the
answer better.

`min` is still **not** a limiter. You cannot guarantee a floor by
throttling — you guarantee it by not overselling, which makes `min` the
currency of placement and of scaling (§6). `max` is what the bucket's
rate is set from.

**Changing a limit is a push, not a reconnect.** Bifrost sends
`SETLIMIT{subject, min, max}` and the relay calls `SetLimit` on the
bucket. `x/time/rate` supports this at runtime; it takes effect within
one refill interval. An upgrade applies mid-transfer, and so does a
downgrade.

### 4.2 A subject spans relays, and the allowance loop is what holds it

One bucket per subject works while a subject's circuits are on one
relay. They are not: circuits are placed independently, because pinning
every machine of a subject to one relay makes that relay hot with no
remedy short of moving all of them at once, and it caps a subject at one
machine's capacity.

So S's rate is **divided between the relays carrying S, in proportion to
what each can actually use**, and re-divided on a clock:

```text
every second, per relay, for each subject with live circuits:

  relay  -> bifrost   {subject, used_bps, throttled, circuits}
  bifrost:            max-min allocate R across the reporting relays
  bifrost -> relay    {subject, allowance_bps}
  relay:              bucket.SetLimit(allowance)
```

The allocation is ordinary max-min fairness, about twenty lines:

```text
remaining = R
unsatisfied = every relay reporting demand for S
loop:
    fair = remaining / len(unsatisfied)
    for each relay whose demand <= fair:
        give it its demand; remove it; remaining -= demand
    if none were removed:
        give every remaining relay `fair`; done
```

Demand is estimated the way every congestion controller estimates it:
a relay that spent the period blocked on its bucket asks for more than
it used; one that did not asks for what it used plus a little slack.
Throttled → `used × 1.5`. Not throttled → `used × 1.1`. Floor it at
something small so a quiet relay can wake up.

Walk the example. R is 10 MB/s. Laptop A is on relay 1 behind a 100 MB/s
link; laptop B is on relay 2 behind a 1 MB/s link.

```text
demands: relay1 = 100 (throttled), relay2 = 1
fair = 5      -> relay2 asks 1, which is under 5: give it 1
remaining 9, unsatisfied {relay1}
fair = 9      -> relay1 asks 100: give it 9
                                        A = 9 MB/s, B = 1 MB/s
```

Which is exactly the answer the same two machines would get on one
relay, from one bucket, with no allocator at all. That is the property
to hold on to: **placement never changes the answer, only how long it
takes to get there.**

**The first period is free of overshoot, because bifrost places the
circuit.** It knows a circuit is about to exist when it issues the
lease, so it carves that relay's opening allowance out of R in the same
breath and no relay ever forwards a byte it was not allocated.

### 4.3 What this costs, and it is not nothing

**One period of lag.** A machine that stops transferring leaves its
allowance stranded on its relay for up to a second before the rest can
have it. Shorten the period and you pay in messages; a second is a
reasonable place to start and the number should be measured, not
assumed.

**Contention fairness is approximate.** Two equally greedy machines on
one bucket land near half each because reservations are granted in call
order, not because anything enforces it. If that turns out to matter —
and it only matters when machines are equally greedy, which is the case
where nobody is being hurt — the fix is a deficit round-robin scheduler
over the subject's circuits, not another limiter.

**Chatter.** One small message per relay per subject per second. Thirteen
subjects across four relays is nothing. Ten thousand subjects across
fifty relays is not nothing, and the answer then is to report only
subjects whose demand *changed*, which is most of the saving for very
little code. Do that when the number is real.

**Bifrost dying freezes the split.** Relays keep their last allowance,
which can never sum above R, so the failure is safe but unfair: A stays
at 9 even after it finishes. Traffic continues; the division stops
adapting. That is the right trade and §7 says why.

### 4.4 Three limiters, and only three

In the byte path on a relay:

```text
relay total       everything this VPS forwards, at the configured ceiling
subject allowance this subject's share of R on this relay          §4.2
(nothing)         per circuit — demand does that                   §4.1
```

The relay-level ceiling is what makes §6's "relay A is full" a fact
rather than an estimate, and it is deliberately a little under what the
VPS can really do, so that being full is something bifrost sees coming
rather than something users feel.

### 4.5 Where this goes in the code, and why it is a fork

Read against go-libp2p v0.49.0, `p2p/protocol/circuitv2/relay`:

| Hook | What it gives | Enough? |
|------|---------------|---------|
| `WithACL(ACLFilter)` | `AllowReserve`, `AllowConnect` | **Yes** — this is admission, §3 uses it as-is |
| `WithLimit(*RelayLimit)` | `{Duration, Data}` per circuit, identical for everyone | No. A byte *cap*, not a rate, and not per subject |
| `WithMetricsTracer` | `BytesTransferred(cnt int)` | No. No peer id in the signature, so it cannot meter per subject either |

The byte pump is `relay.go`'s `relayLimited`/`relayUnlimited` calling
`copyWithBuffer`, a hand-rolled `io.CopyBuffer` with a metrics call in
the loop. There is no per-connection rate hook and no way to add one
from outside the package.

An earlier draft of this section concluded: vendor `circuitv2/relay`
into `internal/relay` and add a `Shaper` hook to `copyWithBuffer`. That
was wrong, and the thing it missed is that the copy loop is not the only
place the bytes can be caught.

**Every byte a relay forwards arrives through one of two calls on the
host it was built with**: the handler it registers for machines dialling
in (`SetStreamHandler` on the hop protocol), and the stream it opens to
the machine being dialled (`NewStream` on the stop protocol). `relay.New`
takes a `host.Host`, which is an interface. So the hook is a host that
embeds the real one and overrides those two methods, returning streams
whose `Read` waits on the subject's bucket — twenty lines in
`cmd/heimdall`, against a thousand-line fork to keep in step with
upstream. Nothing else on the machine is shaped: the coordinator link
and the address protocols run on the real host.

Reads only, and that is what makes the accounting right. A forwarded
byte is read from one leg and written to the other, so shaping every
leg's reads charges each byte exactly once, in the direction it
travelled. Waiting after the read rather than before is the only order
available — how many bytes there are is not known until they have
arrived — and it is enough: the wait holds the next read off, the flow
control window fills behind it, and the sender stops. The blocked time
§4.2 wants is the same wait, measured; it is not recorded yet because
nothing reads it until §5.

The buckets live in `internal/shape`, keyed by subject, with the relay's
own ceiling under them. `x/time/rate` is the bucket, it was already in
the module, and `SetLimit` on a live limiter is what makes a rate change
reach a transfer already running.

Rejected, and why:

- **`tc`/HTB on the VPS.** Shapes by IP address. Several of a subject's
  machines can sit behind one NAT and share an IP, and one subject spans
  many IPs and now many relays. Wrong key entirely.
- **Pinning a subject to one relay.** Simpler — no allocator — but it
  makes one busy subject a hot relay with no remedy, caps a subject at
  one machine's worth of capacity, and packs badly. This was the earlier
  design and it is the thing this section replaces.
- **`RelayLimit.Data` alone.** A monthly quota wearing a rate's clothes.
  It cannot answer "10 MB/s". It stays as a backstop under the shaper,
  since a byte cap and a rate are not the same guard.
- **Forking the relay.** See above. It shapes the same bytes and costs a
  thousand lines of somebody else's code, kept in step by hand for as
  long as this exists.

## 5. Metering

The same shaper hook counts bytes per subject. Relays expose Prometheus
metrics on a private port and bifrost scrapes them; bifrost also keeps a
rolling counter per subject so `GET /v1/subjects/{id}/usage` answers
without a Prometheus query.

Counting happens where the bytes are, and a relay that dies takes at
most one scrape interval of counts with it. If billing needs better than
that, the relay should checkpoint to bifrost on a short timer — but that
is a decision to make when someone is actually billing, not now.

---

## 6. Scaling

### 6.1 The arithmetic, including the part that gets missed

**A relayed byte crosses the VPS twice**: once in, once out. A "50 MB/s
VPS" therefore does not forward 50 MB/s of user traffic unless that
figure is per direction. Check which the provider means before sizing
anything.

```text
usable_per_vps = min(egress, ingress) × 0.7        headroom
want           = clamp(ceil(committed / usable_per_vps), minVPS, maxVPS)
committed      = Σ subject.min  over subjects holding a live reservation
```

Scale on **committed minimums, not observed throughput.** Observed
throughput is what everyone happens to be doing this second; the moment
they all wake up you have oversold and there is nowhere to put them.

Worked, with the numbers from the requirement: 50 MB/s per VPS,
per direction, gives ~35 MB/s usable. Thirteen subjects at 10 MB/s is
130 MB/s committed, so `ceil(130/35) = 4` VPS — **not three.** Three is
what you get by dividing by the sticker number and skipping the
headroom, and it is exactly the sort of arithmetic that oversells
quietly and shows up as everyone being slow at once.

### 6.2 Placement packs tight, on purpose

When bifrost places a circuit it picks **the fullest relay that still
has room** — not the emptiest.

That is the opposite of what load balancing usually does, and it is the
decision that makes shrinking possible. Spread every circuit evenly and
the fleet is permanently three relays at 40%: nothing is overloaded and
nothing can ever be destroyed. Pack tight and the fleet is two relays
full and one half empty, which is a fleet that can shed a machine the
moment demand drops.

"Room" means committed load after placing stays under `0.85 ×
usable_per_vps`. The headroom is what §4.1's demand-sharing bursts into,
so packing tight never means packing to the ceiling.

### 6.3 The loop

Every 30 seconds:

- **Up**, immediately, when `committed > 0.85 × capacity` fleet-wide, or
  when a placement fails for want of room, or when a relay's own
  measured throughput sits at its ceiling. 0.85 rather than 1.0 because
  a fresh VPS takes 30–90 seconds to boot and register, and that time
  has to come from somewhere.
- **Down**, only when `committed < 0.6 × capacity` has held for fifteen
  minutes. Relay churn costs reconnections for real users; a machine
  kept for an extra quarter hour costs pennies.
- Never below `minVPS` — the requirement is two, so that at least one
  relay survives any single loss.
- Never above `maxVPS`. At the cap, placement refuses new subjects
  rather than overselling the ones already placed. Refusing is honest;
  degrading everybody is not.

### 6.4 Rebalancing, in both directions

Relay A fills up: move some of it away. Relay A has room: pull work onto
it so something else can be destroyed. One mechanism, two triggers.

**Everything turns on what a move costs**, and that depends entirely on
what is on the circuit:

| What is being moved | Cost |
|---|---|
| A reservation with no live circuit — the agent is direct with everyone right now | **Free.** Re-lease it and nothing notices |
| A circuit that has moved no bytes for 30 seconds | **Free.** Nothing is in flight to lose |
| A circuit carrying a transfer | **A reconnect.** The connection dies, the stream dies, the application has to reissue — §3 |

So the rule is: **consolidation only ever makes free moves; shedding
makes costly ones only when the alternative is worse.**

**Shedding** — a relay is over its ceiling, or heading there. Move
cheapest-first: bare reservations, then idle circuits, until the relay
is back under. Only if that is not enough, and the relay is genuinely
saturated, move active circuits — largest first, because one restarted
transfer beats every transfer on that relay running slow. New capacity
should already be booting; §6.3 triggered at 0.85 for exactly this
reason, so shedding into a relay that does not exist yet is rare.

**Consolidating** — the fleet has more relays than demand needs. Pick
the **emptiest** relay and mark it `draining`:

```text
draining  -> no new placements land here
          -> every free move goes to a relay with room     §6.2 packing
          -> what is left drains by itself: leases expire, transfers
             finish, and pairs that punch through stop relaying at all
          -> when it hits zero circuits, or the window expires
destroy   -> provider API
```

Draining is patient because it is only saving money. A relay that still
has one active transfer on it at the end of the window is worth keeping
for another window; the whole point of the fifteen-minute hysteresis in
§6.3 is that being slightly overprovisioned is cheap and being wrong is
not. Force a drain only when the relay is being retired for another
reason — a failing host, a provider migration — and accept the reconnect
knowingly.

Two guards. A circuit is never moved twice within five minutes, or a
busy fleet will bounce the same agent between relays. And a relay that
just came up is not immediately a drain candidate — cooldown for one
scale-down window, or the loop will build and destroy the same machine
forever.

### 6.5 The provisioner interface

Bifrost calls a provider's API directly from its loop. Which provider is
a decision that will change — for price, for an outage, for a region —
so the loop talks to an interface, and the interface is **derived from
what the loop needs, not from any provider's documentation**. A provider
that does not fit adapts to this; this does not adapt to a provider.

What the loop actually needs is small:

```go
// Package provision creates and destroys the machines relays run on.
package provision

type State string

const (
    Starting State = "starting" // asked for, not yet answering
    Running  State = "running"
    Gone     State = "gone"
)

// A Machine is one VPS, capable of running one relay.
type Machine struct {
    ID      string    // the provider's id. Opaque to bifrost
    Key     string    // the idempotency key bifrost chose
    Addrs   []string  // public addresses; may be empty while Starting
    Size    string
    Region  string
    State   State
    Created time.Time
}

// A Spec is a machine bifrost wants to exist.
type Spec struct {
    Key      string // idempotency: two Creates with one Key make one machine
    Region   string // from Regions(); opaque to bifrost
    Size     string // from Sizes()
    Image    string // the pre-baked relay image
    UserData string // cloud-init: bifrost's address and peer id
}

// A Size is what bifrost places against. Bandwidth is the whole point.
type Size struct {
    ID        string
    Bandwidth int64    // bytes per second, per direction, forwardable
    Monthly   int64    // minor units, for choosing between providers
    Regions   []string
}

type Region struct {
    ID   string
    Name string
}

type Provisioner interface {
    Create(ctx context.Context, s Spec) (Machine, error)
    Destroy(ctx context.Context, id string) error
    List(ctx context.Context) ([]Machine, error)
    Sizes(ctx context.Context) ([]Size, error)
    Regions(ctx context.Context) ([]Region, error)
}
```

Five methods, and each one is there because the loop breaks without it.
The four properties underneath them matter more than the signatures:

**`Create` is idempotent on `Key`, and that is the adapter's problem.**
A create that times out has still probably created something. Retrying
without a key is how a scaling loop quietly runs two machines and bills
for both. Almost no provider offers idempotency natively, so the adapter
implements it: tag or name the machine with the key, and `List` before
creating. This is the single most important line in this section.

**`Create` does not wait for readiness.** It returns as soon as the
provider has accepted, with `State: Starting` and possibly no address at
all. Bifrost does not care: a relay is usable when it *registers over
libp2p and heartbeats*, which is a fact about the relay rather than a
fact about the provider. Every provider means something different by
"active", and none of them mean "the relay is answering". Not waiting
deletes a whole class of polling, and a whole class of wrong.

**`Destroy` is idempotent, and "already gone" is success.** The loop
must be able to call it twice without special-casing.

**`List` is the truth, and bifrost's database is a cache.** Reconcile on
every tick: a machine in the provider that bifrost does not know about
is an orphan from a crashed loop and must be destroyed, and a machine
bifrost thinks exists that the provider does not is a placement to
retire. Without this, a bifrost restart at the wrong moment leaks a VPS
that bills forever.

**`Size.Bandwidth` is bytes per second, per direction, that a relay can
actually forward.** §6.1's trap gets handled exactly once, here in the
adapter, rather than at every call site: if the provider quotes an
aggregate figure, or a monthly transfer allowance, or a burst rate it
will not sustain, the adapter converts and the loop never learns the
difference. `usable_per_vps` in §6.1 is `Size.Bandwidth × 0.7`, and it
comes from this call rather than from a constant someone typed.

Errors need one distinction the loop acts on, so the package defines it
and each adapter classifies into it:

```go
var (
    ErrNoCapacity = errors.New("provider has no capacity here") // try another region or provider
    ErrQuota      = errors.New("account limit reached")         // stop scaling up, alert
    ErrTransient  = errors.New("retry")                         // back off and retry
)
```

Anything else is fatal: stop the loop and page someone. A scaling loop
that retries a malformed request forever is worse than one that stops.

### 6.6 What the interface refuses to model

Deliberately absent, because they are provider dialect rather than fleet
requirement: SSH keys, firewalls and security groups, private networks,
volumes, snapshots, floating and reserved IPs, IPv6 assignment, DNS
records, tags as a queryable API, and every provider's own
wait-for-status ceremony.

Those belong in the adapter's own configuration, set up once when the
account is created — with Terraform, which is the right tool for things
that do not change. If a fleet requirement ever genuinely needs one of
them, it gets added here after that is proved, not in advance because a
provider happened to expose it.

Writing an adapter is then three questions, and only three: how does
this provider create a machine from an image with user data; how do I
list only the machines this fleet made; and what is the honest
forwardable bandwidth of each size. `internal/provision/<name>` per
provider, one file each, and the loop never changes.

## 7. When bifrost is down

It is a single point of failure, and the failure mode is chosen rather
than discovered:

- Relays keep enforcing the **last pushed** limits and keep forwarding.
- Agents keep using their **current** placement past its TTL rather than
  tearing down a working path.
- What stops is *new* placements and scaling.

So a bifrost outage is invisible to anyone already transferring and
blocks anyone starting fresh. Make bifrost's state small enough to
restore from a backup in minutes, and run a second instance when that
stops being good enough. Two bifrosts need agreement on placement, which
is a real distributed systems problem — do not take it on until the
single one has actually hurt.

---

## 8. What changes in the code that exists

**`transport/transport.go`** — `New(Config{Dir, Relays, NoLAN})`
becomes `New(Config{Dir, Coordinator, NoLAN})` — the relay list is
replaced by the coordinator that hands relays out, and step 4 already
spent the one API break this costs. The `circuits` slice,
built once at startup, becomes a set behind a mutex that the lease loop
replaces. `Addrs()` changes when the relay changes, so the peer has to
be told: bifrost knows both ends and carries it, and
`/ratatoskr/addrs/1.0.0` covers the case where some connection survives.

**`transport/upgrade.go`** — split `restore()`. The 30-second
window stays for direct addresses. Relay acquisition moves to a
host-level lease loop that never gives up. `redial()` reads the
refreshed circuit set instead of the frozen one.

**`cmd/heimdall/main.go`** — register with bifrost and heartbeat; accept
`ADMIT`, `SETLIMIT`, `ALLOWANCE`, `MOVE` and `DRAIN`; back `ACLFilter`
with the pushed table, failing closed; build the relay on a host that
shapes the two calls its bytes pass through (§4.5); report per-subject
bytes and blocked time every second, which is both the metering feed and
the demand signal §4.2 runs on.

**`internal/config/config.go`** — `relays []string` becomes
`coordinator`. Keep a manual relay override for single-relay
development; it is two lines and it keeps `make build` useful without a
fleet.

**`cmd/bifrost/`** — new, and four loops rather than one. A libp2p host
for agents and relays and plain HTTP+JSON for the application's admin
calls. The **allocator** runs every second and divides each subject's
rate across the relays carrying it (§4.2). The **scaler** runs every
thirty seconds and decides how many machines should exist (§6.3). The
**rebalancer** runs beside it and decides what should move (§6.4). The
**reconciler** runs against `Provisioner.List` and destroys what nobody
owns (§6.5). State is SQLite (`modernc.org/sqlite`, no cgo): subjects,
devices, relays, placements and the current allocation. It stays small
for a long time — move to Postgres when bifrost needs a second instance,
not before.

Only the allocator is on a hot path, and it is the one to keep boring:
it is a map, a sort and twenty lines of arithmetic, and it must never
block on the database.

---

## 9. Build it in this order

Risk first, and the part that costs money last.

1. **Bifrost with a static relay list.** Leases, placement, ACL push,
   the agent's endless re-lease loop. No shaping, no scaling. This is
   the part that touches every agent, so it is the part to get wrong
   early. *Check: kill a relay mid-transfer; the pair is back on another
   one, and the application above is told the stream died.*
2. **The shaper, on one relay.** A shaped host, one bucket per subject,
   live `SETLIMIT`. *Check: two machines on a 10 MB/s subject, one
   behind a 1 MB/s link, measure 9 and 1 — not 5 and 1. A limit changed
   during a transfer takes effect during it.*
3. **The allowance loop.** Demand reporting, max-min allocation, push.
   *Check: the same two machines on two different relays measure the
   same 9 and 1, within a second of each other starting. Kill bifrost
   mid-transfer and both keep running at their last allowance.*
4. **Metering.** Per-subject counters, Prometheus, the usage endpoint.
5. **Autoscaling and rebalancing.** `Provisioner`, the scaler, the
   packing rule, drain-before-destroy. *Check: demand crosses a
   threshold and a VPS appears; a relay at its ceiling sheds its idle
   circuits first; demand falls and, fifteen minutes later, the emptiest
   relay drains itself and goes — never below the minimum, never above
   the maximum, and never taking a live transfer with it.*

Steps 1 to 3 are the requirement. Steps 4 and 5 are what make it cost
what it should. Step 3 is the one to be suspicious of: it is the only
part with a control loop in the data path's way, and the check that
matters is the one where bifrost dies.
