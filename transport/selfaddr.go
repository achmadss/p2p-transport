package transport

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/achmadss/p2p-transport/internal/stun"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
)

// Asking a reflector where libp2p's own QUIC socket is, from inside the
// process.
//
// TODO.md step 3 measured a carrier that leaves an open mapping on its
// original port and gives each new destination the next value of a
// counter that walks forward. The agent's connection to the relay is
// opened once at startup and never moves, so the port the relay
// observes drifts further from the port a fresh peer will reach for as
// long as the agent runs. Publishing the relay's view therefore hands
// every peer an address that was true at startup, and the punch aims at
// a door nobody is behind. A three-minute-old agent already fails; an
// hour-old one fails by more.
//
// There is no offset to learn and add, because it is not a property of
// the carrier — it is the age of the connection that was measured. The
// only address worth publishing is one taken on the socket that will do
// the punching, toward somewhere it has not spoken to, at the moment it
// punches. That socket belongs to quic-go, and
// quicreuse.OverrideListenUDP is the one place it can be wrapped before
// it is handed over.
//
// The reply is intercepted on the way in. quic-go would drop it as
// unparseable, which is harmless, but it is also the answer we asked
// for and nobody else can read it.

// endpointsFresh is how long a measured set is worth believing.
//
// Tailscale's `endpointsFreshEnoughDuration`, taken with its reasoning
// intact: a UDP mapping typically expires at thirty seconds, so a set
// measured twenty-seven seconds ago names doors that are probably still
// open, and the set measured when the agent started names doors that
// certainly are not. Their `enqueueCallMeMaybe` checks this clock before
// signalling and re-measures if it has run out; here the refresher runs
// on it and the punch reads what the refresher left.
const endpointsFresh = 27 * time.Second

// selfAddr wraps the socket libp2p punches from and can name it.
type selfAddr struct {
	net.PacketConn

	mu      sync.Mutex
	waiting map[string]chan string // transaction id -> where the answer goes

	cacheMu sync.Mutex
	cache   []string
	taken   time.Time
}

// Addrs returns every public address this socket answers on, as several
// reflectors see it independently, freshest first. Empty when none
// answers in time.
//
// One answer was never enough. A NAT can hold the mapping a first
// observer was given while handing a second observer a different port,
// and then the first port is a door with nobody behind it — netcheck's
// GetGlobalAddrs describes exactly this case, "new traffic to the old
// endpoint will not succeed, but new traffic to the newly discovered
// endpoints does succeed". So ask everyone at once and offer the whole
// set: DCUtR's CONNECT carries a list, and it has been handed one
// address since the day it was wired up.
//
// The keep rule is theirs. The first answer back is kept whatever else
// happens — it is the lowest-latency reflector's word, and dropping it
// would leave a machine with a perfectly ordinary NAT advertising
// nothing. Every other distinct address needs two independent sightings,
// because an address one observer alone reports is a door minted for
// that observer.
//
// Answers are cached for endpointsFresh. A punch asks more than once
// within a few seconds and the set must not change underneath it.
func (s *selfAddr) Addrs(within time.Duration) []string {
	s.cacheMu.Lock()
	if time.Since(s.taken) < endpointsFresh && len(s.cache) > 0 {
		defer s.cacheMu.Unlock()
		return s.cache
	}
	s.cacheMu.Unlock()

	servers := stun.Servers()
	answers := make(chan string, len(servers))
	for _, server := range servers {
		go func(server string) { answers <- s.ask(server, within) }(server)
	}
	order := make([]string, 0, len(servers))
	seen := map[string]int{}
	for range servers {
		a := <-answers
		if a == "" {
			continue
		}
		if seen[a]++; seen[a] == 1 {
			order = append(order, a)
		}
	}

	out := keepCorroborated(order, seen)
	if len(out) > 0 {
		s.cacheMu.Lock()
		s.cache, s.taken = out, time.Now()
		s.cacheMu.Unlock()
	}
	if os.Getenv("RATATOSKR_DIAG") != "" {
		// Answers and distinct addresses are different counts, and
		// confusing them inverts the finding: one address seen seven
		// times is a carrier that gives every observer the same door,
		// and seven addresses seen once each is a carrier that gives
		// each one its own. Both used to print as "1 of 8".
		answers := 0
		say := make([]string, 0, len(order))
		for _, a := range order {
			answers += seen[a]
			say = append(say, fmt.Sprintf("%s x%d", a, seen[a]))
		}
		fmt.Fprintf(os.Stderr, "punch addresses: %d of %d reflectors answered, %d distinct: %s\n",
			answers, len(servers), len(order), strings.Join(say, ", "))
	}
	return out
}

// keepCorroborated applies the rule above to answers in arrival order.
func keepCorroborated(order []string, seen map[string]int) []string {
	var out []string
	for i, a := range order {
		if i == 0 || seen[a] > 1 {
			out = append(out, a)
		}
	}
	return out
}

func (s *selfAddr) ask(server string, within time.Duration) string {
	addr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return ""
	}
	req, txid, err := stun.Request()
	if err != nil {
		return ""
	}

	reply := make(chan string, 1)
	s.mu.Lock()
	s.waiting[string(txid)] = reply
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, string(txid))
		s.mu.Unlock()
	}()

	if _, err := s.PacketConn.WriteTo(req, addr); err != nil {
		return ""
	}
	select {
	case a := <-reply:
		return a
	case <-time.After(within):
		return ""
	}
}

// ReadFrom hands quic-go everything except the answers we asked for.
func (s *selfAddr) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := s.PacketConn.ReadFrom(b)
		if err != nil {
			return n, addr, err
		}
		if s.claim(b[:n]) {
			continue // ours, and quic-go has no use for it
		}
		return n, addr, err
	}
}

// claim reports whether these bytes answer one of our own questions, and
// delivers the answer if so.
func (s *selfAddr) claim(b []byte) bool {
	if len(b) < 20 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiting) == 0 {
		return false
	}
	ch, ok := s.waiting[string(b[8:20])]
	if !ok {
		return false
	}
	// A reply whose id matches is ours whether or not it parses; passing
	// a malformed one to quic-go helps nobody.
	if mapped := stun.ParseResponse(b, b[8:20]); mapped != "" {
		select {
		case ch <- mapped:
		default:
		}
	}
	return true
}

// socketRef holds the socket once libp2p opens it. The option has to be
// built before libp2p.New and the socket only exists afterwards, so the
// caller keeps the box rather than the thing.
type socketRef struct{ v atomic.Pointer[selfAddr] }

// Addrs names the socket, or returns nothing before one exists or when
// no reflector answers.
func (r *socketRef) Addrs(within time.Duration) []string {
	s := r.v.Load()
	if s == nil {
		return nil
	}
	return s.Addrs(within)
}

// ownSocket installs the wrapper and hands back the box the first IPv4
// socket lands in.
//
// libp2p opens one socket per listen address and every QUIC listen
// address here binds a wildcard, so in practice there is one and the
// punch leaves from it. Later sockets are wrapped too and simply never
// asked.
func ownSocket() (*socketRef, libp2p.Option) {
	ref := &socketRef{}

	listen := func(network string, laddr *net.UDPAddr) (net.PacketConn, error) {
		c, err := net.ListenUDP(network, laddr)
		if err != nil {
			return nil, err
		}
		s := &selfAddr{PacketConn: c, waiting: map[string]chan string{}}
		if network == "udp4" {
			ref.v.CompareAndSwap(nil, s)
		}
		return s, nil
	}

	return ref, libp2p.QUICReuse(quicreuse.NewConnManager, quicreuse.OverrideListenUDP(listen))
}
