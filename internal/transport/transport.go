// Package transport carries Ratatoskr's bytes between two peers.
//
// It is built on libp2p. The Noise handshake proves who the remote peer
// is, so there is no separate challenge-response here; see PLAN.md §5.
// What a proven peer may then read is a question for the trust list and
// for mimir-signed grants, and is decided a layer above this one.
package transport

import (
	"context"
	"crypto/rand"
	"fmt"
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
)

// EchoProto is the step 0 scaffold. It exists to prove a Noise-secured
// QUIC stream carries bytes end to end, and is deleted once the real
// control and transfer protocols of PLAN.md §9 replace it.
const EchoProto = protocol.ID("/ratatoskr/echo/1.0.0")

// Path is how a session reached the far end. It is measured from a live
// connection, never guessed. PLAN.md §14.
type Path string

const (
	PathUnknown Path = "unknown"
	PathLAN     Path = "lan"
	PathDirect  Path = "direct"
	PathRelay   Path = "relay"
)

// Options configures a Host. The zero value listens on an
// operating-system-assigned port on every interface, over both QUIC and
// TCP, and generates a throwaway identity.
type Options struct {
	// Key is this peer's long-lived private key. When nil a new Ed25519
	// key is generated and discarded on exit, which is what the dev
	// commands want and what step 1 replaces.
	Key crypto.PrivKey

	// ListenAddrs overrides the default listen set.
	ListenAddrs []string
}

func (o Options) listenAddrs() []string {
	if len(o.ListenAddrs) > 0 {
		return o.ListenAddrs
	}
	return []string{
		"/ip4/0.0.0.0/udp/0/quic-v1",
		"/ip6/::/udp/0/quic-v1",
		"/ip4/0.0.0.0/tcp/0",
		"/ip6/::/tcp/0",
	}
}

// Host is a libp2p node in either role. Ratatoskr is one binary that
// serves and consumes, so there is no separate client type.
type Host struct {
	h host.Host
}

// New starts a host. QUIC is listed first so it is preferred; TCP is the
// fallback for networks that drop UDP.
func New(opts Options) (*Host, error) {
	key := opts.Key
	if key == nil {
		var err error
		key, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate identity: %w", err)
		}
	}

	h, err := libp2p.New(
		libp2p.Identity(key),
		libp2p.ListenAddrStrings(opts.listenAddrs()...),
		libp2p.Transport(quic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Security(noise.ID, noise.New),
	)
	if err != nil {
		return nil, fmt.Errorf("start host: %w", err)
	}
	return &Host{h: h}, nil
}

// ID is this peer's identity, derived from its public key.
func (t *Host) ID() peer.ID { return t.h.ID() }

// Addrs are the full multiaddrs a remote peer can dial, identity
// included. Diagnostics only: SPEC.md §30.4 keeps multiaddrs out of
// ordinary user-facing output.
func (t *Host) Addrs() []string {
	var out []string
	for _, a := range t.h.Addrs() {
		out = append(out, a.String()+"/p2p/"+t.h.ID().String())
	}
	return out
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
	if err := t.h.Connect(ctx, *info); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	s, err := t.h.NewStream(ctx, info.ID, p)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return s, nil
}

func (t *Host) Close() error { return t.h.Close() }

// Conn describes one live connection, as measured.
type Conn struct {
	Peer      peer.ID
	Addr      string
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
		Addr:      addr.String(),
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

func pathOf(a multiaddr.Multiaddr) Path {
	if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
		return PathRelay
	}
	ip, err := a.ValueForProtocol(multiaddr.P_IP4)
	if err != nil {
		if ip, err = a.ValueForProtocol(multiaddr.P_IP6); err != nil {
			return PathUnknown
		}
	}
	if isPrivate(ip) {
		return PathLAN
	}
	return PathDirect
}

func isPrivate(ip string) bool {
	return strings.HasPrefix(ip, "10.") ||
		strings.HasPrefix(ip, "192.168.") ||
		strings.HasPrefix(ip, "127.") ||
		strings.HasPrefix(ip, "169.254.") ||
		strings.HasPrefix(ip, "fe80:") ||
		strings.HasPrefix(ip, "fc") || strings.HasPrefix(ip, "fd") ||
		ip == "::1" ||
		isCarrierRange(ip)
}

// 172.16.0.0/12 and 100.64.0.0/10, spelled out rather than parsed so the
// check stays one function with no error path.
func isCarrierRange(ip string) bool {
	var a, b int
	if n, _ := fmt.Sscanf(ip, "%d.%d.", &a, &b); n != 2 {
		return false
	}
	if a == 172 && b >= 16 && b <= 31 {
		return true
	}
	return a == 100 && b >= 64 && b <= 127
}
