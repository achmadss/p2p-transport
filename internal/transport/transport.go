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
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
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

// AddrsProto asks a peer where it is listening, in its own words.
//
// identify already carries this and the receiving side throws half of
// it away: it drops every non-public address when the connection they
// arrived on is public, and a circuit through a relay on a VPS is a
// public address (go-libp2p `identify.filterAddrs`). So two machines on
// one LAN that meet over heimdall are never told each other's LAN
// address — the one dial certain to succeed is the one dial never
// tried. DCUtR does not rescue it either; it only ever direct-dials
// addresses it considers public.
const AddrsProto = protocol.ID("/ratatoskr/addrs/1.0.0")

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

	// done stops the background work — measuring this machine's public
	// addresses, and retrying the punch on a relayed peer — when the
	// host closes. Without it every test that starts a host leaves two
	// goroutines asking reflectors for the life of the run.
	done      chan struct{}
	closeOnce sync.Once

	// upgrading names the peers a punch loop is already running for, so
	// that a second relayed connection to the same peer does not start
	// a second one.
	upgrading sync.Map // peer.ID -> struct{}
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

	// The socket has to be wrapped before libp2p opens it and asked
	// long after, so the handle is made first and filled in by the
	// option itself.
	mine, socketOpt := ownSocket()

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
		for _, extra := range [][]multiaddr.Multiaddr{known(&observed), known(&measured)} {
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
	HandleObserved(h)
	HandleAddrs(h)
	t := &Host{h: h, done: make(chan struct{})}
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
	return t, nil
}

// known reads one of the address stores. Nothing is published before
// the first answer lands, which is the honest state and not a failure.
func known(v *atomic.Value) []multiaddr.Multiaddr {
	as, _ := v.Load().([]multiaddr.Multiaddr)
	return as
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
			for _, ma := range quicAddrs(a, nextDoors) {
				if !has(out, ma) {
					out = append(out, ma)
				}
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
		for _, fresh := range quicAddrs(m, nextDoors) {
			if !has(out, fresh) {
				out = append(out, fresh)
			}
		}
	}
	return out
}

// nextDoors is how many ports above the measured one to offer.
//
// Zero, measured rather than chosen. This carrier gives each new
// destination a different port, and the question was only whether it
// gives them in order: a punch offering the measured port and the
// sixty-four above it — sixty-six addresses, 36011 through 62066 —
// never landed. They are not ordered within any span worth advertising,
// so a span is not a fix and pretending otherwise costs every peer
// sixty-five dials that cannot arrive.
//
// The constant stays because the measurement above it is worth keeping
// and this is where its width is stated. On a network whose published
// address is merely old, zero is correct and one address is enough.
const nextDoors = 0

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
// quicAddrs turns a reflector's "ip:port" into the QUIC multiaddrs a
// peer can dial — that port and the next few — holding each to the same
// standard as an address a relay reported.
//
// The two observers speak different languages and the difference is easy
// to miss: ObservedProto answers with a multiaddr because it is libp2p
// talking to libp2p, and STUN answers with a host and port because it
// predates all of this. Handing the second to a parser expecting the
// first fails silently and every measurement is discarded — which is
// exactly what happened, with the measured address printed in the log
// immediately above the punch that ignored it.
func quicAddrs(hostPort string, extra int) []multiaddr.Multiaddr {
	ip, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	base, err := strconv.Atoi(port)
	if err != nil || base <= 0 {
		return nil
	}
	family := "ip4"
	if !addr.Unmap().Is4() {
		family = "ip6"
	}

	var out []multiaddr.Multiaddr
	for off := 0; off <= extra; off++ {
		if base+off > 65535 {
			break
		}
		a, ok := usableObserved(fmt.Sprintf("/%s/%s/udp/%d/quic-v1", family, addr.Unmap(), base+off))
		if !ok {
			return nil // the first one decides: a refusal is about the address, not the port
		}
		out = append(out, a)
	}
	return out
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
	direct := directOnly(info.Addrs)
	if len(direct) == 0 {
		return nil, fmt.Errorf("no direct address for %s", info.ID)
	}
	info.Addrs = direct
	return t.dial(ctx, info, p)
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

// Open starts a stream on a connection that already exists.
//
// The path is whatever the connection manager considers best right now,
// which after an upgrade is the direct connection rather than the relay
// the session started on. That is the whole benefit of upgrading: a
// stream opened later takes the better path without anything switching
// over, and a stream opened earlier stays where it was born.
func (t *Host) Open(ctx context.Context, id peer.ID, p protocol.ID) (network.Stream, error) {
	s, err := t.h.NewStream(network.WithAllowLimitedConn(ctx, "ratatoskr"), id, p)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	return s, nil
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

// BetterThan ranks two measured paths against step 3's ladder: the LAN
// if the peer is here, the Internet if it is not, the relay only when
// neither can be opened, and unknown below all three.
//
// It exists so that a transfer already running can ask whether moving
// is worth a new stream. PathTo has the same order built into it, and
// this is where the order is written down.
func (p Path) BetterThan(other Path) bool { return rank(p) < rank(other) }

func rank(p Path) int {
	switch p {
	case PathLAN:
		return 0
	case PathDirect:
		return 1
	case PathRelay:
		return 2
	}
	return 3
}

func (t *Host) Close() error {
	t.closeOnce.Do(func() { close(t.done) })
	return t.h.Close()
}

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
