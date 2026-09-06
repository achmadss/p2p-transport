// Package transport carries Ratatoskr's bytes between two peers.
//
// It is built on libp2p. The Noise handshake proves who the remote peer
// is, so there is no separate challenge-response here; see PLAN.md §5.
// What a proven peer may then read is a question for the trust list and
// for mimir-signed grants, and is decided a layer above this one.
package transport

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// EchoProto is the step 0 scaffold. It exists to prove a Noise-secured
// QUIC stream carries bytes end to end, and is deleted once the real
// control and transfer protocols of PLAN.md §9 replace it.
const EchoProto = protocol.ID("/ratatoskr/echo/1.0.0")

// BenchProto measures a path. The far end sinks bytes and reports how
// many arrived; sending them back would double the relay's bill and
// halve the number. Deleted with EchoProto when the File API lands.
const BenchProto = protocol.ID("/ratatoskr/bench/1.0.0")

// ObservedProto asks the far end for the address it sees us at.
//
// A reflector on any other socket cannot answer this. A NAT that keeps
// the mapping endpoint-independent but renumbers the port gives each
// socket its own external port, so the port a throwaway STUN socket
// learns is not the port libp2p punches from — which is exactly what a
// phone hotspot does, and exactly why the punch was one-sided. Asked on
// the connection itself, the answer is the QUIC socket's own address.
//
// One observer is enough here only because the mapping class is
// established separately by `ratatoskr natcheck`: on an endpoint-
// independent NAT the address heimdall sees is the address any peer may
// use, and on any other kind no single address exists to be found.
const ObservedProto = protocol.ID("/ratatoskr/observed/1.0.0")

// Path is how a session reached the far end. It is measured from a live
// connection, never guessed. PLAN.md §14.
type Path string

const (
	PathUnknown Path = "unknown"
	PathLAN     Path = "lan"
	PathDirect  Path = "direct"
	PathRelay   Path = "relay"
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

// Host is a libp2p node in either role. Ratatoskr is one binary that
// serves and consumes, so there is no separate client type.
type Host struct {
	h host.Host
}

// New starts a host under the given identity.
//
// The transport and security lists are explicit rather than left to
// libp2p's defaults. The defaults would also enable WebTransport and TLS,
// and which transports exist is a decision of PLAN.md §4, not something
// to inherit from a dependency's default and discover later.
// Relays are heimdall addresses. With none the host is LAN-only, which
// is a complete way to run and not a degraded one.
func New(key crypto.PrivKey, relays []string) (*Host, error) {
	infos, err := ParseAddrs(relays)
	if err != nil {
		return nil, err
	}

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
		libp2p.EnableHolePunching(),
		// Ask the home router to forward a port, the way a torrent
		// client does. This is what makes a relay a genuine last
		// resort rather than the only route: a machine with a
		// forwarded port is dialled directly by peers that could
		// never be dialled themselves. It is also the only answer
		// to a symmetric carrier NAT, which no amount of hole
		// punching opens — the phone dials out, the house listens.
		libp2p.NATPortMap(),
	}
	// The address a reflector sees us at, once one has been asked and
	// believed. Empty until then, and empty forever on a NAT that will
	// not answer for it; see internal/stun.
	var public atomic.Value
	opts = append(opts, libp2p.AddrsFactory(func(as []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		a, ok := public.Load().(multiaddr.Multiaddr)
		if !ok {
			return as
		}
		for _, have := range as {
			if have.Equal(a) {
				return as
			}
		}
		out := make([]multiaddr.Multiaddr, len(as), len(as)+1)
		copy(out, as)
		return append(out, a)
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

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("start host: %w", err)
	}
	HandleObserved(h)
	go holdRelays(h, infos, &public)
	return &Host{h: h}, nil
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
func holdRelays(h host.Host, relays []peer.AddrInfo, public *atomic.Value) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, r := range relays {
		if err := h.Connect(ctx, r); err != nil {
			fmt.Fprintf(os.Stderr, "relay %s unreachable: %v\n", r.ID, err)
			continue
		}
		if a, ok := askObserved(ctx, h, r.ID); ok {
			public.Store(a)
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
	s, err := h.NewStream(ctx, relay, ObservedProto)
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

// HandleObserved answers ObservedProto with the address this connection
// came from. Heimdall serves it; every agent also does, so two peers on
// one LAN can name each other without a relay in the room.
func HandleObserved(h host.Host) {
	h.SetStreamHandler(ObservedProto, func(s network.Stream) {
		defer s.Close()
		s.SetDeadline(time.Now().Add(10 * time.Second))
		io.WriteString(s, s.Conn().RemoteMultiaddr().String())
	})
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

// Host exposes the libp2p host to packages that need to attach to it,
// such as discovery. Nothing above the transport layer should reach for
// this; it is here because mDNS advertises the host itself.
func (t *Host) Host() host.Host { return t.h }

// ParseAddrs turns full multiaddrs into peer records, failing on the
// first bad one rather than quietly dropping it.
func ParseAddrs(addrs []string) ([]peer.AddrInfo, error) {
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

// ID is this peer's identity, derived from its public key.
func (t *Host) ID() peer.ID { return t.h.ID() }

// Addrs are the full multiaddrs a remote peer can dial, identity
// included. Diagnostics only: SPEC.md §30.4 keeps multiaddrs out of
// ordinary user-facing output.
func (t *Host) Addrs() ([]multiaddr.Multiaddr, error) {
	return peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: t.h.ID(), Addrs: t.h.Addrs()})
}

// Handle registers a handler for a protocol.
func (t *Host) Handle(p protocol.ID, fn network.StreamHandler) {
	t.h.SetStreamHandler(p, fn)
}

// Dial connects to a peer named by a full multiaddr and opens a stream.
// The Noise handshake inside proves the far end holds the private key
// for the peer id in that address; a mismatch fails the dial.
func (t *Host) Dial(ctx context.Context, addr string, p protocol.ID) (network.Stream, error) {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return nil, fmt.Errorf("bad address: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return nil, fmt.Errorf("address names no peer: %w", err)
	}
	return t.DialPeer(ctx, *info, p)
}

// DialPeer opens a stream to an already-located peer.
//
// Circuit addresses are dropped from the dial set. A peer found on the
// local network must be reached over the local network or not at all —
// silently relaying through heimdall would send bytes off a network the
// user believed they never left. PLAN.md §6.
func (t *Host) DialPeer(ctx context.Context, info peer.AddrInfo, p protocol.ID) (network.Stream, error) {
	direct := info.Addrs[:0:0]
	for _, a := range info.Addrs {
		if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err != nil {
			direct = append(direct, a)
		}
	}
	if len(direct) == 0 {
		return nil, fmt.Errorf("no direct address for %s", info.ID)
	}
	info.Addrs = direct
	return t.dial(ctx, info, p)
}

// DialRelayed reaches a peer through a relay. The caller has chosen to
// leave the local network, so circuit addresses are kept.
func (t *Host) DialRelayed(ctx context.Context, info peer.AddrInfo, p protocol.ID) (network.Stream, error) {
	return t.dial(ctx, info, p)
}

func (t *Host) dial(ctx context.Context, info peer.AddrInfo, p protocol.ID) (network.Stream, error) {
	if err := t.h.Connect(ctx, info); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	// libp2p treats a relayed connection as limited and refuses streams
	// on it unless asked. Ratatoskr wants them: a relayed path is slow
	// and metered, but it is a working path, and the alternative is no
	// connection at all.
	s, err := t.h.NewStream(network.WithAllowLimitedConn(ctx, "ratatoskr"), info.ID, p)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return s, nil
}

// PathTo reports how this machine currently reaches a peer, across every
// open connection. A relayed connection that DCUtR later upgrades leaves
// a direct one here, which is how the upgrade is observed rather than
// assumed.
func (t *Host) PathTo(id peer.ID) Path {
	best := PathUnknown
	for _, c := range t.h.Network().ConnsToPeer(id) {
		switch p := pathOfConn(c); p {
		case PathLAN, PathDirect:
			return p
		case PathRelay:
			best = p
		}
	}
	return best
}

func pathOfConn(c network.Conn) Path { return pathOf(c.RemoteMultiaddr()) }

func (t *Host) Close() error { return t.h.Close() }

// Conn describes one live connection, as measured.
type Conn struct {
	Peer      peer.ID
	Addr      multiaddr.Multiaddr
	Transport string // quic | tcp | ws
	Path      Path
}

// Describe reports how a connection actually reached its far end. It
// reads the live connection rather than the intent that opened it,
// because presence and path are different questions. PLAN.md §14.
func Describe(c network.Conn) Conn {
	addr := c.RemoteMultiaddr()
	return Conn{
		Peer:      c.RemotePeer(),
		Addr:      addr,
		Transport: transportOf(addr),
		Path:      pathOf(addr),
	}
}

func transportOf(a multiaddr.Multiaddr) string {
	s := a.String()
	switch {
	case strings.Contains(s, "/quic"):
		return "quic"
	case strings.Contains(s, "/ws"), strings.Contains(s, "/wss"):
		return "ws"
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
