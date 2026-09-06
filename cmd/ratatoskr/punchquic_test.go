package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// TestSpeakQUICCarriesAStreamBothWays checks the half of punch-quic that
// is not a person typing addresses: two QUIC transports on two sockets,
// one listening and one dialling, and a message that has to come back.
// If this breaks, the diagnostic would blame a network for its own bug.
func TestSpeakQUICCarriesAStreamBothWays(t *testing.T) {
	lc, dc := loopbackUDP(t), loopbackUDP(t)
	lt := &quic.Transport{Conn: lc}
	dt := &quic.Transport{Conn: dc}
	t.Cleanup(func() { lt.Close(); dt.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- speakQUIC(ctx, lt, dc.LocalAddr(), "listen") }()

	if err := speakQUIC(ctx, dt, lc.LocalAddr(), "dial"); err != nil {
		t.Fatalf("dial side: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("listen side: %v", err)
	}
}

func loopbackUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
