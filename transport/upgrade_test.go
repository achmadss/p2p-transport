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

// TestAskAddrsCarriesWhatIdentifyDrops is the reason AddrsProto exists.
// go-libp2p's identify filters every non-public address out of what it
// stores when the connection carrying it is public, and a circuit
// through a relay is public — so over a relay the peerstore holds
// exactly the addresses that need a hole punched and none of the LAN
// address that needs nothing at all. This asks the peer directly, so
// what comes back must be its listen set, private addresses included.
func TestAskAddrsCarriesWhatIdentifyDrops(t *testing.T) {
	a, b := newHost(t), newHost(t)
	handleAddrs(b)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}

	h := &Host{h: a, done: make(chan struct{})}
	defer h.Close()

	got := h.askAddrs(ctx, b.ID())
	if len(got) != len(b.Addrs()) {
		t.Fatalf("asked for %d addresses, got %d: %v", len(b.Addrs()), len(got), got)
	}
	for _, want := range b.Addrs() {
		if !has(got, want) {
			t.Fatalf("%s was not in the answer: %v", want, got)
		}
	}
}

// TestPathLadder pins the order a running transfer moves along. Getting
// it wrong is silent: a transfer would either refuse to leave the relay
// or leave the LAN for the Internet, and both still deliver the bytes.
func TestPathLadder(t *testing.T) {
	ladder := []Path{PathLAN, PathDirect, PathRelay, PathUnknown}
	for i, better := range ladder {
		for _, worse := range ladder[i+1:] {
			if !better.BetterThan(worse) || worse.BetterThan(better) {
				t.Fatalf("%s and %s are the wrong way round", better, worse)
			}
		}
		if better.BetterThan(better) {
			t.Fatalf("%s is better than itself", better)
		}
	}
}

// TestRepairReadsTheRungNotTheEvent guards the branch both notifee
// hooks share. A peer already reached directly must start nothing, or
// every connection would carry a dial every five seconds for its whole
// life; a peer on the relay climbs; a peer with nothing left is chased
// back down. The decision has to come from the best path rather than
// from the connection that fired the event, because the same notifee
// now runs on connect and disconnect both.
// TestRepairGivesUpOnAPeerThatIsGone pins the one way the ladder loop
// can fail to end. It walks both directions now, so an ending it does
// not recognise is not a stopped goroutine but a spin: restore gives up
// after restoreFor, the path is still unknown, and a loop that read
// that as "try again" would dial forever and never release the peer.
func TestRepairGivesUpOnAPeerThatIsGone(t *testing.T) {
	defer swap(&upgradeEvery, 10*time.Millisecond)()
	defer swap(&restoreFor, 100*time.Millisecond)()

	h := &Host{h: newHost(t), done: make(chan struct{})}
	defer h.Close()

	gone := newHost(t).ID()
	h.repair(gone)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, held := h.working.Load(gone); !held {
			return // the loop ended and let the peer go
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the ladder loop never released a peer it had given up on")
}

func TestRepairReadsTheRungNotTheEvent(t *testing.T) {
	a, b := newHost(t), newHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}

	h := &Host{h: a, done: make(chan struct{})}
	defer h.Close()

	if p := h.repair(b.ID()); p != PathLAN {
		t.Errorf("a peer reached over loopback repaired as %s, want lan", p)
	}
	if p := h.repair(newHost(t).ID()); p != PathUnknown {
		t.Errorf("a peer with no connection repaired as %s, want unknown", p)
	}
}
