package main

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/peer"
)

// testFleet builds a coordinator with the default knobs and its state in
// a temporary directory.
func testFleet(t *testing.T) *fleet {
	t.Helper()
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newFleet(st, knobs{ttl: defaultTTL, headroom: defaultHeadroom, packTo: defaultPackTo})
}

// join registers a relay that discards whatever is pushed at it.
func join(f *fleet, name string, bandwidth int64) *relayNode {
	id := peer.ID(name)
	return f.register(id, wire.Register{Addrs: []string{"/relay/" + name}, Bandwidth: bandwidth}, json.NewEncoder(io.Discard))
}

// machine adds a subject with one machine on it and returns that
// machine's id.
func machine(t *testing.T, f *fleet, name string, min int64) peer.ID {
	t.Helper()
	id := peer.ID(name)
	if err := f.store.set(name, min, min*10); err != nil {
		t.Fatal(err)
	}
	if err := f.store.setDevices(name, []string{id.String()}); err != nil {
		t.Fatal(err)
	}
	return id
}

// A relay's ceiling is its bandwidth after headroom and packing, and it
// is the number the whole capacity question turns on.
func TestCeiling(t *testing.T) {
	f := testFleet(t)
	n := join(f, "a", 1000)
	if got := f.ceiling(n); got != 595 {
		t.Fatalf("ceiling of a 1000 byte/s relay = %d, want 595 (1000 * 0.70 * 0.85)", got)
	}
}

// Placement fills the fullest relay that still has room, so the other
// one stays empty enough to be destroyed.
func TestPlacementPacksTight(t *testing.T) {
	f := testFleet(t)
	a := join(f, "a", 1000) // ceiling 595
	join(f, "b", 1000)

	// Something already on a, so the two relays are not tied.
	f.placed[peer.ID("seed")] = &placement{relay: a.id, min: 200, expires: time.Now().Add(time.Hour)}

	first := machine(t, f, "first", 300)
	if got := f.lease(first); len(got.Relay) != 1 || got.Relay[0] != "/relay/a" {
		t.Fatalf("300 byte/s machine landed on %v, want the fuller relay a", got.Relay)
	}

	// a now holds 500 of 595, so this one cannot fit there.
	second := machine(t, f, "second", 400)
	if got := f.lease(second); len(got.Relay) != 1 || got.Relay[0] != "/relay/b" {
		t.Fatalf("400 byte/s machine landed on %v, want b: a has no room left", got.Relay)
	}

	// Neither relay has 400 left now. Refusing is the answer, not
	// overselling the machines already placed.
	third := machine(t, f, "third", 400)
	if got := f.lease(third); len(got.Relay) != 0 {
		t.Fatalf("got %v, want no relay: the fleet is full", got.Relay)
	}
}

// A renewal must not move a working machine: a move costs it every live
// connection through that relay.
func TestLeaseRenewalStays(t *testing.T) {
	f := testFleet(t)
	join(f, "a", 1000)
	join(f, "b", 1000)

	who := machine(t, f, "who", 100)
	first := f.lease(who)
	if len(first.Relay) != 1 {
		t.Fatal("no relay placed")
	}
	for range 5 {
		if got := f.lease(who); got.Relay[0] != first.Relay[0] {
			t.Fatalf("renewal moved the machine from %s to %s", first.Relay[0], got.Relay[0])
		}
	}
	if len(f.placed) != 1 {
		t.Fatalf("%d placements for one machine, want 1", len(f.placed))
	}
}

// A machine that stops renewing must give its share back, or a laptop
// switched off holds bandwidth for as long as the coordinator runs.
func TestExpiryFreesRoom(t *testing.T) {
	f := testFleet(t)
	join(f, "a", 1000)

	who := machine(t, f, "who", 500)
	if got := f.lease(who); len(got.Relay) != 1 {
		t.Fatal("no relay placed")
	}
	if got := f.committed(peer.ID("a")); got != 500 {
		t.Fatalf("committed %d, want 500", got)
	}

	f.expire(time.Now().Add(defaultTTL + time.Second))
	if got := f.committed(peer.ID("a")); got != 0 {
		t.Fatalf("committed %d after the lease ran out, want 0", got)
	}
}

// A machine nobody told the coordinator about gets no relay. It still
// runs: the local network and a direct connection need no relay at all.
func TestUnknownMachineGetsNoRelay(t *testing.T) {
	f := testFleet(t)
	join(f, "a", 1000)

	if got := f.lease(peer.ID("stranger")); len(got.Relay) != 0 {
		t.Fatalf("got %v, want no relay for a machine that belongs to no subject", got.Relay)
	}
	if len(f.placed) != 0 {
		t.Fatal("a stranger took a placement")
	}
}

// A relay that restarts keeps its identity, so the machines still
// pointed at it must be admitted again without waiting for a lease.
func TestRegisterReadmits(t *testing.T) {
	f := testFleet(t)
	join(f, "a", 1000)
	who := machine(t, f, "who", 100)
	f.lease(who)

	var pushed []wire.Command
	f.register(peer.ID("a"), wire.Register{Addrs: []string{"/relay/a"}, Bandwidth: 1000}, json.NewEncoder(recorder{&pushed}))
	if len(pushed) != 1 || pushed[0].Op != wire.OpAdmit || pushed[0].Peer != who.String() {
		t.Fatalf("after a restart the relay was pushed %v, want one admit for %s", pushed, who.String())
	}
}

// recorder decodes whatever is encoded into it, so a test can read the
// commands a relay would have been sent.
type recorder struct{ into *[]wire.Command }

func (r recorder) Write(b []byte) (int, error) {
	var c wire.Command
	if err := json.Unmarshal(b, &c); err != nil {
		return 0, err
	}
	*r.into = append(*r.into, c)
	return len(b), nil
}

// A relay that reconnects registers again before the old stream's reader
// notices it died. The late drop must not take the new registration, or
// the relay disappears the moment it comes back.
func TestReconnectSurvivesTheOldStreamsDrop(t *testing.T) {
	f := testFleet(t)
	old := join(f, "a", 1000)
	who := machine(t, f, "alice", 100)
	if got := f.lease(who); len(got.Relay) == 0 {
		t.Fatal("the machine was not placed to begin with")
	}

	fresh := join(f, "a", 1000) // the same relay, a new stream
	f.drop(old)                 // the old reader finally notices

	if f.relays[fresh.id] != fresh {
		t.Fatal("the reconnected relay was dropped by the old stream")
	}
	if _, ok := f.placed[who]; !ok {
		t.Fatal("the machines on the reconnected relay were dropped with it")
	}
}

// Raising a floor while a machine sits on a relay must count against
// that relay, or the coordinator oversells what it already placed.
func TestRenewalRereadsTheFloor(t *testing.T) {
	f := testFleet(t)
	n := join(f, "a", 1000) // ceiling 595
	who := machine(t, f, "alice", 100)
	f.lease(who)

	if err := f.store.set("alice", 500, 5000); err != nil {
		t.Fatal(err)
	}
	f.lease(who)
	if got := f.committed(n.id); got != 500 {
		t.Fatalf("relay committed %d after the floor was raised to 500", got)
	}
}
