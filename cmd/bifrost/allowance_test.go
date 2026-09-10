package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/peer"
)

// carry puts a subject on a relay the way a placement does, without
// going through pick, so a test can say which relay carries what.
func carry(f *fleet, subject string, relay peer.ID, max int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.placed[peer.ID(subject+"@"+string(relay))] = &placement{
		relay:   relay,
		subject: subject,
		expires: time.Now().Add(time.Hour),
	}
	f.open(subject, relay, max)
}

// joinRecording registers a relay and hands back everything pushed at it.
func joinRecording(f *fleet, name string, bandwidth int64) (peer.ID, *[]wire.Command) {
	var pushed []wire.Command
	id := peer.ID(name)
	f.register(id, wire.Register{Addrs: []string{"/relay/" + name}, Bandwidth: bandwidth}, json.NewEncoder(recorder{&pushed}))
	return id, &pushed
}

// last is the rate a relay was most recently told a subject may use.
func last(pushed *[]wire.Command, subject string) int64 {
	var rate int64
	for _, c := range *pushed {
		if c.Subject == subject && (c.Op == wire.OpSetLimit || c.Op == wire.OpAdmit) {
			rate = c.Rate
		}
	}
	return rate
}

// The worked example from the design. Ten megabytes a second, one
// machine behind a fast link and one behind a slow one: the answer is
// nine and one, not five and one. Anything else wastes what the slow
// machine cannot use.
func TestMaxMinDivision(t *testing.T) {
	got := allocate(10, []int64{100, 1})
	if got[0] != 9 || got[1] != 1 {
		t.Fatalf("divided 10 between demands of 100 and 1 as %v, want [9 1]", got)
	}
}

// Nobody is ever given more than there is, whatever they ask for, and
// nobody is ever given nothing — a rate of zero is how "no limit" is
// written on a relay.
func TestDivisionNeverOversellsOrStarves(t *testing.T) {
	for _, want := range [][]int64{
		{100, 100, 100},    // all greedy
		{1, 1, 1},          // all modest
		{1, 1, 1 << 30},    // one greedy
		{0, 0},             // nobody asking
		{1 << 40, 1 << 40}, // more than the fleet has
	} {
		got := allocate(1000, want)
		var sum int64
		for i, g := range got {
			if g < 1 {
				t.Fatalf("demands %v gave relay %d a rate of %d, which reads as no limit", want, i, g)
			}
			sum += g
		}
		if sum > 1000+int64(len(want)) {
			t.Fatalf("demands %v were given %d of 1000", want, sum)
		}
	}
}

// A subject's machines land on relays independently, so its rate has to
// be divided between them. The division must give the same answer the
// two machines would get from one bucket on one relay: placement changes
// how long the answer takes, never what it is.
func TestOneSubjectOnTwoRelaysIsDividedByDemand(t *testing.T) {
	const R = 10 << 20 // 10 MB/s
	f := testFleet(t)
	fast, fastPushed := joinRecording(f, "fast", 1<<30)
	slow, slowPushed := joinRecording(f, "slow", 1<<30)
	if err := f.store.set("alice", 1<<20, R); err != nil {
		t.Fatal(err)
	}
	carry(f, "alice", fast, R)
	carry(f, "alice", slow, R)

	// A period passes. The machine on the fast relay pulled hard and
	// spent it waiting on the bucket; the one on the slow relay took
	// everything its link had and never waited.
	f.report(fast, wire.Demand{Period: time.Second, Reports: []wire.Report{
		{Subject: "alice", Used: 8 << 20, Throttled: true},
	}})
	f.report(slow, wire.Demand{Period: time.Second, Reports: []wire.Report{
		{Subject: "alice", Used: 1 << 20, Throttled: false},
	}})

	got, other := last(fastPushed, "alice"), last(slowPushed, "alice")
	if got+other > R {
		t.Fatalf("the two relays were given %d and %d, which is more than the %d there is", got, other, R)
	}
	// The slow relay asked for what it used plus a tenth, which is well
	// under an equal share, so it gets exactly that and the fast relay
	// gets the rest.
	if want := int64(1<<20) * 11 / 10; other != want {
		t.Fatalf("the slow relay was given %d, want the %d it asked for", other, want)
	}
	if got <= other*4 {
		t.Fatalf("the fast relay was given %d against the slow one's %d; the rate was split rather than divided by demand", got, other)
	}
}

// A relay that goes quiet must give its share back. Holding its last
// demand would strand a subject's rate on a machine that has stopped
// using it.
func TestAQuietRelayGivesItsShareBack(t *testing.T) {
	const R = 10 << 20
	f := testFleet(t)
	fast, fastPushed := joinRecording(f, "fast", 1<<30)
	quiet, _ := joinRecording(f, "quiet", 1<<30)
	if err := f.store.set("alice", 1<<20, R); err != nil {
		t.Fatal(err)
	}
	carry(f, "alice", fast, R)
	carry(f, "alice", quiet, R)

	f.report(quiet, wire.Demand{Period: time.Second, Reports: []wire.Report{
		{Subject: "alice", Used: 5 << 20, Throttled: true},
	}})
	f.report(fast, wire.Demand{Period: time.Second, Reports: []wire.Report{
		{Subject: "alice", Used: 5 << 20, Throttled: true},
	}})
	if got := last(fastPushed, "alice"); got > R*3/4 {
		t.Fatalf("with both relays busy the fast one was given %d of %d", got, R)
	}

	// Three periods later nothing has been heard from the quiet relay.
	f.mu.Lock()
	f.shares[shareKey{"alice", quiet}].until = time.Now().Add(-time.Second)
	f.mu.Unlock()
	f.report(fast, wire.Demand{Period: time.Second, Reports: []wire.Report{
		{Subject: "alice", Used: 5 << 20, Throttled: true},
	}})

	// Max-min gives what was asked for, never more, so the busy relay
	// now gets its whole demand instead of half the rate.
	if want := int64(5<<20) * 3 / 2; last(fastPushed, "alice") != want {
		t.Fatalf("the busy relay was given %d after the other went quiet, want the %d it asked for",
			last(fastPushed, "alice"), want)
	}
}

// A machine placed on a second relay must not double the subject's rate
// for a period. The coordinator issues the lease, so it knows the
// circuit is about to exist and divides before it admits.
func TestPlacingOnASecondRelayDoesNotOversell(t *testing.T) {
	const R = 10 << 20
	f := testFleet(t)
	one, onePushed := joinRecording(f, "one", 1<<30)
	if err := f.store.set("alice", 1<<20, R); err != nil {
		t.Fatal(err)
	}
	if err := f.store.setDevices("alice", []string{peer.ID("a").String(), peer.ID("b").String()}); err != nil {
		t.Fatal(err)
	}
	f.lease(peer.ID("a"))
	if got := last(onePushed, "alice"); got != R {
		t.Fatalf("the only relay carrying alice was given %d, want all %d", got, R)
	}

	two, twoPushed := joinRecording(f, "two", 1<<30)
	carry(f, "alice", two, R)
	f.divide("alice")

	first, second := last(onePushed, "alice"), last(twoPushed, "alice")
	if first+second > R {
		t.Fatalf("two relays hold %d and %d of a %d subject", first, second, R)
	}
	_ = one
}
