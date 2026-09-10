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

// Getting a session off the relay, and keeping it off.
//
// libp2p's own hole puncher tries three times at connection time, using
// whatever address the far end published at that moment — which on a
// carrier that renumbers ports is the moment the answer is most likely
// wrong, with no fourth attempt to be right in. It is private, so the
// retry cannot be asked of it and is done here beside it.
//
// The path is therefore never decided once. Both ends re-dial each other
// every upgradeEvery for as long as the peer stays relayed, and because
// both start counting from the same event — the relayed connection they
// share — their dials cross. Two crossing dials are a hole punch, and
// each miss costs one dial against an address set re-measured since.
//
// Nothing here relays anything, and nothing switches over. The relayed
// connection keeps carrying what it was carrying; a direct one that
// lands beside it is preferred for every stream opened afterwards.
var (
	// upgradeEvery is how often a relayed peer is re-tried.
	upgradeEvery = config.Duration("RATATOSKR_UPGRADE_EVERY", 5*time.Second)

	// upgradeDial bounds one attempt. Short on purpose: a punch that has
	// not landed by now has missed, and the answer to a miss is the next
	// attempt rather than a longer wait.
	upgradeDial = config.Duration("RATATOSKR_UPGRADE_DIAL", 5*time.Second)
)

// watchForRelayed calls changed on every connection change.
//
// Disconnections count as much as connections. A direct connection that
// dies leaves the relayed one beside it still carrying the session, and
// no new connection arrives to say so — without watching for that, the
// peer falls back to the relay and stays there.
func (t *Host) watchForRelayed() {
	t.h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) { t.changed(c.RemotePeer()) },
		DisconnectedF: func(_ network.Network, c network.Conn) {
			t.relays.gone(c.RemotePeer())
			t.changed(c.RemotePeer())
		},
	})
}

// changed puts a peer back on the best path it can reach and tells
// everyone watching it where it ended up. These are the same event seen
// twice — repair reads the path it will act on, so publishing what it
// returns costs no second look at the connections.
func (t *Host) changed(id peer.ID) { t.publish(id, t.repair(id)) }

// repair puts a peer back on the best path it can reach, and returns the
// one it found it on. It reads the best live path rather than the
// connection that fired the event: a second relayed connection to a peer
// already reached directly is no reason to punch, and one connection
// closing is a different problem from the last one closing.
//
// One goroutine per peer walks both directions. Splitting them into two
// dropped every handover: each took the peer's slot for itself, so the
// event that should have started the other half found the slot taken and
// did nothing. A punch loop whose peer vanishes owes it a redial, and a
// redial that lands on the relay owes it a punch.
func (t *Host) repair(id peer.ID) Path {
	p := t.pathTo(id)
	if p != PathRelay && p != PathUnknown {
		return p
	}
	// Closing the host disconnects every peer, and each of those events
	// arrives here. Without this the last act of a shutdown is to start
	// a chase for every peer it has just dropped.
	if t.ctx.Err() != nil {
		return p
	}
	if _, running := t.working.LoadOrStore(id, struct{}{}); running {
		return p
	}
	go func() {
		defer t.working.Delete(id)
		for {
			select {
			case <-t.ctx.Done():
				return
			default:
			}
			switch t.pathTo(id) {
			case PathRelay:
				t.upgrade(id) // something better may still open
			case PathUnknown:
				if !t.restore(id) {
					return // gone for good, or the host is closing
				}
			default:
				return // direct or on the LAN: nothing to repair
			}
		}
	}()
	return p
}

// restoreFor bounds how long a peer that has gone entirely is chased:
// long enough to outlast a Wi-Fi handover or a sleeping radio, short
// enough that a machine switched off is not dialled forever. After it
// the peer is simply gone, and the next request dials afresh.
var restoreFor = 30 * time.Second

// restore redials a peer every path to which has gone, until it comes
// back or restoreFor expires. It reports which of the two happened, so
// repair can start the climb again: landing on the relay is the bottom
// of the ladder, not the end of it.
//
// It cannot ask the peer where it lives, since that needs a connection
// and there is none. It has the peerstore, filled while the connection
// was up and outliving it.
func (t *Host) restore(id peer.ID) bool {
	tick := time.NewTicker(upgradeEvery)
	defer tick.Stop()
	deadline := time.After(restoreFor)
	for {
		if t.pathTo(id) != PathUnknown {
			return true // it came back, by our dial or by theirs
		}
		t.redial(id)
		select {
		case <-t.ctx.Done():
			return false
		case <-deadline:
			if os.Getenv("RATATOSKR_DIAG") != "" {
				fmt.Fprintf(os.Stderr, "gave up reaching %s after %s\n",
					identity.Short(id.String()), restoreFor)
			}
			return false
		case <-tick.C:
		}
	}
}

// redial races every known direct address in one dial, then falls back
// to the relay. Unlike dialDirect it does not force past an existing
// connection: there is none to get past, and forcing would rule out the
// relay, which is the only option left.
func (t *Host) redial(id peer.ID) {
	if direct := directOnly(t.h.Peerstore().Addrs(id)); len(direct) > 0 {
		ctx, cancel := context.WithTimeout(t.ctx, upgradeDial)
		err := t.h.Connect(ctx, peer.AddrInfo{ID: id, Addrs: direct})
		cancel()
		if err == nil {
			return
		}
	}
	circuits := t.relays.dialAddrs()
	if len(circuits) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(t.ctx, upgradeDial)
	defer cancel()
	if err := t.h.Connect(ctx, peer.AddrInfo{ID: id, Addrs: circuits}); err != nil {
		if os.Getenv("RATATOSKR_DIAG") != "" {
			fmt.Fprintf(os.Stderr, "no rung left to %s: %v\n", identity.Short(id.String()), err)
		}
	}
}

// upgrade re-dials a relayed peer directly until it answers, the peer
// goes away, or the host closes. It returns to repair, which decides
// what the state it stopped on needs next.
func (t *Host) upgrade(id peer.ID) {
	tick := time.NewTicker(upgradeEvery)
	defer tick.Stop()
	start := time.Now()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-tick.C:
		}
		switch p := t.pathTo(id); p {
		case PathDirect, PathLAN:
			// A dial landed — ours or theirs, which does not matter,
			// since the path is measured from the connection either
			// way. Name the path rather than saying "direct": direct
			// is also one of the two, and a local upgrade reported as
			// direct reads like a trip out to the Internet and back.
			fmt.Fprintf(os.Stderr, "connection to %s left the relay for %s after %s\n",
				identity.Short(id.String()), p, time.Since(start).Round(time.Second))
			return
		case PathUnknown:
			return // nothing left to upgrade; repair climbs back down
		}
		t.dialDirect(id)
	}
}

// dialDirect races every address the far end can be reached at, local
// and public together. Racing beats walking: a local dial finishes in a
// millisecond or two while a punch across the Internet is still on its
// first round trip, so the nearer address wins whenever it exists. The
// relay needs no dialling — it is the connection this loop runs on, and
// it keeps carrying the session until something better lands.
//
// Candidates come from two places because neither is complete. The
// peerstore holds what libp2p learned, which over a relayed connection
// is public addresses only; askAddrs gets the rest from the peer itself,
// its local address above all. Relay addresses are dropped from both,
// since a relay cannot be the way to stop using a relay.
//
// The dial is forced past the existing connection. Without that, libp2p
// answers "already connected" and hands back the relayed connection,
// which is the thing being escaped.
func (t *Host) dialDirect(id peer.ID) {
	ask, cancelAsk := context.WithTimeout(t.ctx, upgradeDial)
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

	ctx, cancel := context.WithTimeout(t.ctx, upgradeDial)
	defer cancel()
	err := t.h.Connect(network.WithForceDirectDial(ctx, "upgrade"), peer.AddrInfo{ID: id, Addrs: direct})
	if err != nil && os.Getenv("RATATOSKR_DIAG") != "" {
		fmt.Fprintf(os.Stderr, "punch at %s missed on %d addresses: %v\n",
			identity.Short(id.String()), len(direct), err)
	}
}

// handleAddrs answers AddrsProto with this machine's own addresses, one
// per line, relay addresses stripped. Stripping happens here as well as
// on the reading side so a peer never has to filter what it was sent.
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
// It exists because libp2p drops every private address it is told over a
// public connection, and a relay circuit is a public connection — so two
// machines on one network that meet over a relay are never told each
// other's local address. Asking directly recovers it.
//
// Asked every tick rather than once, since the answer moves: the far end
// re-measures its public address, and a laptop that changes network
// changes its local one. It is a kilobyte, so the cost does not matter.
// Silence is not worth reporting; the peerstore's addresses are dialled
// on their own.
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
