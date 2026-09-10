package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	knobs knobs

	mu     sync.Mutex
	relays map[peer.ID]*relayNode
	placed map[peer.ID]*placement
}

func newFleet(s *store, k knobs) *fleet {
	return &fleet{
		store:  s,
		knobs:  k,
		relays: map[peer.ID]*relayNode{},
		placed: map[peer.ID]*placement{},
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
		if n, up := f.relays[p.relay]; up && p.subject == id {
			// The floor is re-read, so raising it counts against the
			// relay's capacity without waiting for the machine to move.
			p.min = sub.Min
			p.expires = time.Now().Add(f.knobs.ttl)
			addrs := n.addrs
			f.mu.Unlock()
			return wire.Lease{Relay: addrs, TTL: f.knobs.ttl}
		}
		delete(f.placed, who)
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
	f.mu.Unlock()

	// Admit before answering, or the agent races its own lease: it
	// would dial the relay and be refused by an access list that has
	// not heard of it yet.
	if err := n.push(wire.Command{
		Op:      wire.OpAdmit,
		Peer:    who.String(),
		Subject: id,
		Rate:    sub.Max,
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
	defer f.mu.Unlock()
	if f.relays[n.id] != n {
		return
	}
	delete(f.relays, n.id)
	for who, p := range f.placed {
		if p.relay == n.id {
			delete(f.placed, who)
		}
	}
}

// register records a relay and re-admits whatever was already placed on
// it. The re-admission is what makes a relay restart invisible: its
// identity survives, so the machines on it are still pointed here and
// would otherwise be refused by an empty access list.
func (f *fleet) register(id peer.ID, r wire.Register, enc *json.Encoder) *relayNode {
	n := &relayNode{id: id, addrs: r.Addrs, bandwidth: r.Bandwidth, enc: enc}

	// The subjects are looked up after the lock is dropped, so this
	// never holds the fleet's lock and the store's at once.
	f.mu.Lock()
	f.relays[id] = n
	back := map[string]string{} // machine id -> subject id
	for who, p := range f.placed {
		if p.relay == id {
			back[who.String()] = p.subject
		}
	}
	f.mu.Unlock()

	for who, subject := range back {
		_, sub, ok := f.store.subject(subject)
		if !ok {
			continue
		}
		if err := n.push(wire.Command{
			Op:      wire.OpAdmit,
			Peer:    who,
			Subject: subject,
			Rate:    sub.Max,
		}); err != nil {
			break
		}
	}
	return n
}

// setLimit tells every relay carrying a subject that its rate has
// changed. The relays apply it to the transfers already running, so a
// plan changed while somebody is downloading changes that download.
//
// A relay that has gone is dropped rather than retried: the machines on
// it will be placed again, and they will carry the new rate with them.
func (f *fleet) setLimit(subject string, max int64) {
	f.mu.Lock()
	carrying := map[peer.ID]*relayNode{}
	for _, p := range f.placed {
		if p.subject != subject {
			continue
		}
		if n, ok := f.relays[p.relay]; ok {
			carrying[p.relay] = n
		}
	}
	f.mu.Unlock()

	for _, n := range carrying {
		if err := n.push(wire.Command{Op: wire.OpSetLimit, Subject: subject, Rate: max}); err != nil {
			f.drop(n)
		}
	}
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

	f.mu.Lock()
	for who, p := range f.placed {
		if now.Before(p.expires) {
			continue
		}
		delete(f.placed, who)
		if n, ok := f.relays[p.relay]; ok {
			out = append(out, revocation{n, who.String()})
		}
	}
	f.mu.Unlock()

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

	var reg wire.Register
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewDecoder(io.LimitReader(s, 1<<16)).Decode(&reg); err != nil {
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

	// The relay never sends again. This read is only how the stream's
	// death is noticed.
	io.Copy(io.Discard, s)
}
