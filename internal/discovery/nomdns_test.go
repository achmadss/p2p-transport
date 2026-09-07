package discovery

import (
	"testing"

	"github.com/libp2p/go-libp2p"
)

// The machine this exists for takes its terminal down the moment the
// agent opens a multicast socket, so the off switch has to work without
// opening one — and shutting down must not be what crashes instead.
func TestNoMDNSStartsAndStops(t *testing.T) {
	t.Setenv("RATATOSKR_NO_MDNS", "1")

	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	l, err := Start(h)
	if err != nil {
		t.Fatalf("Start with mdns off: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close with mdns off: %v", err)
	}
}
