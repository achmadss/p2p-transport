package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// knobs are the numbers that decide how many machines fit on a relay
// and how long a placement lasts. Every one is read from the
// environment with a working default; knobsFromEnv names them.
type knobs struct {
	// ttl is how long a placement is promised for. The agent renews at
	// half of it, so one lost renewal is not a lost relay.
	ttl time.Duration

	// headroom and packTo are how full a relay is allowed to get, as
	// fractions of what the provider sells.
	//
	// A relayed byte crosses the machine twice, once in and once out, so
	// a relay never forwards its sticker rate; headroom is that plus
	// slack. packTo is where placement stops: the gap above it is what a
	// quiet subject bursts into, so packing to the ceiling would mean
	// nobody ever exceeds their floor.
	headroom float64
	packTo   float64
}

// Defaults, and what each one costs to change. Raising headroom or
// packTo fits more machines on a relay and leaves less room for anyone
// to burst into; lowering them does the opposite and costs money.
const (
	defaultTTL      = 120 * time.Second
	defaultHeadroom = 0.70
	defaultPackTo   = 0.85
)

func knobsFromEnv() knobs {
	return knobs{
		ttl:      config.Duration("BIFROST_LEASE_TTL", defaultTTL),
		headroom: config.Float("BIFROST_HEADROOM", defaultHeadroom),
		packTo:   config.Float("BIFROST_PACK_TO", defaultPackTo),
	}
}

// A relayNode is one relay that has registered and is still connected.
type relayNode struct {
	id        peer.ID
	addrs     []string
	bandwidth int64

	// draining and busy are read and written under the fleet's lock,
	// unlike the fields above, which never change after registration.
	//
	// draining stops new machines landing here so the relay can be
	// emptied and destroyed. busy is the last time a report said
	// something actually moved, which is what says an emptied relay is
	// finished rather than merely unplaced: a circuit opened before the
	// machines moved keeps running until it ends.
	draining bool
	busy     time.Time

	// enc writes commands down the relay's own stream. One writer at a
	// time, hence the lock: two goroutines encoding into one stream
	// interleave two half JSON objects.
	mu  sync.Mutex
	enc *json.Encoder
}

func (n *relayNode) push(c wire.Command) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.enc.Encode(c)
}

// A placement is one machine on one relay, until it stops renewing.
type placement struct {
	relay   peer.ID
	subject string
	min     int64
	expires time.Time
}

// fleet is the registry: which relays exist, and which machine sits on
// which. It is the whole of the coordinator's live state, and none of it
// is written down — a relay re-registers and an agent re-leases, so a
// restart costs one round of both rather than a restore.
type fleet struct {
	store *store
	meter *meter
	knobs knobs

	mu     sync.Mutex
	relays map[peer.ID]*relayNode
	placed map[peer.ID]*placement

	// shares is how much of each subject's rate each relay carrying it
	// may use. One subject on two relays has two, and they are divided
	// and re-divided by the allowance loop.
	shares map[shareKey]*share
}

func newFleet(s *store, m *meter, k knobs) *fleet {
	return &fleet{
		store:  s,
		meter:  m,
		knobs:  k,
		relays: map[peer.ID]*relayNode{},
		placed: map[peer.ID]*placement{},
		shares: map[shareKey]*share{},
	}
}

// committed is what a relay has promised, in bytes per second. The
// caller holds the lock.
func (f *fleet) committed(id peer.ID) int64 {
	var total int64
	for _, p := range f.placed {
		if p.relay == id {
			total += p.min
		}
	}
	return total
}

// ceiling is the committed load a relay will accept.
func (f *fleet) ceiling(n *relayNode) int64 {
	return int64(float64(n.bandwidth) * f.knobs.headroom * f.knobs.packTo)
}

// pick chooses the fullest relay that still has room for min.
//
// Fullest, not emptiest, and that is the opposite of load balancing on
// purpose: spreading every machine evenly leaves every relay half used
// and none of them ever removable. Packing tight leaves one relay with
// room, which is the one that can be drained and destroyed when demand
// falls. The caller holds the lock.
func (f *fleet) pick(min int64) *relayNode {
	var best *relayNode
	var bestLoad int64
	for id, n := range f.relays {
		if n.draining {
			continue
		}
		load := f.committed(id)
		if load+min > f.ceiling(n) {
			continue
		}
		if best == nil || load > bestLoad {
			best, bestLoad = n, load
		}
	}
	return best
}

// lease answers one agent: which relay it should use, and for how long.
//
// A machine that belongs to no subject, and a fleet with no room, both
// get an empty answer. That is honest: the machine runs on the local
// network and on whatever it can dial directly, which is a complete way
// to run. Refusing beats placing a machine the fleet cannot carry.
func (f *fleet) lease(who peer.ID) wire.Lease {
	id, sub, ok := f.store.find(who.String())
	if !ok {
		return wire.Lease{TTL: f.knobs.ttl}
	}

	f.mu.Lock()
	// Renewal is the common case and must not move a working machine: a
	// move costs it every live connection through that relay. A machine
	// moved to another subject is not a renewal: it is placed again, so
	// the relay is told the subject it is now carrying.
	if p, ok := f.placed[who]; ok {
		// A draining relay is not renewed on, so the machine moves itself
		// at its next renewal and its open circuits keep running here
		// until they end. It is renewed on anyway when nowhere else has
		// room: emptying a relay is worth a wait, and never worth leaving
		// a machine with no relay at all.
		n, up := f.relays[p.relay]
		if up && p.subject == id && (!n.draining || f.pick(sub.Min) == nil) {
			// The floor is re-read, so raising it counts against the
			// relay's capacity without waiting for the machine to move.
			p.min = sub.Min
			p.expires = time.Now().Add(f.knobs.ttl)
			addrs := n.addrs
			f.mu.Unlock()
			return wire.Lease{Relay: addrs, TTL: f.knobs.ttl}
		}
		delete(f.placed, who)
		freed := f.forget(p.subject, p.relay)
		if freed {
			defer f.divide(p.subject)
		}
	}
	n := f.pick(sub.Min)
	if n == nil {
		f.mu.Unlock()
		return wire.Lease{TTL: f.knobs.ttl}
	}
	f.placed[who] = &placement{
		relay:   n.id,
		subject: id,
		min:     sub.Min,
		expires: time.Now().Add(f.knobs.ttl),
	}
	f.open(id, n.id, sub.Max)
	f.mu.Unlock()

	// Divided before the machine is admitted, so the relay is never told
	// a rate that the subject's other relays have not been taken out of.
	// This is what keeps the first period free of overshoot: the
	// coordinator knows the circuit is about to exist because it is the
	// one issuing the lease.
	f.divide(id)

	rate := f.allotted(id, n.id)
	if rate <= 0 {
		// The subject was removed between the lookup and the division.
		// The ceiling is wrong but survivable; a zero is not, because a
		// relay reads it as no limit at all.
		rate = sub.Max
	}

	// Admit before answering, or the agent races its own lease: it
	// would dial the relay and be refused by an access list that has
	// not heard of it yet.
	if err := n.push(wire.Command{
		Op:      wire.OpAdmit,
		Peer:    who.String(),
		Subject: id,
		Rate:    rate,
	}); err != nil {
		f.drop(n)
		return wire.Lease{TTL: f.knobs.ttl}
	}
	return wire.Lease{Relay: n.addrs, TTL: f.knobs.ttl}
}

// drop forgets a relay and everything placed on it. The machines find
// out by leasing again, which they do within half a TTL.
//
// It drops the node rather than the id, and does nothing if that node is
// no longer the registered one. A relay that reconnects registers again
// before the old stream's reader notices it died, and dropping by id
// would take the new registration and its placements with it.
func (f *fleet) drop(n *relayNode) {
	f.mu.Lock()
	if f.relays[n.id] != n {
		f.mu.Unlock()
		return
	}
	delete(f.relays, n.id)
	for who, p := range f.placed {
		if p.relay == n.id {
			delete(f.placed, who)
		}
	}
	// Its shares go with it, so what it was holding is divided among the
	// relays the subject still has.
	var freed []string
	for k := range f.shares {
		if k.relay == n.id {
			delete(f.shares, k)
			freed = append(freed, k.subject)
		}
	}
	f.mu.Unlock()

	for _, subject := range freed {
		f.divide(subject)
	}
}

// register records a relay and re-admits whatever was already placed on
// it. The re-admission is what makes a relay restart invisible: its
// identity survives, so the machines on it are still pointed here and
// would otherwise be refused by an empty access list.
func (f *fleet) register(id peer.ID, r wire.Register, enc *json.Encoder) *relayNode {
	n := &relayNode{id: id, addrs: r.Addrs, bandwidth: r.Bandwidth, enc: enc, busy: time.Now()}

	// A machine comes back at the share this relay was already holding,
	// not at its subject's whole rate: the subject's other relays have
	// not given anything up, and a restart must not be a way to be
	// allotted the rate twice.
	type readmit struct {
		who     string
		subject string
		rate    int64
	}

	// The store is read after the lock is dropped, so this never holds
	// the fleet's lock and the store's at once.
	f.mu.Lock()
	f.relays[id] = n
	var back []readmit
	for who, p := range f.placed {
		if p.relay != id {
			continue
		}
		r := readmit{who: who.String(), subject: p.subject}
		if s, ok := f.shares[shareKey{p.subject, id}]; ok {
			r.rate = s.given
		}
		back = append(back, r)
	}
	f.mu.Unlock()

	for _, m := range back {
		rate := m.rate
		if rate <= 0 {
			// No share yet, or one that was never pushed. The ceiling is
			// the only honest answer, and a zero is not one: a relay
			// reads it as no limit at all.
			_, sub, ok := f.store.subject(m.subject)
			if !ok {
				continue
			}
			rate = sub.Max
		}
		if err := n.push(wire.Command{
			Op:      wire.OpAdmit,
			Peer:    m.who,
			Subject: m.subject,
			Rate:    rate,
		}); err != nil {
			break
		}
	}
	return n
}

// expire drops placements nobody renewed and tells the relay to stop
// carrying them. Without it a machine that is switched off holds its
// share of a relay for as long as the coordinator runs.
func (f *fleet) expire(now time.Time) {
	type revocation struct {
		n   *relayNode
		who string
	}
	var out []revocation

	var freed []string
	f.mu.Lock()
	for who, p := range f.placed {
		if now.Before(p.expires) {
			continue
		}
		delete(f.placed, who)
		if n, ok := f.relays[p.relay]; ok {
			out = append(out, revocation{n, who.String()})
		}
		if f.forget(p.subject, p.relay) {
			freed = append(freed, p.subject)
		}
	}
	f.mu.Unlock()

	for _, subject := range freed {
		f.divide(subject)
	}
	for _, r := range out {
		if err := r.n.push(wire.Command{Op: wire.OpRevoke, Peer: r.who}); err != nil {
			f.drop(r.n)
		}
	}
}

// serve registers the two protocols and starts the expiry loop.
func (f *fleet) serve(h host.Host, done <-chan struct{}) {
	h.SetStreamHandler(wire.LeaseProto, f.handleLease)
	h.SetStreamHandler(wire.FleetProto, f.handleFleet)
	go func() {
		tick := time.NewTicker(f.knobs.ttl / 4)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				f.expire(now)
			}
		}
	}()
}

// handleLease answers an agent. The handshake already proved which
// machine is asking, so the stream carries no request at all.
func (f *fleet) handleLease(s network.Stream) {
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	json.NewEncoder(s).Encode(f.lease(s.Conn().RemotePeer()))
}

// handleFleet holds one relay's stream open for as long as the relay
// lives: it reads the registration, then pushes commands down it until
// the far end goes away.
func (f *fleet) handleFleet(s network.Stream) {
	defer s.Close()
	id := s.Conn().RemotePeer()

	// One decoder for the life of the stream. A second one would miss
	// whatever the first had already read past the registration.
	dec := json.NewDecoder(s)
	var reg wire.Register
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := dec.Decode(&reg); err != nil {
		return
	}
	if reg.Bandwidth <= 0 {
		fmt.Fprintf(os.Stderr, "relay %s registered with no bandwidth; ignoring it\n", id)
		return
	}
	s.SetReadDeadline(time.Time{})

	n := f.register(id, reg, json.NewEncoder(s))
	fmt.Fprintf(os.Stderr, "relay %s registered, %d bytes/s per direction\n", id, reg.Bandwidth)
	defer func() {
		f.drop(n)
		fmt.Fprintf(os.Stderr, "relay %s gone\n", id)
	}()

	// The relay now reports its demand up this stream, once a period,
	// and each report re-divides the subjects it names. It reports on an
	// idle period too, so the deadline below is what notices a relay
	// that has gone: a machine whose power is cut, or whose process is
	// wedged, never closes the stream and would otherwise be counted as
	// capacity until libp2p gave up on the connection.
	//
	// The first deadline is the most generous one there is, because the
	// relay has not yet said how often it means to report.
	beat := silentFor(maxPeriod)
	for {
		var d wire.Demand
		s.SetReadDeadline(time.Now().Add(beat))
		if err := dec.Decode(&d); err != nil {
			return
		}
		beat = silentFor(d.Period)
		f.report(id, d)
	}
}

// heartbeatFloor is the least time a relay is given to say something
// before it is treated as gone. Dropping a relay evicts every machine on
// it, so a relay that is merely slow for a moment must not look like one
// whose power was cut.
const heartbeatFloor = 15 * time.Second

// silentFor is how long a relay may say nothing: three of its own
// periods, and never less than the floor.
func silentFor(period time.Duration) time.Duration {
	return max(3*min(max(period, minPeriod), maxPeriod), heartbeatFloor)
}

// drain marks a relay as one to empty, or lets it take work again, and
// says whether it found the relay.
//
// A draining relay keeps every machine and every circuit it already has.
// What stops is new ones landing on it: each machine moves itself off at
// its next renewal, which costs nothing, because a circuit already open
// through this relay goes on running until it ends. When nothing is
// placed here any more and nothing has moved for a while, the relay is
// finished and can be destroyed.
func (f *fleet) drain(id peer.ID, yes bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.relays[id]
	if ok {
		n.draining = yes
	}
	return ok
}

// relayStatus is one relay as an operator sees it. Draining, with no
// machines and nothing moving, is the answer to "may I destroy this".
type relayStatus struct {
	ID        string   `json:"id"`
	Addrs     []string `json:"addrs"`
	Bandwidth int64    `json:"bandwidth"`
	Ceiling   int64    `json:"ceiling"`
	Committed int64    `json:"committed"`
	Machines  int      `json:"machines"`
	Draining  bool     `json:"draining"`
	IdleFor   float64  `json:"idle_for_seconds"`
}

// status is every registered relay, in no particular order: it is a list
// an operator reads, not one anything decides on.
func (f *fleet) status(now time.Time) []relayStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	machines := map[peer.ID]int{}
	for _, p := range f.placed {
		machines[p.relay]++
	}
	out := make([]relayStatus, 0, len(f.relays))
	for id, n := range f.relays {
		out = append(out, relayStatus{
			ID:        id.String(),
			Addrs:     n.addrs,
			Bandwidth: n.bandwidth,
			Ceiling:   f.ceiling(n),
			Committed: f.committed(id),
			Machines:  machines[id],
			Draining:  n.draining,
			IdleFor:   now.Sub(n.busy).Seconds(),
		})
	}
	return out
}
