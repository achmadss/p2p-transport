package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/identity"
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

	la, err := Start(a)
	if err != nil {
		t.Fatal(err)
	}
	defer la.Close()
	lb, err := Start(b)
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := la.Find(ctx, b.ID().String()); err != nil {
		t.Fatalf("a did not find b: %v", err)
	}
	if _, err := lb.Find(ctx, identity.Short(a.ID().String())); err != nil {
		t.Fatalf("b did not find a by fingerprint: %v", err)
	}
}

// Hearing our own announcement back must not look like a second machine.
func TestSelfIsNotAPeer(t *testing.T) {
	h := newHost(t)
	l, err := Start(h)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.HandlePeerFound(peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if got := l.Peers(); len(got) != 0 {
		t.Fatalf("found ourselves in the peer list: %v", got)
	}
}

func TestMatches(t *testing.T) {
	h := newHost(t)
	id := h.ID()
	full := id.String()

	for _, want := range []string{full, identity.Short(full)} {
		if !Matches(id, want) {
			t.Errorf("%q did not match %s", want, full)
		}
	}
	for _, want := range []string{"", "nope", full[:8], full + "x"} {
		if Matches(id, want) {
			t.Errorf("%q matched %s but should not have", want, full)
		}
	}
}

func TestFindGivesUp(t *testing.T) {
	l, err := Start(newHost(t))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, err := l.Find(ctx, "AAAA-AAAA"); err == nil {
		t.Fatal("Find returned a peer that does not exist")
	}
}
