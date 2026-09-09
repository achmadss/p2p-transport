package transport

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestManyCyclesLeakNoGoroutines: every disconnection starts a goroutine
// that chases the peer, and every connection decides whether one is
// needed. A cycle that does not clean up after itself costs a goroutine
// each time, and a laptop that sleeps and wakes all day does thousands.
func TestManyCyclesLeakNoGoroutines(t *testing.T) {
	defer swap(&upgradeEvery, time.Millisecond)()
	defer swap(&restoreFor, 5*time.Millisecond)()

	h := wrap(newHost(t))
	defer h.Close()
	h.watchForRelayed()

	b := newHost(t)
	info := peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}

	before := runtime.NumGoroutine()
	for i := 0; i < 1000; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := h.h.Connect(ctx, info)
		cancel()
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if err := h.h.Network().ClosePeer(b.ID()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	// A slack rather than an exact count: libp2p starts and stops its own
	// goroutines underneath, and what is being caught here is a thousand
	// of them, not ten.
	settle(t, before+20)
}

// settle waits for the goroutine count to come back down. The last chase
// the loop started is still finishing when the loop ends.
func settle(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := runtime.NumGoroutine()
		if got <= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines still running, want at most %d", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
