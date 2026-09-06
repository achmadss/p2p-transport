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
	"net/netip"
	"os"
	"strings"

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
	if len(infos) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(infos))
	}
	// AutoNAT decides whether a relay reservation is needed, and on a
	// loopback test it correctly decides no. This forces the answer so
	// the relay path can be exercised without two real networks.
	if os.Getenv("RATATOSKR_FORCE_PRIVATE") != "" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("start host: %w", err)
	}
	return &Host{h: h}, nil
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
