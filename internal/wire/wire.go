// Package wire holds the protocol identifiers the transport and the
// relay must agree on, and the handlers that are identical in both.
//
// It exists so a constant that appears in two binaries is written once.
// Applications do not import it: they name their own protocols as plain
// strings when they call transport.Handle.
package wire

import (
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// ObservedProto asks the far end what address it sees this connection
// coming from. Asked on the connection itself, the answer names the
// socket that will do the punching, which a reflector on any other
// socket cannot report.
const ObservedProto = protocol.ID("/ratatoskr/observed/1.0.0")

// AddrsProto asks a peer where it is listening, in its own words.
//
// libp2p's identify already carries this and the receiving side discards
// half of it: every private address is dropped when the connection it
// arrived on is public, and a relay circuit is a public connection. So
// two machines on one network that meet over a relay are never told each
// other's local address — the one dial certain to succeed is the one
// never tried.
const AddrsProto = protocol.ID("/ratatoskr/addrs/1.0.0")

// HandleObserved answers ObservedProto with the address this connection
// came from. The relay serves it, and so does every agent, so two
// machines on one network can name each other with no relay involved.
func HandleObserved(h host.Host) {
	h.SetStreamHandler(ObservedProto, func(s network.Stream) {
		defer s.Close()
		s.SetDeadline(time.Now().Add(10 * time.Second))
		io.WriteString(s, s.Conn().RemoteMultiaddr().String())
	})
}
