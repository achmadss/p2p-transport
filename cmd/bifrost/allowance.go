package main

import (
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/peer"
)

// The allowance loop.
//
// A subject has one rate, but its machines are placed on relays
// independently, so that rate has to be divided between the relays
// carrying it and re-divided as demand moves. Pinning every machine of a
// subject to one relay would avoid the whole problem and is worse: it
// makes that relay hot with no remedy short of moving all of them at
// once, and it caps a subject at one machine's capacity.
//
// The division is max-min fair. A relay asking for less than an equal
// share gets exactly what it asked for, and what it leaves is divided
// again among the rest. That is the same answer the machines would get
// from one bucket on one relay, and holding on to it is the point:
// placement changes how long the answer takes, never what it is.

const (
	// demandFloor is the smallest thing a relay is asked to enforce. A
	// relay that carried nothing must still be able to wake up without
	// waiting for the next round.
	demandFloor = 64 << 10

	// openFor is how long a relay that has just been given a machine
	// keeps asking for everything. It has no report yet, so it is
	// divided down with the rest rather than starting at the floor, and
	// the first period costs nobody a slow start.
	openFor = 5 * time.Second

	// A relay names its own reporting period, so these bound what it is
	// allowed to claim: silence for three periods means idle, and a
	// relay must not be able to make that either forever or never.
	minPeriod = 100 * time.Millisecond
	maxPeriod = 30 * time.Second
)

// A share is one subject's slice of one relay.
type share struct {
	want  int64     // what the relay last asked for
	given int64     // what it was last told, so an unchanged number is not pushed again
	until time.Time // when want falls back to the floor, having heard nothing
}

type shareKey struct {
	subject string
	relay   peer.ID
}

// wantFrom estimates what a relay will use next period from what it used
// last one, the way every congestion controller does it. A relay that
// spent the period waiting on the bucket asks for half again as much; one
// that did not asks for a little more than it moved, so a subject that is
// speeding up is not held back by its own history.
func wantFrom(r wire.Report) int64 {
	want := r.Used * 11 / 10
	if r.Throttled {
		want = r.Used * 3 / 2
	}
	if want < demandFloor {
		want = demandFloor
	}
	return want
}

// allocate divides total between the demands, max-min fair.
//
// Everyone asking for no more than an equal share gets exactly what they
// asked for; what they leave is divided again among the rest, until a
// round hands nothing back and the greedy split what is left.
func allocate(total int64, want []int64) []int64 {
	got := make([]int64, len(want))
	left := make([]int, 0, len(want))
	for i := range want {
		left = append(left, i)
	}

	remaining := total
	for len(left) > 0 {
		fair := remaining / int64(len(left))
		rest := left[:0:0]
		for _, i := range left {
			if want[i] <= fair {
				got[i] = want[i]
				remaining -= want[i]
				continue
			}
			rest = append(rest, i)
		}
		if len(rest) == len(left) {
			for _, i := range rest {
				got[i] = fair
			}
			break
		}
		left = rest
	}

	// A rate of zero is how "no limit" is written on a relay, so a
	// subject squeezed down to nothing must still be given something.
	for i := range got {
		if got[i] < 1 {
			got[i] = 1
		}
	}
	return got
}

// open records that a relay is now carrying a subject, asking for
// everything until it has reported. The caller holds the lock.
func (f *fleet) open(subject string, relay peer.ID, max int64) {
	if _, ok := f.shares[shareKey{subject, relay}]; ok {
		return
	}
	f.shares[shareKey{subject, relay}] = &share{want: max, until: time.Now().Add(openFor)}
}

// forget drops a subject's share of a relay once no machine of that
// subject is left there, and says whether it dropped one. The caller
// holds the lock.
func (f *fleet) forget(subject string, relay peer.ID) bool {
	for _, p := range f.placed {
		if p.subject == subject && p.relay == relay {
			return false
		}
	}
	if _, ok := f.shares[shareKey{subject, relay}]; !ok {
		return false
	}
	delete(f.shares, shareKey{subject, relay})
	return true
}

// allotted is what a relay was last told a subject may use there.
func (f *fleet) allotted(subject string, relay peer.ID) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.shares[shareKey{subject, relay}]; ok {
		return s.given
	}
	return 0
}

// report takes one period of demand from a relay and re-divides every
// subject it mentioned.
func (f *fleet) report(id peer.ID, d wire.Demand) {
	period := min(max(d.Period, minPeriod), maxPeriod)
	// ponytail: no cap on how many subjects one message may name. The
	// stream is authenticated but not admitted; a size limit belongs
	// here when bifrost is exposed to anything but its own relays.
	until := time.Now().Add(3 * period)

	// A set, not a list: dividing is a scan of every share, and a relay
	// naming one subject twice must not buy two of them.
	touched := map[string]bool{}
	f.mu.Lock()
	for _, r := range d.Reports {
		s, ok := f.shares[shareKey{r.Subject, id}]
		if !ok {
			continue // nothing of that subject is placed here
		}
		s.want, s.until = wantFrom(r), until
		touched[r.Subject] = true
	}
	f.mu.Unlock()

	for subject := range touched {
		f.divide(subject)
	}
}

// divide splits a subject's rate between the relays carrying it and
// pushes every share that moved.
//
// Only what changed is sent. A fleet where nothing is happening is a
// fleet where nothing is on the wire, which is what makes a one-second
// period affordable.
func (f *fleet) divide(subject string) {
	_, sub, ok := f.store.subject(subject)
	if !ok {
		return
	}

	type push struct {
		n    *relayNode
		rate int64
	}
	var out []push

	now := time.Now()
	f.mu.Lock()
	// ponytail: a scan of every share to find one subject's. A map per
	// subject when the scan shows up in a profile, not before.
	var keys []shareKey
	var want []int64
	for k, s := range f.shares {
		if k.subject != subject {
			continue
		}
		w := s.want
		if now.After(s.until) {
			// Nothing heard for three periods. The relay has gone quiet,
			// and holding its last demand would strand the rate there.
			w = demandFloor
		}
		keys, want = append(keys, k), append(want, w)
	}
	for i, got := range allocate(sub.Max, want) {
		s := f.shares[keys[i]]
		if s.given == got {
			continue
		}
		s.given = got
		if n, ok := f.relays[keys[i].relay]; ok {
			out = append(out, push{n, got})
		}
	}
	f.mu.Unlock()

	for _, p := range out {
		if err := p.n.push(wire.Command{Op: wire.OpSetLimit, Subject: subject, Rate: p.rate}); err != nil {
			f.drop(p.n)
		}
	}
}
