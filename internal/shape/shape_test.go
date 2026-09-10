package shape

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Every machine of one subject shares one bucket. Two machines that both
// pull hard land near half each; a machine on a slow link takes what it
// can and leaves the rest. That sharing only happens because the bucket
// is keyed by subject, so this is the check that it is.
func TestOneBucketPerSubject(t *testing.T) {
	l := New(0)
	fast, slow, stranger := peer.ID("fast"), peer.ID("slow"), peer.ID("stranger")

	l.Admit(fast, "alice", 1000)
	l.Admit(slow, "alice", 1000)
	l.Admit(stranger, "bob", 500)

	if l.bucketFor(fast)[0] != l.bucketFor(slow)[0] {
		t.Fatal("two machines of one subject are drawing on different buckets")
	}
	if l.bucketFor(fast)[0] == l.bucketFor(stranger)[0] {
		t.Fatal("two subjects are sharing one bucket")
	}
	if got := l.RateFor(fast); got != 1000 {
		t.Fatalf("shaping at %d, want 1000", got)
	}
}

// A machine nobody admitted still meets the relay's own ceiling. It is
// the difference between an unadmitted circuit costing the relay
// nothing and costing it everything.
func TestUnknownMachineMeetsTheCeiling(t *testing.T) {
	l := New(1000)
	if got := l.bucketFor(peer.ID("stranger")); len(got) != 1 || got[0] != l.total {
		t.Fatalf("an unknown machine passes through %d limiters, want the relay ceiling", len(got))
	}
	if got := l.RateFor(peer.ID("stranger")); got != 0 {
		t.Fatalf("an unknown machine has a subject rate of %d, want none", got)
	}
}

// A rate change reaches the transfers already running. The bucket is not
// replaced, it is retuned, so a reader waiting on it right now waits by
// the new rate.
func TestSetLimitKeepsTheBucket(t *testing.T) {
	l := New(0)
	who := peer.ID("who")
	l.Admit(who, "alice", 1000)
	before := l.bucketFor(who)[0]

	l.SetLimit("alice", 4000)
	if after := l.bucketFor(who)[0]; after != before {
		t.Fatal("changing the rate replaced the bucket, so a transfer already running would keep the old one")
	}
	if got := l.RateFor(who); got != 4000 {
		t.Fatalf("shaping at %d after the change, want 4000", got)
	}
}

// A subject's bucket goes when its last machine does, and not before.
func TestRevokeKeepsTheBucketUntilTheLastMachine(t *testing.T) {
	l := New(0)
	first, second := peer.ID("first"), peer.ID("second")
	l.Admit(first, "alice", 1000)
	l.Admit(second, "alice", 1000)

	l.Revoke(first)
	if got := l.RateFor(second); got != 1000 {
		t.Fatalf("one machine leaving took the subject's bucket with it: %d", got)
	}
	l.Revoke(second)
	if got := l.RateFor(second); got != 0 {
		t.Fatalf("the bucket outlived every machine on it: %d", got)
	}
}

// The rate is really enforced, and enforced on the total rather than
// per reader: two readers on one subject take twice as long as one, and
// that is the whole point of a shared bucket.
func TestRateIsEnforced(t *testing.T) {
	const perSecond = 200 << 10 // 200 KiB/s
	l := New(0)
	one, two := peer.ID("one"), peer.ID("two")
	l.Admit(one, "alice", perSecond)
	l.Admit(two, "alice", perSecond)

	// The first burst is free, so it is spent before the clock starts.
	pay(l.bucketFor(one)[0], l.bucketFor(one)[0].Burst())

	start := time.Now()
	done := make(chan struct{}, 2)
	for _, who := range []peer.ID{one, two} {
		go func() {
			for range 10 {
				pay(l.bucketFor(who)[0], perSecond/10)
			}
			done <- struct{}{}
		}()
	}
	<-done
	<-done

	// Two readers moved two seconds' worth between them. Generous
	// bounds: this is a check that the bucket is shared and enforced,
	// not a measurement of anything.
	if took := time.Since(start); took < 1500*time.Millisecond || took > 4*time.Second {
		t.Fatalf("two readers on one %d byte/s bucket took %s to move two seconds' worth", perSecond, took)
	}
}
