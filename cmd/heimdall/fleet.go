package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/achmadss/p2p-transport/internal/shape"
	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// admitted is the set of machines the coordinator has allowed to relay
// here, and the relay's access list reads it.
//
// It exists only when a coordinator is configured. When one is, it fails
// closed: a machine that learned this relay's address some other way is
// refused, so the fleet's bandwidth goes to the subjects it was placed
// for and nobody else.
type admitted struct {
	mu  sync.RWMutex
	set map[peer.ID]bool

	// limits is where the same pushes land as rates. Kept beside the
	// access list because they arrive together and change together: a
	// machine is admitted at a rate, and revoked from both at once.
	limits *shape.Limits
}

func newAdmitted(limits *shape.Limits) *admitted {
	return &admitted{set: map[peer.ID]bool{}, limits: limits}
}

func (a *admitted) allow(id peer.ID, yes bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if yes {
		a.set[id] = true
	} else {
		delete(a.set, id)
	}
}

// apply carries out one instruction from the coordinator.
func (a *admitted) apply(c wire.Command) {
	if c.Op == wire.OpSetLimit {
		a.limits.SetLimit(c.Subject, c.Rate)
		return
	}
	id, err := peer.Decode(c.Peer)
	if err != nil {
		return
	}
	switch c.Op {
	case wire.OpAdmit:
		a.allow(id, true)
		a.limits.Admit(id, c.Subject, c.Rate)
	case wire.OpRevoke:
		a.allow(id, false)
		a.limits.Revoke(id)
	}
}

func (a *admitted) has(id peer.ID) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.set[id]
}

// AllowReserve decides who may hold a slot here.
func (a *admitted) AllowReserve(p peer.ID, _ multiaddr.Multiaddr) bool { return a.has(p) }

// AllowConnect decides who may be reached through this relay.
//
// Only the destination is checked. It is the machine that was placed
// here, whose subject the traffic belongs to, and whose reservation the
// circuit runs through. The source is whoever it chose to talk to, and
// deciding that is the destination's business, not the relay's.
func (a *admitted) AllowConnect(_ peer.ID, _ multiaddr.Multiaddr, dest peer.ID) bool {
	return a.has(dest)
}

// follow keeps one stream to the coordinator open for as long as the
// relay runs: it registers, then applies what is pushed down it.
//
// A lost coordinator is not an emergency. The access list is left
// exactly as it was, so every machine already placed here keeps
// relaying; what stops is new machines arriving. Clearing the list
// instead would turn a coordinator restart into an outage for everyone.
func (a *admitted) follow(ctx context.Context, h host.Host, coord peer.AddrInfo, bandwidth int64) {
	const (
		firstWait = time.Second
		maxWait   = 30 * time.Second
	)
	wait := firstWait
	down := false
	for {
		registered, err := a.session(ctx, h, coord, bandwidth)
		switch {
		case registered:
			// The backoff resets on a session that got as far as
			// registering, so a coordinator that restarts nightly does
			// not leave every relay waiting half a minute to come back.
			wait, down = firstWait, false
		case !down:
			fmt.Fprintf(os.Stderr, "coordinator unreachable, keeping the machines already here: %v\n", err)
			down = true
		}
		select {
		case <-ctx.Done():
			return
		// Jitter so a coordinator that restarts is not hit by every
		// relay in the fleet on the same tick.
		case <-time.After(wait + time.Duration(rand.Int64N(int64(wait/2)))):
		}
		if wait *= 2; wait > maxWait {
			wait = maxWait
		}
	}
}

// session runs one connection to the coordinator and returns when it
// ends, saying whether it got as far as registering. A clean end and a
// failure look the same from here: either way the answer is to open
// another one.
func (a *admitted) session(ctx context.Context, h host.Host, coord peer.AddrInfo, bandwidth int64) (bool, error) {
	dial, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := h.Connect(dial, coord); err != nil {
		return false, err
	}
	s, err := h.NewStream(dial, coord.ID, wire.FleetProto)
	if err != nil {
		return false, err
	}
	defer s.Close()

	// Sent fresh on every reconnection, so a relay that has learned a
	// new address since it started registers the address it has now.
	addrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if err != nil {
		return false, err
	}
	out := make([]string, 0, len(addrs))
	for _, ad := range addrs {
		out = append(out, ad.String())
	}
	if err := json.NewEncoder(s).Encode(wire.Register{Addrs: out, Bandwidth: bandwidth}); err != nil {
		return false, err
	}
	fmt.Fprintln(os.Stderr, "registered with the coordinator")

	dec := json.NewDecoder(s)
	for {
		var c wire.Command
		if err := dec.Decode(&c); err != nil {
			return true, err
		}
		a.apply(c)
	}
}
