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
	"sync"
	"sync/atomic"
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
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
// transfer: the bucket is retuned rather than replaced, and the readers
// already waiting on it wait by the new rate.
type Limits struct {
	mu      sync.RWMutex
	subject map[peer.ID]string    // machines the coordinator placed here
	via     map[peer.ID]peer.ID   // machines dialling in, and the placed machine they reached
	allow   map[string]*allowance // one per subject
	total   *rate.Limiter         // the relay's own ceiling
}

// allowance is a subject's bucket on this relay, and what has passed
// through it since the coordinator last asked.
//
// The counters are atomic rather than under the lock because they are
// touched on every read of every circuit and looked at once a period.
// Together they are the whole demand signal: how much moved, and whether
// it would have moved more if the bucket had let it.
type allowance struct {
	bucket    *rate.Limiter
	used      atomic.Int64
	throttled atomic.Bool
}

// New builds the limits for a relay that can forward total bytes per
// second. A total of zero means no ceiling of its own, which is only
// sensible in a test.
func New(total int64) *Limits {
	l := &Limits{
		subject: map[peer.ID]string{},
		via:     map[peer.ID]peer.ID{},
		allow:   map[string]*allowance{},
	}
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
	delete(l.via, p)
	l.tune(subject, max)
}

// Attach charges a machine dialling in to the subject it is reaching.
//
// Only one end of a circuit is placed here: the machine holding the
// reservation. The other end dials in from wherever it is, belongs to no
// subject on this relay, and carries the bytes travelling towards the
// placed machine — its downloads. Without this those bytes would meet
// nothing but the relay's own ceiling.
func (l *Limits) Attach(src, dst peer.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, own := l.subject[src]; !own {
		l.via[src] = dst
	}
}

// tune points a subject's bucket at max, creating it on the first
// machine and dropping it when max says there is no limit. An existing
// bucket is retuned rather than replaced, which is what lets a rate
// change reach a transfer already running. The caller holds the lock.
func (l *Limits) tune(subject string, max int64) {
	a, ok := l.allow[subject]
	switch {
	case max <= 0:
		// No limit rather than no bandwidth. A limiter set to zero
		// hands out its burst and then refuses every wait, which the
		// byte path reads as "do not shape" — the opposite of what a
		// zero would mean.
		delete(l.allow, subject)
	case ok:
		a.bucket.SetLimit(rate.Limit(max))
		a.bucket.SetBurst(burstFor(max))
	default:
		l.allow[subject] = &allowance{bucket: rate.NewLimiter(rate.Limit(max), burstFor(max))}
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
	for src, dst := range l.via {
		if dst == p {
			delete(l.via, src)
		}
	}
	for _, s := range l.subject {
		if s == subject {
			return
		}
	}
	delete(l.allow, subject)
}

// SetLimit changes what a subject may reach, and takes effect on the
// transfers already running. Nothing reconnects and nothing is torn
// down: the rate is changed on the bucket every reader is already
// waiting on.
func (l *Limits) SetLimit(subject string, max int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tune(subject, max)
}

// subjectOf is the subject a machine's bytes belong to: its own, or the
// one it dialled in to reach. The caller holds the lock.
func (l *Limits) subjectOf(p peer.ID) string {
	if s, ok := l.subject[p]; ok {
		return s
	}
	return l.subject[l.via[p]]
}

// bucketFor returns what a machine's bytes pass through: its subject's
// allowance, nil when nothing is shaping it, and the relay's own
// ceiling. Two returns rather than a slice because this is the byte
// path, and a slice here is an allocation on every read.
func (l *Limits) bucketFor(p peer.ID) (*allowance, *rate.Limiter) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.allow[l.subjectOf(p)], l.total
}

// Demand is what each subject moved since this was last called, and
// whether it wanted more. Reading it starts a new period.
//
// A subject that moved nothing is left out rather than reported as
// zero: the coordinator lets a share it has not heard about decay on its
// own, so silence and a zero say the same thing for less traffic.
func (l *Limits) Demand() []wire.Report {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]wire.Report, 0, len(l.allow))
	for id, a := range l.allow {
		used := a.used.Swap(0)
		throttled := a.throttled.Swap(false)
		if used == 0 {
			continue
		}
		out = append(out, wire.Report{Subject: id, Used: used, Throttled: throttled})
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
	a, ok := l.allow[l.subjectOf(p)]
	if !ok {
		return 0
	}
	return int64(a.bucket.Limit())
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
	a, total := s.limits.bucketFor(s.who)
	if a != nil {
		if pay(a.bucket, n) {
			// Waiting on its own bucket is the subject saying it would
			// have taken more. Waiting on the relay's ceiling is not:
			// that is the relay being full, and it is not this
			// subject's to ask about.
			a.throttled.Store(true)
		}
		a.used.Add(int64(n))
	}
	pay(total, n)
	return n, err
}

// pay waits out n bytes, in pieces no larger than the bucket can hold,
// and says whether it had to wait at all. A single wait for more than
// the burst never returns, so a caller reading with a buffer bigger than
// the burst would stall for ever. A nil bucket is no limit and costs
// nothing.
//
// The wait is a reservation and a sleep rather than WaitN, which is the
// same thing with the delay hidden. Here the delay is the point: it is
// the only evidence that a subject wanted more than it was given.
func pay(b *rate.Limiter, n int) bool {
	if b == nil {
		return false
	}
	waited := false
	for n > 0 {
		take := n
		if burst := b.Burst(); take > burst {
			take = burst
		}
		r := b.ReserveN(time.Now(), take)
		if !r.OK() {
			return waited
		}
		if d := r.Delay(); d > 0 {
			waited = true
			time.Sleep(d)
		}
		n -= take
	}
	return waited
}

// Compile-time proof that a shaped stream is still a stream, since it is
// handed straight back to code that expects one.
var _ network.Stream = shaped{}
