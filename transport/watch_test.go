package transport

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestWatchDeliversTheCurrentPathThenTheChange is the whole contract in
// one run. A caller that had to ask for the starting path separately
// could miss a change between the two calls, and one told only about
// changes would wait for an event on a peer already connected.
func TestWatchDeliversTheCurrentPathThenTheChange(t *testing.T) {
	a, b := newHost(t), newHost(t)
	h := &Host{h: a, done: make(chan struct{})}
	defer h.Close()
	h.watchForRelayed()

	paths, stop := h.watch(b.ID())
	defer stop()

	if got := next(t, paths); got != PathUnknown {
		t.Fatalf("watching an unconnected machine started at %s, want unknown", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, paths); got != PathLAN {
		t.Fatalf("connecting over loopback reported %s, want lan", got)
	}
}

// TestWatchOfAConnectedPeerStartsThere: the seed is unknown and the real
// path is published over it, so a watch begun after the connection must
// still open on that connection rather than on the placeholder.
func TestWatchOfAConnectedPeerStartsThere(t *testing.T) {
	a, b := newHost(t), newHost(t)
	h := &Host{h: a, done: make(chan struct{})}
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Connect(ctx, peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}); err != nil {
		t.Fatal(err)
	}

	paths, stop := h.watch(b.ID())
	defer stop()
	if got := next(t, paths); got != PathLAN {
		t.Fatalf("watching an already connected machine started at %s, want lan", got)
	}
}

// TestWatchEndsWithTheHost: a caller ranging over the channel must stop
// when the host does, or it waits for an event that can no longer come.
// Cancelling afterwards must still be safe, since the usual shape is a
// deferred stop outliving a deferred Close.
func TestWatchEndsWithTheHost(t *testing.T) {
	h := &Host{h: newHost(t), done: make(chan struct{})}
	paths, stop := h.watch(newHost(t).ID())
	next(t, paths) // the seeded path

	h.Close()
	if _, open := <-paths; open {
		t.Fatal("the host closed and the watch went on")
	}
	stop()
	h.Close()
}

// TestPublishKeepsTheLatestPath: the hooks that publish are libp2p's own
// and must never block on a slow reader, so the buffer holds one value
// and a stale one is dropped. What arrives is where the peer is now.
func TestPublishKeepsTheLatestPath(t *testing.T) {
	h := &Host{h: newHost(t), done: make(chan struct{})}
	defer h.Close()

	id := newHost(t).ID()
	paths, stop := h.watch(id)
	defer stop()

	for _, p := range []Path{PathRelay, PathDirect, PathLAN} {
		h.publish(id, p) // nobody is reading in between
	}
	if got := next(t, paths); got != PathLAN {
		t.Fatalf("three changes with no reader left %s, want the last one", got)
	}
}

func next(t *testing.T, paths <-chan Path) Path {
	t.Helper()
	select {
	case p := <-paths:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no path arrived")
		return PathUnknown
	}
}
