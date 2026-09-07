package transport

import (
	"fmt"
	"net"
	"os"
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

// selfAddr wraps the socket libp2p punches from and can name it.
type selfAddr struct {
	net.PacketConn

	mu      sync.Mutex
	waiting map[string]chan string // transaction id -> where the answer goes

	cacheMu sync.Mutex
	cache   string
	taken   time.Time
}

// Where returns this socket's public address as a reflector sees it, or
// "" if none answers in time.
//
// Answers are cached briefly. A hole punch asks for its own addresses
// more than once within a few seconds, and on this carrier the port is
// stable over that span — it drifts over minutes, not milliseconds. The
// cache is what keeps one punch from measuring three different ports
// and offering all of them as if they were alternatives.
func (s *selfAddr) Where(within time.Duration) string {
	s.cacheMu.Lock()
	if time.Since(s.taken) < 5*time.Second && s.cache != "" {
		defer s.cacheMu.Unlock()
		return s.cache
	}
	s.cacheMu.Unlock()

	for _, server := range stun.Servers() {
		if a := s.ask(server, within); a != "" {
			s.cacheMu.Lock()
			s.cache, s.taken = a, time.Now()
			s.cacheMu.Unlock()
			return a
		}
	}
	return ""
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

// Where names the socket, or returns "" before one exists or when no
// reflector answers. It prints each fresh measurement under
// RATATOSKR_DIAG, because the drift this defeats is otherwise invisible
// in a run that simply works.
func (r *socketRef) Where(within time.Duration) string {
	s := r.v.Load()
	if s == nil {
		return ""
	}
	a := s.Where(within)
	if a != "" && os.Getenv("RATATOSKR_DIAG") != "" {
		fmt.Fprintf(os.Stderr, "punch address measured now: %s\n", a)
	}
	return a
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
