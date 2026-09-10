package main

import (
	"crypto/rand"
	"testing"

	"github.com/achmadss/p2p-transport/internal/shape"
	"github.com/achmadss/p2p-transport/internal/wire"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// The access list fails closed. A machine that learned this relay's
// address some other way must not be able to use it.
func TestAccessListFailsClosed(t *testing.T) {
	a := newAdmitted(shape.New(0))
	stranger := peer.ID("stranger")

	if a.AllowReserve(stranger, nil) {
		t.Fatal("an unplaced machine was allowed to reserve")
	}
	if a.AllowConnect(stranger, nil, stranger) {
		t.Fatal("an unplaced machine was allowed to be reached")
	}
}

// Admit and revoke are the two things the coordinator pushes, and a
// revoked machine must lose the relay it was placed on.
func TestAdmitAndRevoke(t *testing.T) {
	a := newAdmitted(shape.New(0))
	placed, other := peer.ID("placed"), peer.ID("other")

	a.allow(placed, true)
	if !a.AllowReserve(placed, nil) {
		t.Fatal("an admitted machine was refused a reservation")
	}
	// The source is whoever the destination chose to talk to; only the
	// destination has to be placed here.
	if !a.AllowConnect(other, nil, placed) {
		t.Fatal("a connection to an admitted machine was refused")
	}
	if a.AllowConnect(placed, nil, other) {
		t.Fatal("a connection to an unplaced machine was allowed")
	}

	a.allow(placed, false)
	if a.AllowReserve(placed, nil) {
		t.Fatal("a revoked machine kept its reservation")
	}
}

// newPeerID makes a machine id the wire can carry, since a command
// names a machine as text and the relay decodes it back.
func newPeerID(t *testing.T) peer.ID {
	t.Helper()
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// One push carries admission and rate together, and a revoke takes both
// away. Splitting them would let a machine relay at whatever rate it
// was last given, or be admitted with no rate at all.
func TestApplyCarriesTheRate(t *testing.T) {
	limits := shape.New(0)
	a := newAdmitted(limits)
	who := newPeerID(t)

	a.apply(wire.Command{Op: wire.OpAdmit, Peer: who.String(), Subject: "alice", Rate: 1000})
	if !a.AllowReserve(who, nil) {
		t.Fatal("an admitted machine was refused")
	}
	if got := limits.RateFor(who); got != 1000 {
		t.Fatalf("shaping %s at %v, want 1000", who, got)
	}

	a.apply(wire.Command{Op: wire.OpSetLimit, Subject: "alice", Rate: 2000})
	if got := limits.RateFor(who); got != 2000 {
		t.Fatalf("after a rate change, shaping at %v, want 2000", got)
	}

	a.apply(wire.Command{Op: wire.OpRevoke, Peer: who.String()})
	if a.AllowReserve(who, nil) {
		t.Fatal("a revoked machine kept its place")
	}
	if got := limits.RateFor(who); got != 0 {
		t.Fatalf("a revoked machine is still shaped at %v, want nothing", got)
	}
}
