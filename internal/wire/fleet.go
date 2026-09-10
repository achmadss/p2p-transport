package wire

import (
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"
)

// LeaseProto is how an agent asks the coordinator which relay to use.
// The agent sends nothing: the handshake has already proved which
// machine is asking, and that id is the whole request.
const LeaseProto = protocol.ID("/ratatoskr/lease/1.0.0")

// FleetProto is the relay's link to the coordinator. The relay opens it,
// sends one Register, and then reads Commands until the stream dies.
// One long-lived stream rather than a dial per command, so a relay that
// loses the coordinator finds out immediately.
const FleetProto = protocol.ID("/ratatoskr/fleet/1.0.0")

// Register is a relay introducing itself.
type Register struct {
	// Addrs are the full multiaddrs an agent can reach this relay at.
	Addrs []string `json:"addrs"`

	// Bandwidth is bytes per second, per direction, this relay can
	// actually forward. A relayed byte crosses the machine twice, so
	// this is not the provider's sticker number unless that number is
	// already per direction.
	Bandwidth int64 `json:"bandwidth"`
}

// Command is one instruction pushed down the fleet stream.
type Command struct {
	Op   string `json:"op"`
	Peer string `json:"peer"`
}

// The instructions a relay understands. Rate limits are pushed the same
// way once the shaper exists.
const (
	OpAdmit  = "admit"  // this machine may reserve here
	OpRevoke = "revoke" // it may not, any more
)

// Lease is the coordinator's answer to an agent.
type Lease struct {
	// Relay is the full multiaddrs of the relay to use, empty when no
	// relay has room or the asking machine belongs to no subject. Empty
	// is an answer, not an error: the machine runs without a relay.
	Relay []string `json:"relay"`

	// TTL is how long before the agent must ask again. It renews at
	// half this, and keeps using the relay past it if the coordinator
	// has gone quiet, because tearing down a working path helps nobody.
	TTL time.Duration `json:"ttl"`
}
