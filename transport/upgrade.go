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
	"github.com/achmadss/p2p-transport/internal/wire"
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
		ConnectedF:    func(_ network.Network, c network.Conn) { t.repair(c.RemotePeer()) },
		DisconnectedF: func(_ network.Network, c network.Conn) { t.repair(c.RemotePeer()) },
	})
}

// repair puts a peer back on the best rung it can reach, and returns
// the rung it found it on.
//
// It asks for the best path rather than looking at the connection it
// was handed. A second relayed connection to a peer already reached
// directly is not a reason to punch, a direct connection closing is not
// a reason not to, and the last connection closing is a different
// problem from a worse one being left behind.
func (t *Host) repair(id peer.ID) Path {
	p := t.pathTo(id)
	switch p {
	case PathRelay:
		go t.upgrade(id) // something better may still be opened
	case PathUnknown:
		go t.restore(id) // nothing is left; climb back down
	}
	return p
}

// restoreFor bounds how long a peer that has gone entirely is chased.
//
// Long enough to outlast a Wi-Fi handover or a sleeping radio, short
// enough that a machine genuinely switched off is not dialled for the
// life of the process. When it expires the peer is simply gone, and the
// next request from above dials it afresh.
const restoreFor = 30 * time.Second

// restore climbs back down the ladder when every path to a peer has
// gone.
//
// The rungs are tried in order and the order is the point: everything
// direct that is known, raced in one dial so the LAN wins on latency
// where it exists, and the relay only once that has found nothing. A
// relayed connection is worth having and worth leaving, so landing on
// it is not the end — the notifee sees it arrive and starts the punch
// loop, which is the same ladder climbed the other way.
//
// It cannot ask the peer where it lives, because AddrsProto needs a
// connection and there is none. What it has is the peerstore, which
// identify filled while the connection was up and which outlives it.
func (t *Host) restore(id peer.ID) {
	if _, running := t.working.LoadOrStore(id, struct{}{}); running {
		return
	}
	defer t.working.Delete(id)

	tick := time.NewTicker(upgradeEvery)
	defer tick.Stop()
	deadline := time.After(restoreFor)
	for {
		if t.pathTo(id) != PathUnknown {
			return // it came back, by our dial or by theirs
		}
		t.redial(id)
		select {
		case <-t.done:
			return
		case <-deadline:
			if os.Getenv("RATATOSKR_DIAG") != "" {
				fmt.Fprintf(os.Stderr, "gave up reaching %s after %s\n",
					identity.Short(id.String()), restoreFor)
			}
			return
		case <-tick.C:
		}
	}
}

// redial tries the direct rungs together, then the relay if they found
// nothing. Nothing is forced past an existing connection here the way
// dialDirect forces: there is no connection to get past, and forcing
// would rule out the one rung that is left.
func (t *Host) redial(id peer.ID) {
	if direct := directOnly(t.h.Peerstore().Addrs(id)); len(direct) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), upgradeDial)
		err := t.h.Connect(ctx, peer.AddrInfo{ID: id, Addrs: direct})
		cancel()
		if err == nil {
			return
		}
	}
	if len(t.circuits) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), upgradeDial)
	defer cancel()
	if err := t.h.Connect(ctx, peer.AddrInfo{ID: id, Addrs: t.circuits}); err != nil {
		if os.Getenv("RATATOSKR_DIAG") != "" {
			fmt.Fprintf(os.Stderr, "no rung left to %s: %v\n", identity.Short(id.String()), err)
		}
	}
}

// upgrade re-dials a relayed peer directly until it answers, the peer
// goes away, or the host closes.
func (t *Host) upgrade(id peer.ID) {
	if _, running := t.working.LoadOrStore(id, struct{}{}); running {
		return
	}
	defer t.working.Delete(id)

	tick := time.NewTicker(upgradeEvery)
	defer tick.Stop()
	start := time.Now()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
		}
		switch p := t.pathTo(id); p {
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

// handleAddrs answers AddrsProto with this machine's own addresses, one
// per line. Circuits are stripped here as well as on the reading side,
// because a peer should not have to filter what it was never owed.
func handleAddrs(h host.Host) {
	h.SetStreamHandler(wire.AddrsProto, func(s network.Stream) {
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
	s, err := t.openStream(ctx, id, wire.AddrsProto)
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
