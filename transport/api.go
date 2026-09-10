package transport

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multistream"
)

// This file is the package's whole exported surface. Everything else is
// unexported, so a caller never names a transport, a handshake, an
// address format or a traversal technique.

// PeerID identifies a machine. It is stable across restarts and address
// changes, and the handshake proves it before any byte is carried, so a
// stream's Peer is authenticated. It says nothing about what that
// machine may do.
type PeerID string

// Short returns the last eight characters of an id, in two groups, for
// people to read. Do not decide anything on it.
func (id PeerID) Short() string { return identity.Short(string(id)) }

// Config is what New needs. The zero value is valid: local network only,
// with the identity in the per-OS config directory.
type Config struct {
	// Dir holds identity.key, generated on first use. Empty means
	// RATATOSKR_CONFIG_DIR, or the per-OS config directory.
	Dir string

	// Relays are relay addresses, as given by whoever runs one. Empty
	// means no relay: this machine is reachable on the local network
	// and wherever it can be dialled directly.
	//
	// Set these or Coordinator, not both. These name the relay to use
	// and never change; a coordinator hands one out and can change it.
	Relays []string

	// Coordinator is where to ask which relay this machine should use,
	// as given by whoever runs the fleet. Several addresses for the
	// same coordinator are fine and are tried together.
	//
	// A machine with a coordinator asks at startup and keeps asking, so
	// it follows the relay it is given rather than the one it was given
	// once. Being told none is an answer: it runs on the local network
	// and on whatever it can dial directly.
	Coordinator []string

	// NoLAN turns off discovery on the local network, so no multicast
	// socket is opened.
	NoLAN bool
}

// ID returns this machine's identity.
func (t *Host) ID() PeerID { return PeerID(t.h.ID().String()) }

// Addrs returns the strings to give another machine so it can Connect
// here. Treat them as opaque and pass them along however you like.
//
// They change as this machine learns where it appears from outside, so
// read them when handing them over rather than once at startup.
func (t *Host) Addrs() []string {
	return p2pAddrs(peer.AddrInfo{ID: t.h.ID(), Addrs: t.h.Addrs()})
}

// Peers returns the machines currently connected.
func (t *Host) Peers() []PeerID {
	ps := t.h.Network().Peers()
	out := make([]PeerID, 0, len(ps))
	for _, p := range ps {
		out = append(out, PeerID(p.String()))
	}
	return out
}

// Handle registers fn for a protocol name of the caller's choosing.
// Registering the same name again replaces the handler.
//
// The far end is authenticated by the time fn runs. Deciding what it may
// do is the caller's.
func (t *Host) Handle(proto string, fn func(Stream)) {
	t.h.SetStreamHandler(protocol.ID(proto), func(s network.Stream) { fn(stream{s}) })
}

// Connect reaches a machine at the addresses it published, which are
// whatever Addrs returned over there.
//
// Given addrs, only those are dialled — so passing the addresses a
// machine was found at on the local network keeps the session on that
// network. Given none, it uses what it already knows about the machine
// plus any configured relay.
func (t *Host) Connect(ctx context.Context, id PeerID, addrs []string) error {
	info, err := addrInfo(id, addrs)
	if err != nil {
		return err
	}
	if len(info.Addrs) == 0 {
		info.Addrs = t.relays.dialAddrs()
	}
	if err := t.h.Connect(ctx, info); err != nil {
		return fmt.Errorf("%w: connect to %s: %w", ErrUnreachable, id.Short(), err)
	}
	return nil
}

// Open starts a stream to a machine, dialling first if there is no
// connection yet. It takes the best path available at that moment.
//
// Failure is either ErrUnreachable or ErrNotHandled; test with
// errors.Is. A stream already open stays on the path it was born on, so
// a bulk transfer follows Watch, and when a better path arrives finishes
// the current stream and sends the rest on a new one.
func (t *Host) Open(ctx context.Context, id PeerID, proto string) (Stream, error) {
	pid, err := decode(id)
	if err != nil {
		return nil, err
	}
	s, err := t.openStream(ctx, pid, protocol.ID(proto))
	if err != nil {
		return nil, fmt.Errorf("open %s to %s: %w", proto, id.Short(), err)
	}
	return stream{s}, nil
}

// PathTo reports how this machine currently reaches another, taking the
// best of every connection it has to it.
func (t *Host) PathTo(id PeerID) Path {
	pid, err := decode(id)
	if err != nil {
		return PathUnknown
	}
	return t.pathTo(pid)
}

// Watch reports the path to a machine, now and on every change, until
// cancel is called or the host closes — at which point the channel
// closes, so ranging over it ends on its own.
//
// The current path arrives before Watch returns, so there is nothing to
// read separately before waiting. The channel holds one value: a reader
// that falls behind is given where the machine is now rather than every
// rung it passed. An id that is not a machine id watches nothing and its
// channel is closed already.
//
// This is what a bulk transfer waits on. When the path that arrives
// beats the one the current stream is on, finish that stream and send
// the rest on a new one.
func (t *Host) Watch(id PeerID) (<-chan Path, func()) {
	pid, err := decode(id)
	if err != nil {
		dead := make(chan Path)
		close(dead)
		return dead, func() {}
	}
	return t.watch(pid)
}

// OnLAN calls fn for every machine found on the local network, once for
// each already known and again for each one found later. The addresses
// passed reach that machine on this network, and go straight to Connect.
//
// It needs no server and no Internet, and does nothing when Config.NoLAN
// was set. Machines anywhere else arrive as addresses the caller
// obtained some other way.
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

// Path is how a session reached the far end, measured from a live
// connection rather than guessed.
//
// lan and direct are both direct connections; the difference is where.
// lan is a private address on this network, direct a public one across
// the Internet. "direct" never means "not relayed" — relay is its own
// value.
type Path string

const (
	PathUnknown Path = "unknown"
	PathLAN     Path = "lan"
	PathDirect  Path = "direct"
	PathRelay   Path = "relay"
)

// BetterThan reports whether p outranks other. The order is lan, direct,
// relay, unknown. Use it to decide whether a running transfer is worth
// moving onto a new stream.
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

// Stream carries raw bytes to one machine. No framing, envelope or
// length prefix is added; whatever structure the bytes have is the
// caller's. Writes block while the far end is behind, so copying a large
// file needs no flow control of its own.
//
// A stream dies with the connection it was opened on and cannot be
// moved; when it does, Read and Write report ErrUnreachable. Reissuing
// what was in flight is the caller's, since only the caller knows what a
// partial answer meant.
type Stream interface {
	io.ReadWriteCloser

	// CloseWrite says "done sending" and goes on reading, ending a
	// request without ending the connection.
	CloseWrite() error

	// Peer is the machine on the other end, proven by the handshake.
	Peer() PeerID

	// Path is where these bytes are going. It never changes for a
	// stream that is already open.
	Path() Path
}

// The two ways a stream dies. Both are returned wrapped, with what
// actually went wrong still in the chain for a person to read, so test
// them with errors.Is.
var (
	// ErrUnreachable means the machine could not be reached: it is off,
	// it moved, or no path to it can be opened right now. Retrying later,
	// or waiting on Watch, is the answer.
	ErrUnreachable = errors.New("machine unreachable")

	// ErrNotHandled means the machine was reached and answered that it
	// has no handler for that protocol name. Retrying will not change it.
	ErrNotHandled = errors.New("protocol not handled")
)

type stream struct{ network.Stream }

func (s stream) Peer() PeerID { return PeerID(s.Stream.Conn().RemotePeer().String()) }
func (s stream) Path() Path   { return pathOfConn(s.Stream.Conn()) }

func (s stream) Read(p []byte) (int, error) {
	n, err := s.Stream.Read(p)
	return n, died(err)
}

func (s stream) Write(p []byte) (int, error) {
	n, err := s.Stream.Write(p)
	return n, died(err)
}

// died tags a failure on a stream that was already open. A stream dies
// with its connection and there is no other way to lose one here, so
// there is one answer. io.EOF is left alone: it is the far end saying it
// has finished sending, which is how a transfer normally ends.
func died(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, err)
}

// why tags a stream that never opened with which of the two it was. The
// far end refusing a protocol name is the only failure that says
// anything about the machine other than that it could not be used.
func why(err error) error {
	if errors.Is(err, multistream.ErrNotSupported[protocol.ID]{}) {
		return fmt.Errorf("%w: %w", ErrNotHandled, err)
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, err)
}

// decode parses an id, refusing anything that is not one.
func decode(id PeerID) (peer.ID, error) {
	pid, err := peer.Decode(string(id))
	if err != nil {
		return "", fmt.Errorf("%q is not a machine id", string(id))
	}
	return pid, nil
}

// addrInfo parses opaque addresses into a dial set, rejecting any that
// names a different machine. Only this layer can catch that: the id is
// inside the address the caller never parses.
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

// p2pAddrs renders addresses with the identity folded in, which is the
// form Connect accepts.
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
