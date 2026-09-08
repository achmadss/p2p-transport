package transport

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// TestUpgradeStopsOnceThePathIsDirect is the loop's only exit that
// matters. A punch loop that keeps dialling a peer it already reaches
// directly is a dial every five seconds for the life of the session, on
// a connection that has nothing left to fix.
func TestUpgradeStopsOnceThePathIsDirect(t *testing.T) {
	defer swap(&upgradeEvery, 10*time.Millisecond)()

	a, b := newHost(t), newHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}

	h := &Host{h: a, done: make(chan struct{})}
	defer h.Close()

	done := make(chan struct{})
	go func() { h.upgrade(b.ID()); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop kept punching at a peer it already reaches directly")
	}
}

// TestUpgradeStopsWhenThePeerIsGone: no connection is not a reason to
// keep dialling either. The peer's addresses are unknown here, so a loop
// that did not check would spin on a peerstore that never fills.
func TestUpgradeStopsWhenThePeerIsGone(t *testing.T) {
	defer swap(&upgradeEvery, 10*time.Millisecond)()

	h := &Host{h: newHost(t), done: make(chan struct{})}
	defer h.Close()

	done := make(chan struct{})
	go func() { h.upgrade(newHost(t).ID()); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop kept punching at a peer it has no connection to")
	}
}

// TestDirectOnlyDropsCircuits guards the rule both callers share. A
// circuit address left in the set turns the upgrade dial into a second
// relayed connection, which would report itself as a successful punch.
func TestDirectOnlyDropsCircuits(t *testing.T) {
	in := addrs(t,
		"/ip4/203.0.113.7/udp/4242/quic-v1",
		"/ip4/198.51.100.1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN/p2p-circuit",
		"/ip4/192.168.1.5/udp/4242/quic-v1",
	)
	got := directOnly(in)
	if len(got) != 2 || !got[0].Equal(in[0]) || !got[1].Equal(in[2]) {
		t.Fatalf("directOnly kept %v", got)
	}
}

func addrs(t *testing.T, ss ...string) []multiaddr.Multiaddr {
	t.Helper()
	var out []multiaddr.Multiaddr
	for _, s := range ss {
		a, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// swap sets a package knob for one test and gives back the undo.
func swap[T any](p *T, v T) func() {
	old := *p
	*p = v
	return func() { *p = old }
}
