package main

import (
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/peer"
)

// on is the relay a machine is placed on, and whether it is placed at
// all.
func on(f *fleet, who peer.ID) (peer.ID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.placed[who]
	if !ok {
		return "", false
	}
	return p.relay, true
}

// A relay that is draining is on its way out, so nothing new goes on it.
// Without this the fleet can never shrink: a relay emptied by renewals
// fills straight back up.
func TestNothingNewLandsOnADrainingRelay(t *testing.T) {
	f := testFleet(t)
	a, b := join(f, "a", 1<<30), join(f, "b", 1<<30)
	f.drain(a.id, true)

	for _, name := range []string{"alice", "bob", "carol"} {
		who := machine(t, f, name, 1<<20)
		f.lease(who)
		if got, ok := on(f, who); !ok || got != b.id {
			t.Fatalf("%s was placed on %q, want the relay that is not draining", name, got)
		}
	}
}

// A machine moves off a draining relay at its own next renewal. That is
// the whole of the move, and it costs nothing: the circuits already open
// through the old relay keep running until they end.
func TestARenewalMovesOffADrainingRelay(t *testing.T) {
	f := testFleet(t)
	a, b := join(f, "a", 1<<30), join(f, "b", 1<<30)
	who := machine(t, f, "alice", 1<<20)

	f.lease(who)
	first, ok := on(f, who)
	if !ok {
		t.Fatal("the machine was never placed")
	}
	want := a.id
	if first == a.id {
		want = b.id
	}

	f.drain(first, true)
	f.lease(who)
	if got, _ := on(f, who); got != want {
		t.Fatalf("after draining %q the machine is on %q, want %q", first, got, want)
	}
}

// Emptying a relay is worth waiting for. Leaving a machine with no relay
// at all is not: it is unreachable from outside until it has one, and
// that is a worse outcome than a VPS staying alive another quarter hour.
func TestADrainingRelayKeepsWhatItHasWhenNowhereElseFits(t *testing.T) {
	f := testFleet(t)
	only := join(f, "only", 1<<30)
	who := machine(t, f, "alice", 1<<20)

	f.lease(who)
	f.drain(only.id, true)

	got := f.lease(who)
	if len(got.Relay) == 0 {
		t.Fatal("draining the last relay left the machine with none, which is worse than keeping the relay")
	}
	if r, _ := on(f, who); r != only.id {
		t.Fatalf("the machine is on %q, want the draining relay it had nowhere to leave for", r)
	}
}

// The listing is how an operator knows a relay may be destroyed:
// draining, nothing placed on it, and nothing having moved for a while.
func TestStatusSaysWhenADrainedRelayIsFinished(t *testing.T) {
	f := testFleet(t)
	join(f, "a", 1<<30)
	join(f, "b", 1<<30)
	who := machine(t, f, "alice", 1<<20)
	f.lease(who)
	first, _ := on(f, who)
	f.drain(first, true)
	f.lease(who)

	var seen bool
	for _, s := range f.status(time.Now()) {
		if s.ID != first.String() {
			continue
		}
		seen = true
		if !s.Draining || s.Machines != 0 || s.Committed != 0 {
			t.Fatalf("the drained relay reads %+v, want draining with nothing on it", s)
		}
	}
	if !seen {
		t.Fatal("the drained relay is missing from the listing")
	}
	if got := f.status(time.Now()); len(got) != 2 {
		t.Fatalf("the listing has %d relays, want both of them", len(got))
	}
}

// A heartbeat says the relay is alive. It must not say a circuit is
// still open, or a drained relay would never look finished and would
// never be destroyed.
func TestOnlyMovedBytesMarkARelayBusy(t *testing.T) {
	f := testFleet(t)
	n := join(f, "a", 1<<30)

	f.mu.Lock()
	n.busy = time.Now().Add(-time.Minute)
	f.mu.Unlock()

	// An empty report, and one naming a subject that moved nothing. A
	// relay only ever sends the first, because the shaper leaves out a
	// subject with nothing to say; the second is what stops that being
	// the only reason this holds.
	for _, d := range []wire.Demand{
		{Period: time.Second},
		{Period: time.Second, Reports: []wire.Report{{Subject: "alice", Used: 0}}},
	} {
		f.report(n.id, d)
		if got := f.status(time.Now())[0].IdleFor; got < 30 {
			t.Fatalf("a report of %v reset the idle clock to %.0fs, want it left alone", d.Reports, got)
		}
	}

	f.report(n.id, wire.Demand{Period: time.Second, Reports: []wire.Report{{Subject: "alice", Used: 1}}})
	if got := f.status(time.Now())[0].IdleFor; got > 5 {
		t.Fatalf("a report that moved bytes left the relay idle for %.0fs, want it marked busy", got)
	}
}

// Dropping a relay evicts every machine on it, so the silence that means
// "gone" must be long enough that a relay having a slow moment survives.
func TestSilenceAllowedIsThreePeriodsAndNeverTooTight(t *testing.T) {
	for _, c := range []struct {
		period time.Duration
		want   time.Duration
	}{
		{time.Second, heartbeatFloor},        // 3s is far too tight to act on
		{10 * time.Second, 30 * time.Second}, // three periods, comfortably over the floor
		{0, heartbeatFloor},                  // a relay that named no period at all
		{time.Hour, 3 * maxPeriod},           // and one that named an absurd one
	} {
		if got := silentFor(c.period); got != c.want {
			t.Fatalf("a relay reporting every %s may go quiet for %s, want %s", c.period, got, c.want)
		}
	}
}
