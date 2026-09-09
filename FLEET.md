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

The requirement: a subject has an aggregate rate; it divides equally
between that subject's machines that are actually transferring; a floor
is guaranteed; a ceiling is enforced; and all of it can change while a
transfer runs.

### 4.1 Subject affinity is what makes it possible

**Every machine of a subject is placed on the same relay.** Placement is
per subject, not per machine.

Only a relay that carries all of a subject's traffic can enforce that
subject's aggregate. Split a subject across three relays and no one has
the whole picture, and you are building a distributed rate allocator
with all the failure modes that implies. Affinity turns the whole
problem into arithmetic on one machine.

The price: **a subject's `max` can never exceed one relay's usable
capacity.** At 10 MB/s against ~35 MB/s usable (§6) there is room to
spare, but it is a real ceiling and it belongs written down.

### 4.2 The shaper

For each subject S with current rate R:

```text
parent      one rate.Limiter(R), shared by every circuit of S
child       one rate.Limiter(R / active(S)) per circuit
a byte passes only when the child allows it and the parent allows it
```

`active(S)` is the number of S's circuits that moved at least one byte
in the last 5 seconds, recomputed on a 1-second tick. A machine that is
connected but idle does not take a share — which is what "if only 2 out
of 5 machines are using it" asks for.

The parent alone would divide the rate in proportion to demand; the
child is what makes the division *equal*. Five active machines on a
10 MB/s subject get 2 MB/s each; when three stop, the survivors are at
5 MB/s within a second.

`min` is **not** a limiter. You cannot guarantee a floor by throttling —
you guarantee it by not overselling, which makes `min` the currency of
placement and of scaling (§6). `max` is the ceiling the limiters are set
from.

**Changing a limit is a push, not a reconnect.** Bifrost sends
`SETLIMIT{subject, min, max}` and the relay calls `SetLimit` on the
parent and recomputes the children. `x/time/rate` supports this at
runtime; it takes effect within one refill interval. An upgrade applies
mid-transfer, and a downgrade does too.

### 4.3 Where this goes in the code, and why it is a fork

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

So: **vendor `circuitv2/relay` into `internal/relay` and add one hook** —
a `Shaper` that, given `(src, dest)`, returns the two limiters and a
byte counter. About forty lines changed in six hundred and fifty. Open
it upstream as `WithShaper` at the same time, so the fork has somewhere
to go instead of rotting.

Rejected, and why:

- **`tc`/HTB on the VPS.** Shapes by IP address. Several of a subject's
  machines can sit behind one NAT and share an IP, and one subject spans
  many IPs. Wrong key entirely.
- **One relay process per subject.** Multiplies ports, reservations and
  memory by the subject count to avoid forty lines.
- **`RelayLimit.Data` alone.** A monthly quota wearing a rate's clothes.
  It cannot answer "10 MB/s".

---

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

### 6.2 The loop

Every 30 seconds:

- **Up**, immediately, when `committed > 0.85 × capacity`, or when a
  placement fails for want of room. 0.85 rather than 1.0 because a fresh
  VPS takes 30–90 seconds to boot and register, and that time has to
  come from somewhere.
- **Down**, only when `committed < 0.6 × capacity` has held for fifteen
  minutes. Relay churn costs reconnections for real users; a machine
  kept for an extra quarter hour costs pennies.
- Never below `minVPS` — the requirement is two, so that at least one
  relay survives any single loss.
- Never above `maxVPS`. At the cap, placement refuses new subjects
  rather than overselling the ones already placed. Refusing is honest;
  degrading everybody is not.

### 6.3 Never destroy a relay that is carrying traffic

```text
draining  -> bifrost stops placing on it and tells its subjects to re-lease
          -> agents move within one lease period
          -> zero reservations, or five minutes, whichever comes first
destroy   -> provider API
```

A relay that is destroyed while carrying traffic is indistinguishable
from a relay that crashed, and while §3 handles that correctly, doing it
on purpose several times an hour is not "seamless".

### 6.4 The provisioner interface

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

### 6.5 What the interface refuses to model

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

**`internal/transport/transport.go`** — `New(key, relays []string)`
becomes `New(Config{Key, Coordinator, NoLAN})`. The `circuits` slice,
built once at startup, becomes a set behind a mutex that the lease loop
replaces. `Addrs()` changes when the relay changes, so the peer has to
be told: bifrost knows both ends and carries it, and
`/ratatoskr/addrs/1.0.0` covers the case where some connection survives.

**`internal/transport/upgrade.go`** — split `restore()`. The 30-second
window stays for direct addresses. Relay acquisition moves to a
host-level lease loop that never gives up. `redial()` reads the
refreshed circuit set instead of the frozen one.

**`cmd/heimdall/main.go`** — register with bifrost and heartbeat; accept
`ADMIT`, `SETLIMIT` and `DRAIN`; back `ACLFilter` with the pushed table,
failing closed; replace `relay.DefaultResources()`'s single global limit
with the vendored hop and its shaper; report per-subject counters.

**`internal/config/config.go`** — `relays []string` becomes
`coordinator`. Keep a manual relay override for single-relay
development; it is two lines and it keeps `make build` useful without a
fleet.

**`cmd/bifrost/`** — new. Go, a libp2p host for agents and relays, plain
HTTP+JSON for the application's admin calls, and SQLite
(`modernc.org/sqlite`, no cgo) for state. The dataset is subjects,
devices, relays and placements; it stays small for a long time. Move to
Postgres when bifrost needs a second instance, not before.

---

## 9. Build it in this order

Risk first, and the part that costs money last.

1. **Bifrost with a static relay list.** Leases, placement, ACL push,
   the agent's endless re-lease loop. No shaping, no scaling. This is
   the part that touches every agent, so it is the part to get wrong
   early. *Check: kill a relay mid-transfer; the pair is back on another
   one, and the application above is told the stream died.*
2. **The shaper.** Vendored hop, parent and child limiters, live
   `SETLIMIT`. *Check: five machines on one subject measure 1/5 each;
   three stop and the rest measure 1/2 within a second; a limit changed
   during a transfer takes effect during it.*
3. **Metering.** Per-subject counters, Prometheus, the usage endpoint.
4. **Autoscaling.** `Provisioner`, the loop, drain-before-destroy.
   *Check: committed demand crosses a threshold and a VPS appears; it
   falls and, fifteen minutes later, one drains and goes — never below
   the minimum, never above the maximum.*

Steps 1 and 2 are the requirement. Steps 3 and 4 are what make it cost
what it should.
