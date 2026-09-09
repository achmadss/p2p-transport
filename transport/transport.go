// Package transport carries bytes between two machines over the best
// path it can find, and tells the caller only what the caller genuinely
// needs: who the far end is, a stream to it, and which path the bytes
// are taking.
//
// api.go is the whole surface. Everything else in this package is below
// the seam — an application never names a transport, a handshake, an
// address format or a traversal technique, and never imports libp2p to
// use this. PLAN.md §2 is the contract; SPEC.md §4 lists the words that
// do not cross it.
//
// The handshake proves who the far end is, so there is no separate
// challenge-response. What a proven machine may then do is not asked
// here at all: identity is this layer's, authorisation is the
// application's.
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
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// listenAddrs is every interface on an operating-system-assigned port.
// QUIC is listed first so it is preferred; TCP stays for networks that
// drop UDP.
var listenAddrs = []string{
	"/ip4/0.0.0.0/udp/0/quic-v1",
	"/ip6/::/udp/0/quic-v1",
	"/ip4/0.0.0.0/tcp/0",
	"/ip6/::/tcp/0",
}

// Host is this machine on the network, in both roles at once: it serves
// the protocols registered on it and opens streams to other machines.
// There is no separate client type, because no machine here is only one
// of the two.
type Host struct {
	h host.Host

	// lan is local discovery, started here rather than by the caller so
	// that OnLAN is the only way anyone above sees it. Nil when Config
	// turned it off.
	lan *discovery.LAN

	mu    sync.Mutex
	onLAN []func(PeerID, []string)

	// done stops the background work — measuring this machine's public
	// addresses, and retrying the punch on a relayed peer — when the
	// host closes. Without it every test that starts a host leaves two
	// goroutines asking reflectors for the life of the run.
	done      chan struct{}
	closeOnce sync.Once

	// working names the peers a repair is already running for — a punch
	// up the ladder or a redial back down it — so that a second
	// connection event does not start a second one.
	working sync.Map // peer.ID -> struct{}

	// circuits are the configured relays as dialable addresses, kept so
	// the bottom rung can be reached again after every other one has
	// gone. Empty on a LAN-only agent, which then has no bottom rung
	// and says so by failing rather than by hanging.
	circuits []multiaddr.Multiaddr
}

// New starts this machine's transport, loading or generating its
// identity in Config.Dir. Close stops it.
func New(cfg Config) (*Host, error) {
	id, err := identity.LoadOrCreate(cfg.Dir)
	if err != nil {
		return nil, err
	}
	key := id.PrivateKey()
	relays := cfg.Relays
	infos, err := parseAddrs(relays)
	if err != nil {
		return nil, err
	}

	// The socket has to be wrapped before libp2p opens it and asked
	// long after, so the handle is made first and filled in by the
	// option itself.
	mine, socketOpt := ownSocket()

	// The transport and security lists are explicit rather than left to
	// libp2p's defaults. The defaults would also enable WebTransport and
	// TLS, and which transports exist is a decision of PLAN.md §3, not
	// something to inherit from a dependency's default and discover
	// later.
	opts := []libp2p.Option{
		libp2p.Identity(key),
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Transport(quic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
		// AutoNAT learns whether we are reachable; DCUtR turns a relayed
		// connection into a direct one when both ends can be punched
		// through. Both are why the relay is a fallback and not a bill.
		libp2p.EnableNATService(),
		// v1 asks a peer to dial back and takes a majority verdict; v2
		// asks one peer about one address and is told which. That
		// matters here because a private drive never has the several
		// independent peers v1 wants, which is the same shortage that
		// makes ForceReachabilityPrivate necessary below. v0.49 ships
		// it opt-in, and it runs beside v1 rather than replacing it.
		libp2p.EnableAutoNATv2(),
		libp2p.EnableHolePunching(holepunch.WithAddrFilter(punchFilter{hasIPv6: hasGlobalIPv6, now: mine})),
		// Ask the home router to forward a port, the way a torrent
		// client does. This is what makes a relay a genuine last
		// resort rather than the only route: a machine with a
		// forwarded port is dialled directly by peers that could
		// never be dialled themselves. It is also the only answer
		// to a symmetric carrier NAT, which no amount of hole
		// punching opens — the phone dials out, the house listens.
		libp2p.NATPortMap(),
	}
	// Where the world says this machine is, from two observers that fail
	// in different ways. `observed` is what a relay saw when the agent
	// connected to it, which is right on a NAT that keeps its mappings
	// and stale within minutes on one that does not. `measured` is what
	// several reflectors see on libp2p's own socket right now, refreshed
	// on the endpointsFresh clock. Both are empty until something
	// answers, and on a NAT that will answer for neither they stay
	// empty rather than publishing a guess.
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

	if len(infos) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(infos))
	}
	// AutoRelay reserves a slot only once AutoNAT has ruled this machine
	// unreachable, and AutoNAT wants several independent peers to agree
	// before it rules anything. A private drive has one relay and its
	// owner's two laptops, so it never concludes: no verdict, no
	// reservation, and a machine nobody can dial is not a drive. Naming
	// a relay in the config is the owner saying they expect to need one,
	// which is the answer AutoNAT could not reach on its own.
	//
	// A machine that really is reachable loses nothing: it keeps
	// advertising its direct addresses, and peers prefer them.
	if len(infos) > 0 && os.Getenv("RATATOSKR_ASSUME_PUBLIC") == "" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}

	opts = append(opts, socketOpt)

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("start host: %w", err)
	}
	wire.HandleObserved(h)
	handleAddrs(h)
	// ParseAddrs has already refused anything unparseable above, so a
	// relay that fails here is one the circuit suffix broke, which
	// cannot happen for an address that parsed.
	var circuits []multiaddr.Multiaddr
	for _, r := range relays {
		if a, err := multiaddr.NewMultiaddr(r + "/p2p-circuit"); err == nil {
			circuits = append(circuits, a)
		}
	}
	t := &Host{h: h, done: make(chan struct{}), circuits: circuits}
	t.watchForRelayed()
	// Both of these exist to get off a relay, so neither runs without
	// one. A LAN-only agent has no peer that could be told a public
	// address and no relayed connection to escape, and asking eight
	// reflectors every 27 seconds for the life of an idle laptop is
	// traffic nobody asked for.
	if len(infos) > 0 {
		go holdRelays(h, infos, &observed)
		go t.refreshMeasured(mine, &measured)
	}
	if !cfg.NoLAN {
		lan, err := discovery.Start(h, t.found)
		if err != nil {
			// t.Close, not h.Close: the refresher and the relay dialler
			// are already running and watch t.done, so closing only the
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

// found fans one mDNS answer out to whoever registered for it.
func (t *Host) found(info peer.AddrInfo) {
	t.mu.Lock()
	fns := append(t.onLAN[:0:0], t.onLAN...)
	t.mu.Unlock()
	addrs := p2pAddrs(info)
	for _, fn := range fns {
		fn(PeerID(info.ID.String()), addrs)
	}
}

// refreshMeasured keeps this machine's public address set younger than
// a NAT mapping's life.
//
// This is the half of Tailscale's answer that lives outside the punch.
// Their endpoints are re-measured before every signalling round rather
// than once at startup, because a set measured at startup describes a
// door the carrier has since moved. Ours is published through the
// AddrsFactory above, which means identify pushes it to every connected
// peer as it changes — so a peer that is about to punch is told where
// to aim by the ordinary machinery, with nothing new on the wire.
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
		case <-t.done:
			return
		case <-time.After(endpointsFresh):
		}
	}
}

// holdRelays dials every configured relay at startup and asks each one
// where it sees us.
//
// AutoRelay only reserves a slot once AutoNAT has decided this machine
// is unreachable, and AutoNAT cannot decide anything without a peer to
// ask. An idle agent has no peers, so it kept no reservation, reached no
// verdict, and stayed unreachable from anywhere but its own LAN — a
// drive nobody can dial is not a drive. Dialling the relay breaks the
// circle: it is the peer AutoNAT needs, the host AutoRelay reserves
// with, and the observer that names our public address.
//
// One attempt each, in the background, because a relay that is down is a
// reason to run LAN-only rather than a reason not to start. A machine
// that changes network keeps the old address until it restarts.
// ponytail: re-ask on EvtLocalAddressesUpdated when roaming matters.
func holdRelays(h host.Host, relays []peer.AddrInfo, observed *atomic.Value) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, r := range relays {
		if err := h.Connect(ctx, r); err != nil {
			fmt.Fprintf(os.Stderr, "relay %s unreachable: %v\n", r.ID, err)
			continue
		}
		if a, ok := askObserved(ctx, h, r.ID); ok {
			observed.Store([]multiaddr.Multiaddr{a})
		}
	}
}

// askObserved reads one relay's view of this machine's address.
//
// Failure is silent and total: without an answer the host advertises
// only what it can see itself, which means LAN and the relay. That is a
// worse drive, not a broken one, and it is honest — advertising a
// guessed address costs every peer a dial that can never arrive.
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

// punchFilter keeps a hole punch to an address family this machine can
// actually route.
//
// A phone hotspot hands out real IPv6 and a fixed line here does not, so
// the far peer offers four IPv6 addresses and one IPv4. Every IPv6 dial
// fails instantly with "no route to host", and the one address that
// could have worked is left to whatever is still on the clock. An
// address we cannot reach is also an address we never punch towards, and
// a punch only one side makes is not a punch.
//
// Nothing is filtered when this machine does have IPv6: two peers that
// both have it should meet over it and skip the NAT entirely.
type punchFilter struct {
	hasIPv6 func() bool
	now     *socketRef // nil until a socket exists, and in tests
}

// FilterLocal names this socket now, rather than repeating what a relay
// saw when the agent started.
//
// This is the last point before DCUtR tells the peer where to punch, and
// on a carrier whose port creeps it is the only point where the answer
// is still true. The addresses arriving here come from host.Addrs(),
// which carries the relay's observation from startup; the relay's own
// mapping never moves, so that observation ages while the port a fresh
// peer would reach walks away from it. Measured here it cannot age: the
// peer dials within a second or two of being told.
//
// A measured address is added rather than substituted. It is one
// reflector's word about a socket, the published list may already be
// right, and libp2p punches at every candidate it is given — so being
// wrong here costs one extra dial and being right saves the punch.
// Everything is left alone when no reflector answers, which is the
// behaviour this had before.
func (f punchFilter) FilterLocal(_ peer.ID, as []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	if f.now == nil {
		return as
	}
	// Two seconds is the whole budget: DCUtR is on a clock, and a
	// reflector that has not answered by then would arrive too late to
	// be true anyway. In practice this costs nothing, because the
	// refresher has already measured within endpointsFresh and this
	// reads its cache.
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

// hasGlobalIPv6 reports whether any interface holds a globally routable
// IPv6 address. Link-local and unique-local do not count: neither
// reaches a peer on the Internet, and both are handed out by machines
// with no IPv6 service at all.
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
// from inside it and from nowhere else.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// usableObserved accepts an observed address only if advertising it
// would be a promise this machine can keep.
//
// QUIC only, because the measurement is of a UDP mapping: an inbound TCP
// handshake needs a forwarded port rather than a punched one, so a TCP
// address here would be inventing a route. Public only, because an
// address a third party cannot dial is worse than no address at all.
// quicAddr turns a reflector's "ip:port" into the QUIC multiaddr a peer
// can dial, holding it to the same standard as an address a relay
// reported.
//
// The two observers speak different languages and the difference is easy
// to miss: ObservedProto answers with a multiaddr because it is libp2p
// talking to libp2p, and STUN answers with a host and port because it
// predates all of this. Handing the second to a parser expecting the
// first fails silently and every measurement is discarded — which is
// exactly what happened, with the measured address printed in the log
// immediately above the punch that ignored it.
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

// parseAddrs turns full multiaddrs into peer records, failing on the
// first bad one rather than quietly dropping it.
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

// directOnly drops circuit addresses. Two callers want this and want it
// for the same reason: an address that goes through a relay is not a
// direct path, whether it is being refused (DialPeer) or dialled past
// (dialDirect).
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
//
// The path is whatever the connection manager considers best right now,
// which after an upgrade is the direct connection rather than the relay
// the session started on. That is the whole benefit of upgrading: a
// stream opened later takes the better path without anything switching
// over, and a stream opened earlier stays where it was born.
//
// libp2p treats a relayed connection as limited and refuses streams on
// it unless asked. This asks: a relayed path is slow and metered, but
// it is a working path, and the alternative is no connection at all.
func (t *Host) openStream(ctx context.Context, id peer.ID, p protocol.ID) (network.Stream, error) {
	s, err := t.h.NewStream(network.WithAllowLimitedConn(ctx, "ratatoskr"), id, p)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return s, nil
}

// pathTo reports the best way this machine currently reaches a peer,
// across every open connection. An upgrade leaves the relayed
// connection in place beside the new one, so the answer is the best of
// them and not the first one found — which is how the upgrade is
// observed rather than assumed.
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
func (t *Host) Close() error {
	err := t.h.Close()
	t.closeOnce.Do(func() {
		close(t.done)
		if t.lan != nil {
			t.lan.Close()
		}
	})
	return err
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
