// Package wire holds the protocol identifiers two binaries in this
// module must agree on, and the handlers that are the same on both.
//
// It exists because heimdall answers one of the transport's own
// protocols and the constant must not be written twice. Nothing above
// layer 4 imports this: the surface in PLAN.md §2.1 is `transport`, and
// a protocol name there is a plain string the caller picks.
package wire

import (
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// ObservedProto asks the far end for the address it sees us at.
//
// A reflector on any other socket cannot answer this. A NAT that keeps
// the mapping endpoint-independent but renumbers the port gives each
// socket its own external port, so the port a throwaway STUN socket
// learns is not the port libp2p punches from — which is exactly what a
// phone hotspot does, and exactly why the punch was one-sided. Asked on
// the connection itself, the answer is the QUIC socket's own address.
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
