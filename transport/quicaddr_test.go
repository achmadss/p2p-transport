package transport

import "testing"

// The reflector answers in host:port and the relay answers in
// multiaddrs. Feeding the first to the parser built for the second
// discarded every measurement while still logging it, so the punch read
// as working and aimed at a stale port.
func TestQuicAddrFromReflectorAnswer(t *testing.T) {
	got, ok := quicAddr("182.6.166.95:35722")
	if !ok || got.String() != "/ip4/182.6.166.95/udp/35722/quic-v1" {
		t.Fatalf("got %v, %v", got, ok)
	}

	// The same standard as a relay-reported address: carrier-grade NAT
	// space names nobody, and neither does a malformed answer.
	for _, bad := range []string{
		"100.64.0.1:1234", // CGNAT: reachable from nowhere
		"182.6.166.95",    // no port
		"182.6.166.95:0",
		"not-an-address",
		"",
	} {
		if a, ok := quicAddr(bad); ok {
			t.Errorf("%q must be refused, got %v", bad, a)
		}
	}
}
