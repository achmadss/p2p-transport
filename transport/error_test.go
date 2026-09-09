package transport

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TestOpenTellsRefusedFromUnreachable is the difference a caller acts
// on: a machine that cannot be reached is worth retrying when the path
// changes, and one that does not answer to a protocol name never will
// be. Both arrive as a failure to open a stream and nothing but the
// error tells them apart.
func TestOpenTellsRefusedFromUnreachable(t *testing.T) {
	a, b := newHost(t), newHost(t)
	h := wrap(a)
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}

	_, err := h.Open(ctx, PeerID(b.ID().String()), "/nobody/handles/this/1.0.0")
	if !errors.Is(err, ErrNotHandled) {
		t.Errorf("a machine with no such handler gave %v, want ErrNotHandled", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Errorf("a machine that answered was called unreachable: %v", err)
	}

	gone, cancelGone := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelGone()
	_, err = h.Open(gone, PeerID(newHost(t).ID().String()), "/nobody/handles/this/1.0.0")
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("a machine with no known address gave %v, want ErrUnreachable", err)
	}
}

// TestStreamDeathIsUnreachableAndEOFIsNot: a transfer ending normally
// and one whose machine vanished both stop the read, so io.EOF must
// survive untagged or every completed transfer reads as a failure.
func TestStreamDeathIsUnreachableAndEOFIsNot(t *testing.T) {
	a, b := newHost(t), newHost(t)
	h := wrap(a)
	defer h.Close()

	const proto = "/test/hold/1.0.0"
	b.SetStreamHandler(proto, func(s network.Stream) { <-time.After(time.Minute) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := h.Open(ctx, PeerID(b.ID().String()), proto)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	b.Close()
	if _, err := io.ReadAll(s); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("reading from a machine that went away gave %v, want ErrUnreachable", err)
	}
	if got := died(io.EOF); got != io.EOF {
		t.Errorf("end of stream was tagged as %v", got)
	}
}
