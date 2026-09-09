package transport

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Turning a relayed connection into a direct one, and keeping on trying.
//
// DCUtR already attempts this once. It fires when an inbound relayed
// connection arrives, tries three times, and stops — and the three
// attempts all use whatever address the far end published at the moment
// the connection opened. On the carrier TODO.md step 3 measured, that is
// the one moment at which the answer is most likely to be wrong, and
// there is no fourth attempt to be right in. libp2p keeps its hole
// puncher private, so the retry cannot be asked of it; it is done here
// instead, beside it rather than in place of it.
//
// The shape is Tailscale's, from `sendDiscoPingsLocked`: the path is not
// decided once at dial time, it is a race that keeps being re-run for
// the life of the session. Both ends run this loop, so the two dials
// cross, and each outbound dial opens the mapping the other's dial needs
// — which is a hole punch, minus the round-trip DCUtR spends agreeing on
// when to fire. What replaces that agreement is repetition: a punch that
// misses costs one dial and is tried again five seconds later, against
// an address set that has been re-measured in the meantime.
//
// Nothing here relays anything. The relayed connection stays up and
// carries what it was carrying; a direct connection that lands beside it
// is preferred by libp2p for everything opened afterwards, and PathTo
// reports the upgrade because it reads the live connections.
var (
	// upgradeEvery is how often a relayed peer is re-tried. Tailscale's
	// disco ping interval is five seconds and their heartbeat three;
	// five is the cheaper of the two and this dial costs more than a
	// ping.
	upgradeEvery = config.Duration("RATATOSKR_UPGRADE_EVERY", 5*time.Second)

	// upgradeDial bounds one attempt. It is short on purpose: a punch
	// that has not landed in this long has missed, and the answer to a
	// miss is the next attempt rather than a longer wait.
	upgradeDial = config.Duration("RATATOSKR_UPGRADE_DIAL", 5*time.Second)
)

// watchForRelayed starts a punch loop for every peer that is reachable
// only through a relay, in either direction. Both ends must dial for
// either to get through, and both start counting from the same event —
// the relayed connection they share — so their ticks land within a
// round trip of each other without anything being negotiated. That is
// the agreement DCUtR spends a round trip reaching, had for free, and
// it is why this loop does not jitter its clock.
//
// It watches disconnections as well, because the ladder is climbed in
// both directions. A direct connection that dies leaves the relayed one
// beside it still carrying the session — libp2p goes back to it for
// every stream opened after — and no new connection arrives to say so.
// Without this the peer would fall to the relay and stay there with
// nobody trying to climb back.
func (t *Host) watchForRelayed() {
	t.h.Network().Notify(&network.NotifyBundle{
		ConnectedF:    func(_ network.Network, c network.Conn) { t.punchIfRelayed(c.RemotePeer()) },
		DisconnectedF: func(_ network.Network, c network.Conn) { t.punchIfRelayed(c.RemotePeer()) },
	})
}

// punchIfRelayed starts the loop when the relay is the best this
// machine currently has to a peer, and reports whether it did.
//
// It asks for the best path rather than looking at the connection it
// was handed. A second relayed connection to a peer already reached
// directly is not a reason to punch, and a direct connection closing is
// not a reason not to.
func (t *Host) punchIfRelayed(id peer.ID) bool {
	if t.PathTo(id) != PathRelay {
		return false
	}
	go t.upgrade(id)
	return true
}

// upgrade re-dials a relayed peer directly until it answers, the peer
// goes away, or the host closes.
func (t *Host) upgrade(id peer.ID) {
	if _, running := t.upgrading.LoadOrStore(id, struct{}{}); running {
		return
	}
	defer t.upgrading.Delete(id)

	tick := time.NewTicker(upgradeEvery)
	defer tick.Stop()
	start := time.Now()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
		}
		switch p := t.PathTo(id); p {
		case PathDirect, PathLAN:
			// Someone's dial landed — ours, theirs, or DCUtR's. Which
			// one is not worth finding out; the path is measured from
			// the connection either way. Print which path it is rather
			// than the word "direct": "direct" is also the name of one
			// of them, and a LAN upgrade announcing itself as direct
			// reads as a trip out to the Internet and back.
			fmt.Fprintf(os.Stderr, "connection to %s left the relay for %s after %s\n",
				identity.Short(id.String()), p, time.Since(start).Round(time.Second))
			return
		case PathUnknown:
			return // no connection at all any more
		}
		t.dialDirect(id)
	}
}

// dialDirect tries every address the far end can be reached at, lowest
// rung first.
//
// The rungs are the LAN, then the Internet, then the relay that is
// already carrying the session — and they are raced rather than walked,
// because racing arrives at the same answer sooner. libp2p's dial ranker
// groups the candidates into private, public and relay and starts the
// first two together; the LAN dial completes in a millisecond or two
// while a punch across the Internet is still waiting on its first round
// trip, so the lower rung wins whenever it exists at all. The relay rung
// needs no dialling: it is the connection this loop is running on, and
// it keeps carrying the session until something better lands beside it.
//
// The candidates come from two places because neither is complete. The
// peerstore has whatever identify and mDNS put there, which over a
// relayed connection is public addresses only. AddrsProto asks the peer
// itself and gets the rest — its LAN address above all. Circuit
// addresses are dropped from both: a relay cannot be the answer to how
// to stop using a relay.
//
// The dial is forced past the connection that already exists: without
// that, libp2p answers "already connected" and returns the relayed
// connection, which is the thing being escaped.
func (t *Host) dialDirect(id peer.ID) {
	ask, cancelAsk := context.WithTimeout(context.Background(), upgradeDial)
	defer cancelAsk()

	direct := directOnly(t.h.Peerstore().Addrs(id))
	for _, a := range t.askAddrs(ask, id) {
		if !has(direct, a) {
			direct = append(direct, a)
		}
	}
	if len(direct) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), upgradeDial)
	defer cancel()
	err := t.h.Connect(network.WithForceDirectDial(ctx, "upgrade"), peer.AddrInfo{ID: id, Addrs: direct})
	if err != nil && os.Getenv("RATATOSKR_DIAG") != "" {
		fmt.Fprintf(os.Stderr, "punch at %s missed on %d addresses: %v\n",
			identity.Short(id.String()), len(direct), err)
	}
}

// HandleAddrs answers AddrsProto with this machine's own addresses, one
// per line. Circuits are stripped here as well as on the reading side,
// because a peer should not have to filter what it was never owed.
func HandleAddrs(h host.Host) {
	h.SetStreamHandler(AddrsProto, func(s network.Stream) {
		defer s.Close()
		s.SetDeadline(time.Now().Add(10 * time.Second))
		for _, a := range directOnly(h.Addrs()) {
			fmt.Fprintln(s, a)
		}
	})
}

// askAddrs reads the far end's own view of where it can be reached.
//
// Asked every tick rather than once, because the answer moves: the far
// end re-measures its public address on the endpointsFresh clock, and a
// laptop that changes network changes its LAN address too. It is a
// kilobyte over a relayed connection, which PLAN.md §7 already counts as
// free — only bulk transfer cares which path it took.
//
// Silence is not an error worth reporting. A peer too old to know this
// protocol, or a relay too slow to answer within the tick, leaves the
// peerstore's addresses to be dialled on their own.
func (t *Host) askAddrs(ctx context.Context, id peer.ID) []multiaddr.Multiaddr {
	s, err := t.Open(ctx, id, AddrsProto)
	if err != nil {
		return nil
	}
	defer s.Close()
	if d, ok := ctx.Deadline(); ok {
		s.SetDeadline(d)
	}
	var out []multiaddr.Multiaddr
	lines := bufio.NewScanner(io.LimitReader(s, 8<<10))
	for lines.Scan() {
		a, err := multiaddr.NewMultiaddr(strings.TrimSpace(lines.Text()))
		if err == nil {
			out = append(out, a)
		}
	}
	return directOnly(out)
}
