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
// sends one Register, and then reads Commands down it while sending a
// Demand up it once a period, until the stream dies. One long-lived
// stream rather than a dial per command, so a relay that loses the
// coordinator finds out immediately.
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
	Op string `json:"op"`

	// Peer is the machine an admit or a revoke is about.
	Peer string `json:"peer,omitempty"`

	// Subject is who the machine belongs to. Every machine of one
	// subject shares that subject's rate on a relay, so this is the key
	// the relay meters and limits by, and the machine id is not.
	Subject string `json:"subject,omitempty"`

	// Rate is the subject's ceiling in bytes per second: what its
	// machines may reach between them when the relay is quiet. Sent with
	// an admit and again whenever it changes.
	//
	// The floor is not here on purpose. A floor is kept by not
	// overselling the relay in the first place, which is the
	// coordinator's job; throttling cannot create one.
	Rate int64 `json:"rate,omitempty"`
}

// The instructions a relay understands.
const (
	OpAdmit    = "admit"    // this machine may reserve here, at this rate
	OpRevoke   = "revoke"   // it may not, any more
	OpSetLimit = "setlimit" // this subject's rate has changed
)

// Report is what a relay says about one subject it carried this period.
//
// Used is what actually crossed the relay. Throttled says the subject
// spent some of the period waiting on its bucket, which is how a relay
// asks for more than it moved: a machine held at its limit would have
// taken more if it had been allowed to.
type Report struct {
	Subject   string `json:"subject"`
	Used      int64  `json:"used"`
	Throttled bool   `json:"throttled"`
}

// Demand is one period of reports, sent up the same stream the commands
// come down. Subjects that moved nothing are left out, which is most of
// the traffic saved for none of the accuracy: the coordinator lets a
// share it has not heard about decay on its own.
type Demand struct {
	// Period is how long this report covers. The coordinator uses it to
	// decide when a silent relay has gone idle, so it travels with the
	// report rather than being configured twice.
	Period  time.Duration `json:"period"`
	Reports []Report      `json:"reports"`
}

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
