package transport

import (
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
)

// A wire tap on the socket libp2p actually punches from.
//
// Every measurement so far has been taken beside that socket rather than
// on it: tcpdump on the machine that could run it, a reflector on a VPS,
// a second program on a second socket. Between them they proved the two
// carriers punch, pass QUIC and hold an endpoint-independent mapping, and
// still left the same hole in the account — the agent punches, the peer
// punches, and neither sees a packet. One of those two claims is false
// and no instrument outside the process can say which.
//
// quicreuse.OverrideListenUDP hands us the socket before QUIC takes it,
// so this counts what the punch itself sends and receives, per remote
// address, on the machine that has no packet capture and no root.
//
// Set RATATOSKR_DIAG_WIRE to the peer's public IP to turn it on.

type tapped struct {
	net.PacketConn
	watch string

	mu   sync.Mutex
	sent map[string]int
	recv map[string]int
}

func (t *tapped) count(m map[string]int, addr net.Addr) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil || host != t.watch {
		return
	}
	t.mu.Lock()
	m[addr.String()]++
	t.mu.Unlock()
}

func (t *tapped) WriteTo(b []byte, addr net.Addr) (int, error) {
	t.count(t.sent, addr)
	return t.PacketConn.WriteTo(b, addr)
}

func (t *tapped) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := t.PacketConn.ReadFrom(b)
	if err == nil && addr != nil {
		t.count(t.recv, addr)
	}
	return n, addr, err
}

// report prints the two tallies whenever either has moved. A punch that
// sends and never receives is the whole finding, and it is only legible
// beside the timestamps in the hole-punch log.
func (t *tapped) report() {
	var last string
	for range time.Tick(2 * time.Second) {
		t.mu.Lock()
		s := "wire: sent " + tally(t.sent) + " received " + tally(t.recv)
		t.mu.Unlock()
		if s != last {
			fmt.Fprintln(os.Stderr, s)
			last = s
		}
	}
}

func tally(m map[string]int) string {
	if len(m) == 0 {
		return "nothing"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += fmt.Sprintf("%s=%d ", k, m[k])
	}
	return out
}

// wireTap returns the libp2p option that installs the tap, or nothing at
// all when RATATOSKR_DIAG_WIRE is unset — this is a diagnostic, and an
// agent that is not being diagnosed keeps the stock socket.
func wireTap() []libp2p.Option {
	ip := os.Getenv("RATATOSKR_DIAG_WIRE")
	if ip == "" {
		return nil
	}
	fmt.Fprintf(os.Stderr, "wire: watching packets to and from %s\n", ip)
	listen := func(network string, laddr *net.UDPAddr) (net.PacketConn, error) {
		c, err := net.ListenUDP(network, laddr)
		if err != nil {
			return nil, err
		}
		t := &tapped{PacketConn: c, watch: ip, sent: map[string]int{}, recv: map[string]int{}}
		go t.report()
		return t, nil
	}
	return []libp2p.Option{libp2p.QUICReuse(quicreuse.NewConnManager, quicreuse.OverrideListenUDP(listen))}
}
