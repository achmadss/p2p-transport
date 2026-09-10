// Package shape limits how fast a relay forwards bytes.
//
// There are two limits and no more. Each subject has one bucket shared
// by every machine and every circuit it has on this relay, so the
// machines divide the rate by demand: a machine on a slow link takes
// what it can and leaves the rest to one that can use it. Under that
// sits the relay's own ceiling, which is what makes "this relay is
// full" a fact rather than an estimate.
//
// There is deliberately no per-circuit limit. Splitting a subject's
// rate equally between its circuits is the one division that guarantees
// waste whenever machines differ, and machines always differ.
package shape

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/time/rate"
)

// How much a bucket lets through after sitting idle.
//
// It is a tenth of a second at the bucket's own rate, which is small
// enough that a transfer arrives smoothly and big enough that waiting
// costs nothing on a quiet relay. The floor keeps it above any read the
// relay will make in one go; the ceiling stops a long silence from
// releasing a flood.
const (
	burstFloor = 64 << 10
	burstCeil  = 4 << 20
)

func burstFor(rate int64) int {
	b := rate / 10
	if b < burstFloor {
		b = burstFloor
	}
	if b > burstCeil {
		b = burstCeil
	}
	return int(b)
}

// Limits is what a relay knows about who may send how fast.
//
// It is the same object the coordinator pushes into and the byte path
// reads out of, so a rate changed while a transfer runs changes that
// transfer: the bucket is replaced under the readers already waiting on
// it, and they pick the new one up on their next read.
type Limits struct {
	mu      sync.RWMutex
	subject map[peer.ID]string       // which subject a machine belongs to
	bucket  map[string]*rate.Limiter // one per subject
	total   *rate.Limiter            // the relay's own ceiling
}

// New builds the limits for a relay that can forward total bytes per
// second. A total of zero means no ceiling of its own, which is only
// sensible in a test.
func New(total int64) *Limits {
	l := &Limits{subject: map[peer.ID]string{}, bucket: map[string]*rate.Limiter{}}
	if total > 0 {
		l.total = rate.NewLimiter(rate.Limit(total), burstFor(total))
	}
	return l
}

// Admit records which subject a machine belongs to and what that
// subject may reach, creating the bucket if this is its first machine
// here.
func (l *Limits) Admit(p peer.ID, subject string, max int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subject[p] = subject
	if _, ok := l.bucket[subject]; !ok && max > 0 {
		l.bucket[subject] = rate.NewLimiter(rate.Limit(max), burstFor(max))
	}
}

// Revoke forgets a machine, and the subject's bucket with it once no
// machine here belongs to it.
func (l *Limits) Revoke(p peer.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	subject, ok := l.subject[p]
	if !ok {
		return
	}
	delete(l.subject, p)
	for _, s := range l.subject {
		if s == subject {
			return
		}
	}
	delete(l.bucket, subject)
}

// SetLimit changes what a subject may reach, and takes effect on the
// transfers already running. Nothing reconnects and nothing is torn
// down: the rate is changed on the bucket every reader is already
// waiting on.
func (l *Limits) SetLimit(subject string, max int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bucket[subject]
	if !ok {
		if max > 0 {
			l.bucket[subject] = rate.NewLimiter(rate.Limit(max), burstFor(max))
		}
		return
	}
	b.SetLimit(rate.Limit(max))
	b.SetBurst(burstFor(max))
}

// bucketFor returns the limiters a machine's bytes pass through: its
// subject's, if it has one, and the relay's own.
func (l *Limits) bucketFor(p peer.ID) []*rate.Limiter {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []*rate.Limiter
	if b, ok := l.bucket[l.subject[p]]; ok {
		out = append(out, b)
	}
	if l.total != nil {
		out = append(out, l.total)
	}
	return out
}

// RateFor is the rate a machine's subject may reach, in bytes per
// second, and zero when nothing is shaping it. It answers "what is this
// machine allowed right now", which is the only question a diagnostic
// or a test has.
func (l *Limits) RateFor(p peer.ID) int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	b, ok := l.bucket[l.subject[p]]
	if !ok {
		return 0
	}
	return int64(b.Limit())
}

// Stream wraps a stream so the bytes read from it are shaped.
//
// Reads only, and that is what makes the accounting right: a byte the
// relay forwards is read from one leg and written to the other, so
// shaping every leg's reads charges each byte exactly once, in the
// direction it travelled.
func (l *Limits) Stream(s network.Stream) network.Stream {
	return shaped{Stream: s, limits: l, who: s.Conn().RemotePeer()}
}

type shaped struct {
	network.Stream
	limits *Limits
	who    peer.ID
}

// Read takes the bytes and then pays for them.
//
// Paying afterwards is the only order available — how many bytes are
// there is not known until they have been read — and it is enough:
// the wait holds the next read off, the flow control window fills
// behind it, and the machine on the far end stops sending. The rate is
// looked up per read rather than held, so a limit pushed mid-transfer
// applies to the rest of it.
func (s shaped) Read(p []byte) (int, error) {
	n, err := s.Stream.Read(p)
	for _, b := range s.limits.bucketFor(s.who) {
		pay(b, n)
	}
	return n, err
}

// pay waits out n bytes, in pieces no larger than the bucket can hold.
// A single wait for more than the burst never returns, so a caller
// reading with a buffer bigger than the burst would stall for ever.
func pay(b *rate.Limiter, n int) {
	for n > 0 {
		take := n
		if burst := b.Burst(); take > burst {
			take = burst
		}
		if b.WaitN(context.Background(), take) != nil {
			return
		}
		n -= take
	}
}

// Compile-time proof that a shaped stream is still a stream, since it is
// handed straight back to code that expects one.
var _ network.Stream = shaped{}
