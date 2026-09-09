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

// Asking a reflector where the punching socket is, from inside the
// process that owns it.
//
// A reflector reports the port belonging to the socket that asked, so a
// throwaway socket learns nothing about the one libp2p punches from. On
// a carrier that gives each new destination its own port, that is the
// whole difference: the address a relay saw at startup ages while the
// port a fresh peer would reach walks away from it, so the punch aims at
// a door nobody is behind.
//
// The socket belongs to quic-go, and quicreuse.OverrideListenUDP is the
// one place it can be wrapped before it is handed over. The reply is
// intercepted on the way in, since quic-go would drop it as unparseable
// and nobody else can read it.

// endpointsFresh is how long a measured set is worth believing. A UDP
// mapping usually expires at thirty seconds, so a set measured
// twenty-seven seconds ago names doors probably still open, and one
// measured at startup names doors that certainly are not.
const endpointsFresh = 27 * time.Second

// selfAddr wraps the socket libp2p punches from and can name it, by
// writing STUN requests into it and claiming the replies before quic-go
// sees them.
type selfAddr struct {
	net.PacketConn

	mu      sync.Mutex
	waiting map[string]chan string // transaction id -> where the answer goes

	cacheMu sync.Mutex
	cache   []string
	taken   time.Time
}

// Addrs returns every public address this socket answers on, as several
// reflectors see it independently, in arrival order. Empty when none
// answers within the given timeout.
//
// It is a set rather than one address because a NAT can hold the mapping
// one observer was given while handing another a different port, leaving
// the first as a door with nobody behind it. Every candidate is offered
// and every candidate is dialled.
//
// The keep rule: the first answer back survives whatever else happens,
// since dropping it would leave an ordinary NAT advertising nothing.
// Every other distinct address needs two independent sightings, because
// an address only one observer reports was minted for that observer.
//
// Answers are cached for endpointsFresh, so a punch asking twice within
// a few seconds sees the same set.
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
		// times is a carrier giving every observer the same door, seven
		// addresses seen once each is one giving each its own.
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

// keepCorroborated keeps the first answer and every address seen twice.
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

// ReadFrom passes quic-go everything except the replies to our own STUN
// requests, which it would only discard.
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

// claim reports whether these bytes answer one of our own requests, and
// delivers the address if so.
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
	// A reply whose id matches is ours whether or not it parses, and a
	// malformed one is no use to quic-go either.
	if mapped := stun.ParseResponse(b, b[8:20]); mapped != "" {
		select {
		case ch <- mapped:
		default:
		}
	}
	return true
}

// socketRef holds the socket once libp2p opens it. The option must be
// built before libp2p.New and the socket exists only afterwards, so the
// caller keeps the box rather than the thing.
type socketRef struct{ v atomic.Pointer[selfAddr] }

// Addrs names the socket. Empty before one exists, or when no reflector
// answers.
func (r *socketRef) Addrs(within time.Duration) []string {
	s := r.v.Load()
	if s == nil {
		return nil
	}
	return s.Addrs(within)
}

// ownSocket installs the wrapper and returns the box the first IPv4
// socket lands in. libp2p opens one socket per listen address and the
// QUIC addresses here bind wildcards, so in practice there is one and
// the punch leaves from it; later sockets are wrapped but never asked.
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
