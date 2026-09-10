package main

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// The access list fails closed. A machine that learned this relay's
// address some other way must not be able to use it.
func TestAccessListFailsClosed(t *testing.T) {
	a := newAdmitted()
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
	a := newAdmitted()
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
