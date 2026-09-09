// Package discovery finds other Ratatoskr machines.
//
// This is the only discovery layer 4 does: mDNS, no server, no
// Internet. PLAN.md §5 gives it a 400 ms head start over whatever
// addresses the application supplies, because a peer found here is
// dialled over the local network and nothing leaves it. There is no
// Discovery interface, because addresses from anywhere else arrive as
// opaque strings rather than as another implementation.
package discovery

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/achmadss/p2p-transport/internal/identity"
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
	h host.Host

	mu   sync.Mutex
	seen map[peer.ID][]peer.AddrInfo // one entry per peer, latest addrs

	svc    mdns.Service
	cancel context.CancelFunc
	done   chan struct{}
}

// Start begins advertising and listening. Close stops both.
//
// RATATOSKR_NO_MDNS returns a LAN that finds nothing and advertises
// nothing, for a machine where multicast is not merely useless but
// harmful. One such machine is on record: a macOS content filter fails
// every packet this sends to 224.0.0.251 — the kernel logs
// `sosend_reinject() failed` for lport 5353 — and the agent takes the
// terminal down with it on startup, while the same machine runs
// punch-quic, which opens no multicast socket, without trouble. Nothing
// in the LAN path can defend against a filter below the socket, so the
// only remedy available here is not to open it.
//
// The cost is exactly what it says: no peers found on this network, and
// this machine invisible to peers on it. Everything reached through a
// relay or a punched address still works.
func Start(h host.Host) (*LAN, error) {
	l := &LAN{h: h, seen: map[peer.ID][]peer.AddrInfo{}, done: make(chan struct{})}
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

// watchAddresses restarts the advertisement when this machine's
// addresses change. Joining a different network, or waking from sleep on
// a new one, otherwise leaves the service announcing an address that no
// longer reaches anything.
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

// forget drops the peer list on a network change. The machines on the
// old network are not reachable from the new one, and reporting them as
// found would be a guess.
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
	defer l.mu.Unlock()
	l.seen[info.ID] = []peer.AddrInfo{info}
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

// Find waits for a peer whose id or short fingerprint matches want. It
// polls rather than plumbing a channel to every caller; mDNS answers in
// milliseconds and the caller is a person waiting at a prompt.
func (l *LAN) Find(ctx context.Context, want string) (peer.AddrInfo, error) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, p := range l.Peers() {
			if Matches(p.ID, want) {
				return p, nil
			}
		}
		select {
		case <-ctx.Done():
			return peer.AddrInfo{}, fmt.Errorf("no machine matching %q answered on this network", want)
		case <-tick.C:
		}
	}
}

// Matches accepts either the full peer id or the short fingerprint that
// `ratatoskr id` prints.
func Matches(id peer.ID, want string) bool {
	s := id.String()
	return s == want || identity.Short(s) == want
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
