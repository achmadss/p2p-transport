package transport

import (
	"context"
	"fmt"
	"io"

	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

// The surface. PLAN.md §2.1 is the contract and this file is the whole
// of it: nothing here names a transport, a security handshake, an
// address format or a traversal technique, and nothing above needs to
// import libp2p to use it. Everything else in this package is below the
// seam and unexported.

// PeerID is a machine's identity, stable across restarts and address
// changes. It is proven by the handshake before any byte of an
// application's is carried, so a stream's Peer is authenticated.
//
// It is not authorisation. What a proven machine may then do is a
// question this layer does not ask; PLAN.md §2.2.
type PeerID string

// Short is the fingerprint a person reads: the last eight characters,
// in two groups. For display only — nothing decides anything on it.
func (id PeerID) Short() string { return identity.Short(string(id)) }

// Config is everything New needs.
//
// There is no key here. A private key is a libp2p type, and naming one
// on this surface would make every caller import libp2p to fill it in,
// which is the one thing step 4 exists to prevent. The key is loaded or
// generated in Dir instead, and stays there.
type Config struct {
	// Dir is where identity.key lives. Empty means the per-OS default,
	// or RATATOSKR_CONFIG_DIR when that is set.
	Dir string

	// Relays are opaque strings from whoever runs the relay. Empty means
	// this machine is reachable on the local network and from anywhere
	// it can be dialled directly, which is a complete way to run.
	Relays []string

	// NoLAN skips local discovery entirely, for a network where a
	// multicast socket is unwelcome.
	NoLAN bool
}

// ID is this machine's identity.
func (t *Host) ID() PeerID { return PeerID(t.h.ID().String()) }

// Addrs are the strings another machine can be given to reach this one.
//
// They are opaque on purpose: they come out of Addrs() here and go into
// Connect there, through whatever channel the application already has,
// and nothing above this package parses one. That single decision is
// what leaves the application free to invent any rendezvous it likes
// while keeping every address format below the seam.
//
// The set changes as this machine learns where it appears from outside,
// so read it when you are about to hand it over rather than once at
// startup.
func (t *Host) Addrs() []string {
	return p2pAddrs(peer.AddrInfo{ID: t.h.ID(), Addrs: t.h.Addrs()})
}

// Peers are the machines currently connected.
func (t *Host) Peers() []PeerID {
	ps := t.h.Network().Peers()
	out := make([]PeerID, 0, len(ps))
	for _, p := range ps {
		out = append(out, PeerID(p.String()))
	}
	return out
}

// Handle registers a handler for a protocol. The name is a string the
// caller picks; this package imposes no framing inside the stream.
//
// The machine on the far end is already authenticated when the handler
// runs. Whether it may do what it is asking is the caller's to decide.
func (t *Host) Handle(proto string, fn func(Stream)) {
	t.h.SetStreamHandler(protocol.ID(proto), func(s network.Stream) { fn(stream{s}) })
}

// Connect reaches a machine at the addresses it published.
//
// With no addresses it uses what is already known — anything learned on
// the local network or from an earlier session, and the configured
// relays. With addresses it uses those, which is how a caller that
// found a machine on the local network keeps the session on the local
// network: hand over the local addresses and nothing else.
func (t *Host) Connect(ctx context.Context, id PeerID, addrs []string) error {
	info, err := addrInfo(id, addrs)
	if err != nil {
		return err
	}
	if len(info.Addrs) == 0 {
		info.Addrs = t.circuits
	}
	if err := t.h.Connect(ctx, info); err != nil {
		return fmt.Errorf("connect to %s: %w", id.Short(), err)
	}
	return nil
}

// Open starts a stream to a machine, connecting first if there is no
// connection yet.
//
// The stream takes whatever path is best right now, which after an
// upgrade is the direct connection rather than the one the session
// started on. A stream already open stays where it was born; PLAN.md
// §2.2 is why, and Path is how a caller carrying bulk data notices.
func (t *Host) Open(ctx context.Context, id PeerID, proto string) (Stream, error) {
	pid, err := decode(id)
	if err != nil {
		return nil, err
	}
	s, err := t.openStream(ctx, pid, protocol.ID(proto))
	if err != nil {
		return nil, err
	}
	return stream{s}, nil
}

// PathTo reports how this machine currently reaches another, across
// every connection it has to it.
func (t *Host) PathTo(id PeerID) Path {
	pid, err := decode(id)
	if err != nil {
		return PathUnknown
	}
	return t.pathTo(pid)
}

// OnLAN reports machines found on the local network, with the addresses
// to reach them there. It is called for each one already found, and
// again for each one found later.
//
// This is the only discovery below the seam, and it needs no server and
// no Internet. Everything else arrives as addresses the application got
// from somewhere of its own.
func (t *Host) OnLAN(fn func(PeerID, []string)) {
	if t.lan == nil {
		return
	}
	t.mu.Lock()
	t.onLAN = append(t.onLAN, fn)
	known := t.lan.Peers()
	t.mu.Unlock()
	for _, p := range known {
		fn(PeerID(p.ID.String()), p2pAddrs(p))
	}
}

// Path is how a session reached the far end. It is measured from a live
// connection, never guessed.
//
// lan and direct are both direct connections and the difference is
// where: lan is a private address on this network, direct is a public
// one across the Internet. Never write "direct" to mean "not relayed" —
// one of the three is called that. PLAN.md §7.
type Path string

const (
	PathUnknown Path = "unknown"
	PathLAN     Path = "lan"
	PathDirect  Path = "direct"
	PathRelay   Path = "relay"
)

// BetterThan ranks two measured paths: the local network if the machine
// is here, the Internet if it is not, the relay only when neither can be
// opened, and unknown below all three.
//
// It exists so that a transfer already running can ask whether moving is
// worth a new stream. PathTo has the same order built into it, and this
// is where the order is written down.
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

// Stream is bytes to one machine, and nothing else. There is no
// framing, no envelope and no length prefix imposed from below;
// whatever structure the bytes have is the caller's.
//
// Writing blocks while the far end is behind, so a caller copying ten
// gigabytes needs no credit window of its own.
//
// A stream dies with the connection it was opened on. Nothing here can
// save one, and pretending otherwise would produce a caller that is
// subtly wrong; PLAN.md §2.2.
type Stream interface {
	io.ReadWriteCloser

	// CloseWrite says "I am done sending" and goes on reading. It is
	// how the far end learns a request has ended without the
	// connection ending with it.
	CloseWrite() error

	// Peer is the machine on the other end, proven by the handshake.
	Peer() PeerID

	// Path is where this stream's bytes are going. It cannot change:
	// the stream is bound to the connection it was opened on.
	Path() Path
}

type stream struct{ network.Stream }

func (s stream) Peer() PeerID { return PeerID(s.Stream.Conn().RemotePeer().String()) }
func (s stream) Path() Path   { return pathOfConn(s.Stream.Conn()) }

// decode turns an id back into what libp2p needs, refusing anything
// that is not one rather than dialling into the dark.
func decode(id PeerID) (peer.ID, error) {
	pid, err := peer.Decode(string(id))
	if err != nil {
		return "", fmt.Errorf("%q is not a machine id", string(id))
	}
	return pid, nil
}

// addrInfo turns opaque strings back into a dial set, refusing an
// address that names a different machine. A caller cannot check that
// itself — the id is inside the address it never parses — so this is
// the only place it can be caught.
func addrInfo(id PeerID, addrs []string) (peer.AddrInfo, error) {
	pid, err := decode(id)
	if err != nil {
		return peer.AddrInfo{}, err
	}
	info := peer.AddrInfo{ID: pid}
	for _, a := range addrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			return peer.AddrInfo{}, fmt.Errorf("unusable address for %s", id.Short())
		}
		rest, named := peer.SplitAddr(ma)
		if named != "" && named != pid {
			return peer.AddrInfo{}, fmt.Errorf("address belongs to %s, not %s",
				PeerID(named.String()).Short(), id.Short())
		}
		if rest != nil {
			info.Addrs = append(info.Addrs, rest)
		}
	}
	return info, nil
}

// p2pAddrs is the one way an address leaves this package: identity
// folded in, rendered, and opaque from there on.
func p2pAddrs(info peer.AddrInfo) []string {
	as, err := peer.AddrInfoToP2pAddrs(&info)
	if err != nil {
		return nil // only reachable through an invalid id
	}
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}
