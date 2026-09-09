package discovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// The whole point of step 2: two machines find each other with no
// server and no Internet.
func TestTwoHostsFindEachOther(t *testing.T) {
	a, b := newHost(t), newHost(t)

	la, err := Start(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer la.Close()
	lb, err := Start(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := waitFor(ctx, la, b.ID()); err != nil {
		t.Fatalf("a did not find b: %v", err)
	}
	if err := waitFor(ctx, lb, a.ID()); err != nil {
		t.Fatalf("b did not find a: %v", err)
	}
}

// waitFor polls rather than plumbing a channel into the test. mDNS
// answers in milliseconds; anything slower than the context is a
// failure worth reporting as one.
func waitFor(ctx context.Context, l *LAN, want peer.ID) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		for _, p := range l.Peers() {
			if p.ID == want {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s never answered on this network", want)
		case <-tick.C:
		}
	}
}

// Hearing our own announcement back must not look like a second machine.
func TestSelfIsNotAPeer(t *testing.T) {
	h := newHost(t)
	l, err := Start(h, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.HandlePeerFound(peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if got := l.Peers(); len(got) != 0 {
		t.Fatalf("found ourselves in the peer list: %v", got)
	}
}

// found must reach the caller, because OnLAN above the seam is built on
// it and nothing else tells an application a machine appeared.
func TestFoundIsReported(t *testing.T) {
	h := newHost(t)
	told := make(chan peer.ID, 1)
	l, err := Start(h, func(info peer.AddrInfo) { told <- info.ID })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	other := newHost(t)
	l.HandlePeerFound(peer.AddrInfo{ID: other.ID(), Addrs: other.Addrs()})
	select {
	case got := <-told:
		if got != other.ID() {
			t.Fatalf("told about %s, want %s", got, other.ID())
		}
	case <-time.After(time.Second):
		t.Fatal("a peer was found and nobody was told")
	}
}
