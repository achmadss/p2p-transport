package transport

import "testing"

// The reflector answers in host:port and the relay answers in
// multiaddrs. Feeding the first to the parser built for the second
// discarded every measurement while still logging it, so the punch read
// as working and aimed at a stale port.
//
// The span above the measured port is the second half: this carrier
// gives each new destination the next port, so the reflector gets one
// and the peer gets the one after.
func TestQuicAddrsFromReflectorAnswer(t *testing.T) {
	got := quicAddrs("182.6.166.95:35722", 2)
	want := []string{
		"/ip4/182.6.166.95/udp/35722/quic-v1",
		"/ip4/182.6.166.95/udp/35723/quic-v1",
		"/ip4/182.6.166.95/udp/35724/quic-v1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d addresses, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("address %d: got %s, want %s", i, got[i], w)
		}
	}

	// Asking for none still yields the measured address itself.
	if one := quicAddrs("182.6.166.95:35722", 0); len(one) != 1 {
		t.Errorf("no span must still offer the measurement: %v", one)
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
		if a := quicAddrs(bad, 2); a != nil {
			t.Errorf("%q must be refused, got %v", bad, a)
		}
	}

	// A port near the ceiling stops at it rather than wrapping into
	// nonsense.
	if a := quicAddrs("182.6.166.95:65534", 4); len(a) != 2 {
		t.Errorf("near 65535 the span must stop: got %v", a)
	}
}
