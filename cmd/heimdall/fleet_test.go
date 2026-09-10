package main

import (
	"context"
	"crypto/rand"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/shape"
	"github.com/achmadss/p2p-transport/internal/wire"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
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

// The reporter is the relay's half of the allowance loop. If it stays
// quiet the coordinator has nothing to divide, and a subject spread over
// two relays keeps whatever each was given first.
//
// Real bytes over a real shaped host, because the number reported has to
// be the number the byte path counted, and only the byte path counts it.
func TestReporterSendsWhatCrossedTheRelay(t *testing.T) {
	const (
		perSecond = 1 << 20 // fast enough not to slow the test down
		payload   = 4 << 10
	)
	relay, sender := newTestHost(t), newTestHost(t)

	limits := shape.New(0)
	a := newAdmitted(limits)
	a.apply(wire.Command{Op: wire.OpAdmit, Peer: sender.ID().String(), Subject: "alice", Rate: perSecond})
	wrapped := shapedHost{Host: relay, limits: limits}

	// Heartbeats are counted and reports are queued, so a run of empty
	// periods can never push the report this test is waiting for out of
	// the channel.
	var beats atomic.Int64
	sent := make(chan wire.Demand, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go a.report(ctx, func(v any) error {
		d, ok := v.(wire.Demand)
		if !ok {
			return nil
		}
		if len(d.Reports) == 0 {
			beats.Add(1)
			return nil
		}
		select {
		case sent <- d:
		default:
		}
		return nil
	}, 50*time.Millisecond)

	// An idle relay still reports, with nothing in it. That empty report
	// is the heartbeat: it is the only way the coordinator can tell a
	// relay carrying nothing from one that has died.
	time.Sleep(200 * time.Millisecond)
	if beats.Load() == 0 {
		t.Fatal("an idle relay said nothing at all, so silence cannot mean it is gone")
	}
	select {
	case d := <-sent:
		t.Fatalf("an idle relay reported traffic: %v", d)
	default:
	}

	const proto = protocol.ID("/test/1.0.0")
	done := make(chan struct{})
	wrapped.SetStreamHandler(proto, func(s network.Stream) {
		defer s.Close()
		io.Copy(io.Discard, s)
		close(done)
	})
	if err := sender.Connect(ctx, peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := sender.NewStream(ctx, relay.ID(), proto)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(make([]byte, payload)); err != nil {
		t.Fatal(err)
	}
	s.CloseWrite()
	<-done

	var moved int64
	for moved < payload {
		select {
		case d := <-sent:
			if len(d.Reports) != 1 || d.Reports[0].Subject != "alice" {
				t.Fatalf("reported %v, want one line for alice", d.Reports)
			}
			if d.Period != 50*time.Millisecond {
				t.Fatalf("reported a period of %s, want the one it is reporting on", d.Period)
			}
			moved += d.Reports[0].Used
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d bytes were ever reported", moved, payload)
		}
	}
	if moved != payload {
		t.Fatalf("reported %d bytes for a %d byte transfer", moved, payload)
	}
}
