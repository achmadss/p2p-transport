// Package discovery finds other machines on the local network over mDNS,
// with no server and no Internet.
//
// It is the only discovery the transport does. Machines anywhere else
// arrive as addresses the application obtained some other way, so there
// is no Discovery interface to implement.
package discovery

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

// service is what Ratatoskr answers to on the local network. libp2p
// appends the .local domain.
const service = "_ratatoskr._udp"

// LAN advertises this host on the local network and remembers the peers
// it hears from.
type LAN struct {
	h     host.Host
	found func(peer.AddrInfo) // told about every peer heard from

	mu   sync.Mutex
	seen map[peer.ID][]peer.AddrInfo // one entry per peer, latest addrs

	svc    mdns.Service
	cancel context.CancelFunc
	done   chan struct{}
}

// Start begins advertising and listening. Close stops both.
//
// found is called for each peer heard from, so a caller is told rather
// than having to poll. Nil is allowed; a caller that only wants the list
// at the end reads Peers.
//
// Setting RATATOSKR_NO_MDNS returns a LAN that finds nothing and
// advertises nothing, for a machine where opening a multicast socket is
// not merely useless but harmful — a content filter below the socket
// cannot be defended against from here, and one is on record killing the
// process at startup. The cost is exactly that: no machines found on
// this network and none finding this one. Relayed and direct
// connections are unaffected.
func Start(h host.Host, found func(peer.AddrInfo)) (*LAN, error) {
	l := &LAN{h: h, found: found, seen: map[peer.ID][]peer.AddrInfo{}, done: make(chan struct{})}
	if os.Getenv("RATATOSKR_NO_MDNS") != "" {
		fmt.Fprintln(os.Stderr, "mdns off: this machine will not find or be found on the LAN")
		close(l.done)
		return l, nil
	}
	if err := l.restart(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	go l.watchAddresses(ctx)
	return l, nil
}

func (l *LAN) restart() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.svc != nil {
		l.svc.Close()
	}
	svc := mdns.NewMdnsService(l.h, service, l)
	if err := svc.Start(); err != nil {
		return fmt.Errorf("start mDNS: %w", err)
	}
	l.svc = svc
	return nil
}

// watchAddresses re-advertises when this machine's addresses change.
// Joining a different network, or waking on one, otherwise leaves the
// service announcing an address that reaches nothing.
func (l *LAN) watchAddresses(ctx context.Context) {
	defer close(l.done)

	sub, err := l.h.EventBus().Subscribe(new(event.EvtLocalAddressesUpdated))
	if err != nil {
		return // no re-advertisement; the first one still stands
	}
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.Out():
			if !ok {
				return
			}
			l.forget()
			if err := l.restart(); err != nil {
				return
			}
		}
	}
}

// forget drops the peer list on a network change: machines on the old
// network are not reachable from the new one.
func (l *LAN) forget() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = map[peer.ID][]peer.AddrInfo{}
}

// HandlePeerFound satisfies mdns.Notifee.
func (l *LAN) HandlePeerFound(info peer.AddrInfo) {
	if info.ID == l.h.ID() {
		return // our own announcement, echoed back
	}
	l.mu.Lock()
	l.seen[info.ID] = []peer.AddrInfo{info}
	l.mu.Unlock()
	if l.found != nil {
		l.found(info)
	}
}

// Peers is what has been heard so far, in no particular order.
func (l *LAN) Peers() []peer.AddrInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]peer.AddrInfo, 0, len(l.seen))
	for _, v := range l.seen {
		out = append(out, v[0])
	}
	return out
}

func (l *LAN) Close() error {
	// A LAN that never started has no watcher to cancel and no service
	// to stop, and the shutdown path must not be the thing that crashes
	// on the machine mDNS was turned off to rescue.
	if l.cancel != nil {
		l.cancel()
	}
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.svc == nil {
		return nil
	}
	return l.svc.Close()
}
