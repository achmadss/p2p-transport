package transport

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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

// watchForRelayed starts a punch loop for every peer that arrives over a
// relay, in either direction. Both ends must dial for either to get
// through, and both start counting from the same event — the relayed
// connection they share — so their ticks land within a round trip of
// each other without anything being negotiated. That is the agreement
// DCUtR spends a round trip reaching, had for free, and it is why this
// loop does not jitter its clock.
func (t *Host) watchForRelayed() {
	t.h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			if pathOfConn(c) == PathRelay {
				go t.upgrade(c.RemotePeer())
			}
		},
	})
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
		switch t.PathTo(id) {
		case PathDirect, PathLAN:
			// Someone's dial landed — ours, theirs, or DCUtR's. Which
			// one is not worth finding out; the path is measured from
			// the connection either way.
			fmt.Fprintf(os.Stderr, "connection to %s went direct after %s\n",
				identity.Short(id.String()), time.Since(start).Round(time.Second))
			return
		case PathUnknown:
			return // no connection at all any more
		}
		t.dialDirect(id)
	}
}

// dialDirect tries every address the far end has published that is not a
// circuit.
//
// The addresses come from the peerstore, which identify fills over the
// relayed connection and refills whenever the far end's address set
// changes — and it changes every time their refresher measures, which is
// the whole reason the measuring was moved onto a clock. So this dials
// what the peer believes right now, not what it believed when the
// relayed connection opened.
//
// The dial is forced past the connection that already exists: without
// that, libp2p answers "already connected" and returns the relayed
// connection, which is the thing being escaped.
func (t *Host) dialDirect(id peer.ID) {
	direct := directOnly(t.h.Peerstore().Addrs(id))
	if len(direct) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), upgradeDial)
	defer cancel()
	err := t.h.Connect(network.WithForceDirectDial(ctx, "upgrade"), peer.AddrInfo{ID: id, Addrs: direct})
	if err != nil && os.Getenv("RATATOSKR_DIAG") != "" {
		fmt.Fprintf(os.Stderr, "punch at %s missed: %v\n", identity.Short(id.String()), err)
	}
}
