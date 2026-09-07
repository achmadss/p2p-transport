package transport

import (
	"testing"

	"github.com/multiformats/go-multiaddr"
)

// The carrier in TODO.md step 3 advances its external port and holds.
// A span of candidates is what let a punch cross it, so the span has to
// come out the other side intact and pointing at real ports.
func TestSpreadPorts(t *testing.T) {
	a := multiaddr.StringCast("/ip4/182.6.166.95/udp/9314/quic-v1")

	if got := spreadPorts(a, 0); len(got) != 1 || !got[0].Equal(a) {
		t.Fatalf("off by default: got %v", got)
	}

	got := spreadPorts(a, 2)
	want := []string{
		"/ip4/182.6.166.95/udp/9314/quic-v1",
		"/ip4/182.6.166.95/udp/9315/quic-v1",
		"/ip4/182.6.166.95/udp/9316/quic-v1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d addresses, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("address %d: got %s, want %s", i, got[i], w)
		}
	}

	// A circuit address names the relay's port, and the relay listens on
	// one. Eight more are eight timeouts for every dialler.
	circuit := multiaddr.StringCast("/ip4/103.181.143.222/udp/4001/quic-v1/p2p/12D3KooWLD13rmEfjNt8DJd3c86x93LHUGyQLizjvoXZsS3cPk5w/p2p-circuit")
	if got := spreadPorts(circuit, 2); len(got) != 1 {
		t.Errorf("circuit must not spread: got %v", got)
	}

	// A TCP address has no port that creeps, and nothing listens on the
	// ones above it.
	tcp := multiaddr.StringCast("/ip4/182.6.166.95/tcp/9314")
	if got := spreadPorts(tcp, 2); len(got) != 1 {
		t.Errorf("tcp must not spread: got %v", got)
	}
}
