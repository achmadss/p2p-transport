package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// relaySet is the relays this machine may use right now.
//
// It is a set that can be replaced rather than a list built at startup,
// because a coordinator can move this machine to another relay at any
// moment. Everything that reaches for a relay — the last rung of the
// dial ladder, the reservation that makes this machine reachable, the
// address a peer is given — reads it here, so a move is seen by all of
// them at once.
type relaySet struct {
	mu       sync.Mutex
	infos    []peer.AddrInfo
	circuits []multiaddr.Multiaddr

	// changed wakes whoever is holding the relays open, and lost wakes
	// the lease loop when a relay this machine was using has gone.
	// Buffered by one and never blocked on: a reader that has not caught
	// up does not need telling twice.
	changed chan struct{}
	lost    chan struct{}
}

func newRelaySet() *relaySet {
	return &relaySet{changed: make(chan struct{}, 1), lost: make(chan struct{}, 1)}
}

// poke wakes a channel without ever blocking on it.
func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// gone says a machine has disconnected, and wakes the lease loop if it
// was one of the relays.
//
// A relay that dies is the one case where waiting for the next renewal
// is too slow: this machine is unreachable from outside until it has
// another one, and the coordinator already knows the relay is gone.
func (r *relaySet) gone(id peer.ID) {
	r.mu.Lock()
	var mine bool
	for _, info := range r.infos {
		if info.ID == id {
			mine = true
			break
		}
	}
	r.mu.Unlock()
	if mine {
		poke(r.lost)
	}
}

// set replaces the relays, and reports whether they actually differ.
func (r *relaySet) set(infos []peer.AddrInfo) bool {
	circuits := make([]multiaddr.Multiaddr, 0, len(infos))
	for _, info := range infos {
		for _, a := range p2pAddrs(info) {
			// The suffix cannot break an address that already parsed.
			if ma, err := multiaddr.NewMultiaddr(a + "/p2p-circuit"); err == nil {
				circuits = append(circuits, ma)
			}
		}
	}

	r.mu.Lock()
	same := render(r.circuits) == render(circuits)
	if !same {
		r.infos, r.circuits = infos, circuits
	}
	r.mu.Unlock()
	if same {
		return false
	}

	poke(r.changed)
	return true
}

// source is what the reachability machinery asks for relay candidates.
// It is read on every call rather than fixed at startup, so a machine
// the coordinator moves reserves with the relay it was moved to.
//
// The channel is buffered and closed before it is returned, which is
// what the caller expects: it will not ask again until this one closes.
func (r *relaySet) source(_ context.Context, num int) <-chan peer.AddrInfo {
	infos := r.peers()
	if len(infos) > num {
		infos = infos[:num]
	}
	ch := make(chan peer.AddrInfo, len(infos))
	for _, info := range infos {
		ch <- info
	}
	close(ch)
	return ch
}

func (r *relaySet) peers() []peer.AddrInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(r.infos[:0:0], r.infos...)
}

// dialAddrs is what to dial to reach a peer through a relay.
func (r *relaySet) dialAddrs() []multiaddr.Multiaddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(r.circuits[:0:0], r.circuits...)
}

func render(as []multiaddr.Multiaddr) string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return strings.Join(out, " ")
}

// mergeInfos folds several addresses for one machine into a single
// record, so a coordinator listed twice — once for each transport it
// answers on — is dialled as one machine with two ways in rather than
// two machines.
func mergeInfos(infos []peer.AddrInfo) (peer.AddrInfo, error) {
	out := peer.AddrInfo{ID: infos[0].ID}
	for _, info := range infos {
		if info.ID != out.ID {
			return peer.AddrInfo{}, fmt.Errorf("addresses name two different machines, %s and %s",
				identity.Short(out.ID.String()), identity.Short(info.ID.String()))
		}
		out.Addrs = append(out.Addrs, info.Addrs...)
	}
	return out, nil
}

// leaseTTLFallback is how long a lease lasts when the coordinator did
// not say. It only matters against a coordinator that answers with
// nothing useful, and asking again in two minutes beats asking in a
// tight loop.
const leaseTTLFallback = 2 * time.Minute

// lease keeps asking the coordinator where this machine should relay,
// for as long as the host runs. It renews at half the lease, so one
// lost answer is not a lost relay.
//
// It never gives up and it never tears anything down. A coordinator
// that has gone quiet leaves this machine on the relay it already has,
// past the lease's end, because a working path is worth more than an
// expired promise; what stops is moving to a better one.
func (t *Host) lease(coord peer.AddrInfo) {
	const (
		firstWait = time.Second
		maxWait   = 30 * time.Second
	)
	wait := firstWait
	down := false
	for {
		next := wait
		if got, err := t.askLease(coord); err != nil {
			if !down {
				fmt.Fprintf(os.Stderr, "coordinator unreachable, keeping the relay this machine has: %v\n", err)
				down = true
			}
			if wait *= 2; wait > maxWait {
				wait = maxWait
			}
		} else {
			if down {
				fmt.Fprintln(os.Stderr, "coordinator is back")
				down = false
			}
			wait = firstWait
			next = got
		}
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(next):
		case <-t.relays.lost:
			// The relay this machine was on has gone. Ask now rather
			// than at the next renewal: until another one is placed,
			// nothing outside this network can reach here.
			wait = firstWait
		}
	}
}

// askLease asks once and applies the answer, returning how long to wait
// before asking again.
//
// An answer naming no relay is an answer, not a failure: this machine
// belongs to no subject, or the fleet has no room. It keeps running on
// the local network and on whatever it can dial directly, which is a
// complete way to run.
func (t *Host) askLease(coord peer.AddrInfo) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()
	if err := t.h.Connect(ctx, coord); err != nil {
		return 0, err
	}
	s, err := t.h.NewStream(ctx, coord.ID, wire.LeaseProto)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))

	var got wire.Lease
	if err := json.NewDecoder(io.LimitReader(s, 1<<16)).Decode(&got); err != nil {
		return 0, err
	}
	infos, err := parseAddrs(got.Relay)
	if err != nil {
		return 0, err
	}
	if t.relays.set(infos) {
		switch len(infos) {
		case 0:
			fmt.Fprintln(os.Stderr, "no relay for this machine; running without one")
		default:
			fmt.Fprintf(os.Stderr, "relaying through %s\n", identity.Short(infos[0].ID.String()))
		}
	}

	ttl := got.TTL
	if ttl <= 0 {
		ttl = leaseTTLFallback
	}
	return ttl / 2, nil
}
