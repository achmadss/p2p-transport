// Package transport carries bytes between two machines over the best
// path it can find: the local network if the far end is there, a direct
// connection across the Internet if not, and a relay only when neither
// can be opened. It reports who the far end is, a stream to it, and
// which path the bytes are taking.
//
//	h, err := transport.New(transport.Config{Relays: relays})
//	h.Handle("/myapp/1.0.0", func(s transport.Stream) { ... })
//	h.Connect(ctx, id, addrs)   // addrs came from the far end's Addrs()
//	s, err := h.Open(ctx, id, "/myapp/1.0.0")
//
// api.go holds the whole exported surface. The handshake authenticates
// the far end, so a caller needs no challenge of its own; deciding what
// an authenticated machine may do is the caller's.
package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/achmadss/p2p-transport/internal/discovery"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// listenAddrs binds every interface on a port the OS picks. QUIC comes
// first so it is preferred; TCP is there for networks that drop UDP.
var listenAddrs = []string{
	"/ip4/0.0.0.0/udp/0/quic-v1",
	"/ip6/::/udp/0/quic-v1",
	"/ip4/0.0.0.0/tcp/0",
	"/ip6/::/tcp/0",
}

// Host is this machine on the network. It both serves the protocols
// registered on it and opens streams to other machines, so there is no
// separate client type.
type Host struct {
	h host.Host

	// lan is local discovery, nil when Config.NoLAN was set. Started
	// here so OnLAN is the only way a caller reaches it.
	lan *discovery.LAN

	mu    sync.Mutex
	onLAN []func(PeerID, []string)

	// ctx is cancelled by Close, and is the parent of every dial the
	// background work makes: measuring this machine's public addresses,
	// holding the relays, and chasing a peer that is relayed or gone. A
	// dial rooted in Background instead goes on for its whole timeout
	// after the host has gone, which is what a leak on shutdown looks
	// like.
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	// working holds the peers repair is already running for, so a second
	// connection event does not start a second loop.
	working sync.Map // peer.ID -> struct{}

	// watchers are the open Watch calls, by the peer each one follows.
	watchMu  sync.Mutex
	watchers map[peer.ID][]*watcher

	// relays is what this machine may relay through: the addresses
	// Config named, or whatever the coordinator has most recently
	// leased. Empty on a machine with no relay, which then fails to
	// reach an unreachable peer rather than hanging.
	relays *relaySet
}

// New starts this machine's transport, loading or generating its
// identity in Config.Dir. Close stops it and everything it started.
func New(cfg Config) (*Host, error) {
	id, err := identity.LoadOrCreate(cfg.Dir)
	if err != nil {
		return nil, err
	}
	key := id.PrivateKey()
	infos, err := parseAddrs(cfg.Relays)
	if err != nil {
		return nil, err
	}
	coords, err := parseAddrs(cfg.Coordinator)
	if err != nil {
		return nil, err
	}
	if len(infos) > 0 && len(coords) > 0 {
		return nil, fmt.Errorf("configure relays or a coordinator, not both")
	}
	var coord peer.AddrInfo
	if len(coords) > 0 {
		if coord, err = mergeInfos(coords); err != nil {
			return nil, fmt.Errorf("coordinator: %w", err)
		}
	}

	// Built before the host, because the option below closes over it and
	// the lease loop starts filling it as soon as the host exists.
	set := newRelaySet()
	set.set(infos)

	// A machine with a coordinator expects to need a relay even before
	// it has been given one, so everything downstream is switched on the
	// intention rather than on what has arrived so far.
	usesRelay := len(infos) > 0 || len(coords) > 0

	// The socket must be wrapped before libp2p opens it and is asked
	// about long after, so the handle is made now and filled in by the
	// option.
	mine, socketOpt := ownSocket()

	// Listed explicitly rather than taking libp2p's defaults, which
	// would also enable WebTransport and TLS. Which transports exist is
	// a decision to make here, not to inherit and discover later.
	opts := []libp2p.Option{
		libp2p.Identity(key),
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Transport(quic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		// AutoNAT learns whether this machine is reachable; DCUtR opens
		// a direct connection beside a relayed one when it can. Both are
		// why the relay stays a fallback.
		libp2p.EnableNATService(),
		// v1 asks several peers to dial back and takes a majority; v2
		// asks one peer about one address and is told which. Two
		// machines and a relay never reach a v1 majority, which is the
		// same shortage that forces the reservation below. v2 runs
		// beside v1 rather than replacing it.
		libp2p.EnableAutoNATv2(),
		libp2p.EnableHolePunching(holepunch.WithAddrFilter(punchFilter{hasIPv6: hasGlobalIPv6, now: mine})),
		// Ask the router to forward a port, the way a torrent client
		// does. A machine with a forwarded port can be dialled by peers
		// that could never be dialled themselves, and it is the only
		// answer to a carrier NAT no punch will open.
		libp2p.NATPortMap(),
	}
	// Where the world says this machine is, from two observers that fail
	// differently. observed is what a relay saw when this machine
	// connected to it: correct on a NAT that keeps its mappings, stale
	// within minutes on one that renumbers. measured is what several
	// reflectors see on the live socket, refreshed every endpointsFresh.
	// Both stay empty until an answer arrives rather than publishing a
	// guess.
	var observed, measured atomic.Value
	opts = append(opts, libp2p.AddrsFactory(func(as []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		out := append(as[:0:0], as...)
		for _, store := range []*atomic.Value{&observed, &measured} {
			// Nothing is published before the first answer lands, which
			// is the honest state rather than a failure.
			extra, _ := store.Load().([]multiaddr.Multiaddr)
			for _, a := range extra {
				if !has(out, a) {
					out = append(out, a)
				}
			}
		}
		return out
	}))

	if usesRelay {
		// The candidates are read from the set on every call rather
		// than fixed at startup, so a machine the coordinator moves
		// reserves with the relay it was moved to.
		//
		// One candidate is enough and is the normal case: without
		// saying so, the default is to collect four before reserving
		// with any, and a machine given one relay would wait out the
		// boot delay before becoming reachable at all.
		opts = append(opts, libp2p.EnableAutoRelayWithPeerSource(set.source,
			autorelay.WithMinCandidates(1),
			autorelay.WithNumRelays(1),
		))
	}
	// AutoRelay reserves a slot only once AutoNAT has ruled this machine
	// unreachable, and AutoNAT needs several independent peers to agree.
	// Two machines and one relay never reach that, so the reservation is
	// forced: naming a relay is the operator saying they expect to need
	// one. RATATOSKR_ASSUME_PUBLIC opts out. A machine that really is
	// reachable loses nothing, since it goes on advertising its direct
	// addresses and peers prefer them.
	if usesRelay && os.Getenv("RATATOSKR_ASSUME_PUBLIC") == "" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}

	opts = append(opts, socketOpt)

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("start host: %w", err)
	}
	wire.HandleObserved(h)
	handleAddrs(h)
	t := wrap(h)
	t.relays = set
	t.watchForRelayed()
	// Both exist to get off a relay, so neither runs without one. With
	// no relay there is no relayed connection to escape and no reason to
	// ask eight reflectors every 27 seconds for the life of the process.
	if usesRelay {
		go t.holdRelays(&observed)
		go t.refreshMeasured(mine, &measured)
	}
	if len(coords) > 0 {
		go t.lease(coord)
	}
	if !cfg.NoLAN {
		lan, err := discovery.Start(h, t.found)
		if err != nil {
			// t.Close, not h.Close: the refresher and the relay dialler
			// are already running on t.ctx, so closing only the
			// host leaves them asking reflectors for the life of the
			// process.
			t.Close()
			return nil, err
		}
		t.lan = lan
	}
	t.diagnose()
	return t, nil
}

// wrap builds a Host around a started libp2p host. Everything that has
// to stop on Close hangs off ctx, so background work cannot be added
// without a parent that stops it.
func wrap(h host.Host) *Host {
	t := &Host{h: h, relays: newRelaySet()}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	return t
}

// found passes one discovery answer to every OnLAN callback.
func (t *Host) found(info peer.AddrInfo) {
	t.mu.Lock()
	fns := append(t.onLAN[:0:0], t.onLAN...)
	t.mu.Unlock()
	addrs := p2pAddrs(info)
	for _, fn := range fns {
		fn(PeerID(info.ID.String()), addrs)
	}
}

// refreshMeasured re-measures this machine's public addresses every
// endpointsFresh, so the published set stays younger than a NAT mapping.
// Measuring once at startup describes a door the carrier has since
// moved. The set is published through the AddrsFactory above, so peers
// are told as it changes with nothing extra on the wire.
func (t *Host) refreshMeasured(mine *socketRef, measured *atomic.Value) {
	for {
		var out []multiaddr.Multiaddr
		for _, a := range mine.Addrs(2 * time.Second) {
			if ma, ok := quicAddr(a); ok && !has(out, ma) {
				out = append(out, ma)
			}
		}
		if len(out) > 0 {
			measured.Store(out)
		}
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(endpointsFresh):
		}
	}
}

// holdRelays dials every configured relay and asks each one what address
// it sees, at startup and again whenever this machine's addresses change.
//
// It breaks a circle: AutoRelay reserves only after AutoNAT rules this
// machine unreachable, AutoNAT needs a peer to ask, and an idle machine
// has none. The relay is that peer, the host to reserve with, and the
// observer of this machine's public address all at once.
//
// It re-runs on an address change because a machine that moves from
// Wi-Fi to a cable, or wakes on a different network, has a new address
// to be observed at and, if the move dropped the relay connection, no
// reservation left to be reached through. Neither is recovered by the
// dial made at startup.
//
// It re-runs when the relay set is replaced, because a machine the
// coordinator has just moved has a reservation to make and an address to
// be observed at, and neither waits for the next tick.
//
// It re-runs on relayRetry as well, because a relay that restarts is a
// change to nothing on this machine and so raises no event here. A round
// costs one dial and a kilobyte per relay, and it is what puts a pair
// back in touch after the machine between them was rebooted.
func (t *Host) holdRelays(observed *atomic.Value) {
	tick := time.NewTicker(relayRetry)
	defer tick.Stop()

	// Which relays have already been reported unreachable, so an outage
	// costs two lines rather than one a minute for as long as it lasts.
	down := map[peer.ID]bool{}

	// A nil channel blocks forever, so a subscription that could not be
	// made costs the address-change round and leaves the timed one.
	var changed <-chan interface{}
	if sub, err := t.h.EventBus().Subscribe(new(event.EvtLocalAddressesUpdated)); err == nil {
		defer sub.Close()
		changed = sub.Out()
	}
	for {
		t.dialRelays(t.relays.peers(), observed, down)
		select {
		case <-t.ctx.Done():
			return
		case <-tick.C:
		case <-t.relays.changed:
		case _, ok := <-changed:
			if !ok {
				return
			}
		}
	}
}

// relayRetry is how often a relay is dialled again when nothing has
// changed. Slower than a peer is chased on purpose: a relay that
// restarts is back within seconds, and one that is gone for good should
// cost a dial a minute rather than a dial every five seconds for the
// life of the process.
const relayRetry = time.Minute

// dialRelays connects to each relay once and stores the address it
// reports, marking in down which ones did not answer. One attempt each,
// in the background: a relay that is down means running without one, not
// failing to start, and the next round comes soon enough.
func (t *Host) dialRelays(relays []peer.AddrInfo, observed *atomic.Value, down map[peer.ID]bool) {
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()
	for _, r := range relays {
		if err := t.h.Connect(ctx, r); err != nil {
			if !down[r.ID] {
				fmt.Fprintf(os.Stderr, "relay %s unreachable: %v\n", r.ID, err)
				down[r.ID] = true
				// A relay that was already dead when it was handed out
				// never disconnects, so nothing else says so. Ask the
				// coordinator for another one now rather than at the
				// next renewal, the same as when a live relay dies.
				//
				// Once per relay, because down is only set here: a
				// coordinator that answers with the same dead relay is
				// not asked again until something changes, which is what
				// stops the pair of them spinning.
				poke(t.relays.lost)
			}
			continue
		}
		if down[r.ID] {
			fmt.Fprintf(os.Stderr, "relay %s is back\n", r.ID)
			down[r.ID] = false
		}
		if a, ok := askObserved(ctx, t.h, r.ID); ok {
			observed.Store([]multiaddr.Multiaddr{a})
		}
	}
}

// askObserved reads one relay's view of this machine's address.
// Failure is silent: with no answer the host advertises only what it can
// see itself, which is better than a guessed address that costs every
// peer a dial that can never arrive.
func askObserved(ctx context.Context, h host.Host, relay peer.ID) (multiaddr.Multiaddr, bool) {
	s, err := h.NewStream(ctx, relay, wire.ObservedProto)
	if err != nil {
		return nil, false
	}
	defer s.Close()
	s.SetDeadline(time.Now().Add(10 * time.Second))
	b, err := io.ReadAll(io.LimitReader(s, 256))
	if err != nil {
		return nil, false
	}
	return usableObserved(string(b))
}

// punchFilter narrows a hole punch to addresses this machine can route,
// and widens it to where this machine can be reached right now.
//
// Both halves matter: a punch aimed at an address that cannot be routed
// is a dial one side never makes, and a punch is only a punch when both
// sides dial.
type punchFilter struct {
	hasIPv6 func() bool
	now     *socketRef // nil until a socket exists, and in tests
}

// FilterLocal adds the address this socket answers on now.
//
// It is the last point before the peer is told where to punch, so on a
// carrier whose ports move it is the only point where the answer is
// still true — the addresses arriving here carry an observation from
// startup that has since aged.
//
// The measurement is added rather than substituted: the published list
// may already be right, and every candidate is dialled, so being wrong
// costs one extra dial and being right saves the punch. With no answer
// the set is left alone.
func (f punchFilter) FilterLocal(_ peer.ID, as []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	if f.now == nil {
		return as
	}
	// Two seconds is the whole budget: the punch is on a clock, and an
	// answer later than that arrives too late to still be true. It
	// usually costs nothing, since the refresher has measured within
	// endpointsFresh and this reads its cache.
	mapped := f.now.Addrs(2 * time.Second)
	if len(mapped) == 0 {
		return as
	}
	out := append(as[:0:0], as...)
	for _, m := range mapped {
		if fresh, ok := quicAddr(m); ok && !has(out, fresh) {
			out = append(out, fresh)
		}
	}
	return out
}

func has(as []multiaddr.Multiaddr, a multiaddr.Multiaddr) bool {
	for _, have := range as {
		if have.Equal(a) {
			return true
		}
	}
	return false
}

func (f punchFilter) FilterRemote(_ peer.ID, as []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	if f.hasIPv6() {
		return as
	}
	out := as[:0:0]
	for _, a := range as {
		if _, err := a.ValueForProtocol(multiaddr.P_IP6); err == nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// hasGlobalIPv6 reports whether any interface holds a routable IPv6
// address. Link-local and unique-local do not count: neither reaches a
// peer on the Internet, and both appear on machines with no IPv6 at all.
func hasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() != nil {
			continue
		}
		if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return true
		}
	}
	return false
}

// cgnat is carrier-grade NAT space: the ISP's own network, reachable
// from inside it and nowhere else.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// quicAddr turns a reflector's "ip:port" into a dialable address, held
// to the same standard as one a relay reported.
//
// The two observers answer in different formats: a relay replies with a
// full multiaddr, a reflector with a host and port. Passing the second
// to a parser expecting the first fails silently and discards every
// measurement.
func quicAddr(hostPort string) (multiaddr.Multiaddr, bool) {
	ip, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return nil, false
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 {
		return nil, false
	}
	family := "ip4"
	if !addr.Unmap().Is4() {
		family = "ip6"
	}
	return usableObserved(fmt.Sprintf("/%s/%s/udp/%s/quic-v1", family, addr.Unmap(), port))
}

// usableObserved accepts an address only if advertising it is a promise
// this machine can keep.
//
// UDP only, because what was measured is a UDP mapping; an inbound TCP
// connection needs a forwarded port, so a TCP address here would invent
// a route. Public only, because an address a third party cannot dial is
// worse than none.
func usableObserved(s string) (multiaddr.Multiaddr, bool) {
	ma, err := multiaddr.NewMultiaddr(strings.TrimSpace(s))
	if err != nil || transportOf(ma) != "quic" || pathOf(ma) != PathDirect {
		return nil, false
	}
	ip, err := manet.ToIP(ma)
	if err != nil {
		return nil, false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || cgnat.Contains(addr.Unmap()) {
		return nil, false
	}
	return ma, true
}

// parseAddrs turns relay addresses into peer records, failing on the
// first bad one rather than dropping it quietly.
func parseAddrs(addrs []string) ([]peer.AddrInfo, error) {
	var out []peer.AddrInfo
	for _, a := range addrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			return nil, fmt.Errorf("bad address %q: %w", a, err)
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			return nil, fmt.Errorf("address %q names no peer: %w", a, err)
		}
		out = append(out, *info)
	}
	return out, nil
}

// directOnly drops relay addresses, leaving only those that reach a peer
// without one.
func directOnly(as []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	out := as[:0:0]
	for _, a := range as {
		if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err != nil {
			out = append(out, a)
		}
	}
	return out
}

// openStream starts a stream, dialling first if there is no connection.
// It takes the best connection open at that moment, so a stream opened
// after an upgrade gets the direct path without anything switching over.
//
// libp2p refuses streams on a relayed connection unless the dial says
// otherwise. This says otherwise: relayed is slow and metered, but it
// works, and the alternative is no connection at all.
func (t *Host) openStream(ctx context.Context, id peer.ID, p protocol.ID) (network.Stream, error) {
	s, err := t.h.NewStream(network.WithAllowLimitedConn(ctx, "ratatoskr"), id, p)
	if err != nil {
		return nil, why(err)
	}
	return s, nil
}

// pathTo reports the best way this machine currently reaches a peer.
// An upgrade leaves the relayed connection open beside the new one, so
// this takes the best of them rather than the first one found.
func (t *Host) pathTo(id peer.ID) Path {
	best := PathUnknown
	for _, c := range t.h.Network().ConnsToPeer(id) {
		if p := pathOfConn(c); p.BetterThan(best) {
			best = p
		}
	}
	return best
}

func pathOfConn(c network.Conn) Path { return pathOf(c.RemoteMultiaddr()) }

// Close stops everything this host started. Calling it twice is safe,
// which matters because an error path can close a host that a defer
// will close again.
//
// The background work stops before the host does, not after. Closing the
// host disconnects every peer, and each of those disconnections is an
// event that would otherwise start a fresh chase for a peer on a
// transport that is going away.
func (t *Host) Close() error {
	t.closeOnce.Do(func() {
		t.cancel()
		if t.lan != nil {
			t.lan.Close()
		}
		t.closeWatchers()
	})
	return t.h.Close()
}

func transportOf(a multiaddr.Multiaddr) string {
	s := a.String()
	switch {
	case strings.Contains(s, "/quic"):
		return "quic"
	case strings.Contains(s, "/tcp"):
		return "tcp"
	}
	return "unknown"
}

// pathOf classifies the far end from its address. Carrier-grade NAT
// (100.64.0.0/10) is deliberately not local: it is the ISP's network,
// not yours, and reaching a peer through it is an Internet path.
func pathOf(a multiaddr.Multiaddr) Path {
	if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
		return PathRelay
	}
	ip, err := manet.ToIP(a)
	if err != nil {
		return PathUnknown
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return PathUnknown
	}
	if addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return PathLAN
	}
	return PathDirect
}
