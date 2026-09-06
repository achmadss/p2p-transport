package transport

import (
	"net"
	"testing"
)

// The tap is only worth having if its counts are the truth. A packet to
// the watched address must be counted, one to any other address must not
// — a tap that counts the relay's traffic as the peer's would report a
// punch that never happened.
func TestTapCountsOnlyTheWatchedPeer(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tap := &tapped{PacketConn: c, watch: "127.0.0.1", sent: map[string]int{}, recv: map[string]int{}}
	defer tap.Close()

	if _, err := tap.WriteTo([]byte("x"), peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	// An address the tap is not watching must leave both tallies alone.
	// A loopback socket cannot reach it, and the send failing is fine:
	// the tap counts before it writes, so a miscount would still show.
	tap.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(198, 51, 100, 7), Port: 9})

	if _, err := peer.WriteToUDP([]byte("y"), tap.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, _, err := tap.ReadFrom(buf); err != nil {
		t.Fatal(err)
	}

	if got := tap.sent[peer.LocalAddr().String()]; got != 1 {
		t.Errorf("sent to the peer counted %d, want 1", got)
	}
	if len(tap.sent) != 1 {
		t.Errorf("tallied %d destinations, want only the watched one: %v", len(tap.sent), tap.sent)
	}
	if got := tap.recv[peer.LocalAddr().String()]; got != 1 {
		t.Errorf("received from the peer counted %d, want 1", got)
	}
}
