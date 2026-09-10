package main

import (
	"context"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/shape"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

func newTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// The whole design bet: wrapping the two calls the relay gets its
// streams from is enough to shape every byte it forwards, with no fork
// of the relay itself. This is the handler side — a machine dialling in
// — and it is the one that carries the bulk of an upload.
func TestShapedHostSlowsAStream(t *testing.T) {
	const (
		perSecond = 256 << 10 // 256 KiB/s
		payload   = 768 << 10 // three seconds' worth, less the first burst
	)
	relay, sender := newTestHost(t), newTestHost(t)

	limits := shape.New(0)
	limits.Admit(sender.ID(), "alice", perSecond)
	wrapped := shapedHost{Host: relay, limits: limits}

	const proto = protocol.ID("/test/1.0.0")
	read := make(chan int64, 1)
	wrapped.SetStreamHandler(proto, func(s network.Stream) {
		defer s.Close()
		n, _ := io.Copy(io.Discard, s)
		read <- n
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sender.Connect(ctx, peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := sender.NewStream(ctx, relay.ID(), proto)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, payload)
	rand.Read(buf)
	start := time.Now()
	if _, err := s.Write(buf); err != nil {
		t.Fatal(err)
	}
	s.CloseWrite()

	select {
	case n := <-read:
		if n != payload {
			t.Fatalf("read %d bytes, want %d", n, payload)
		}
	case <-ctx.Done():
		t.Fatal("the transfer never finished")
	}

	// Two seconds is the floor: the first burst is free, and what is
	// left is two seconds at the subject's rate. Unshaped this is
	// milliseconds over loopback, so the bound only has to tell the two
	// apart.
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("%d bytes through a %d byte/s subject took %s; it was not shaped", payload, perSecond, took)
	}
}

// The other half of the byte path: the stream the relay opens to the
// machine being dialled. Missing it would shape everything a machine
// uploads and leave everything it downloads unlimited.
func TestShapedHostShapesTheStreamsItOpens(t *testing.T) {
	const (
		perSecond = 256 << 10
		payload   = 768 << 10
	)
	relay, other := newTestHost(t), newTestHost(t)

	limits := shape.New(0)
	limits.Admit(other.ID(), "alice", perSecond)
	wrapped := shapedHost{Host: relay, limits: limits}

	const proto = protocol.ID("/test/1.0.0")
	buf := make([]byte, payload)
	rand.Read(buf)
	other.SetStreamHandler(proto, func(s network.Stream) {
		defer s.Close()
		s.Write(buf)
		s.CloseWrite()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := relay.Connect(ctx, peer.AddrInfo{ID: other.ID(), Addrs: other.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := wrapped.NewStream(ctx, other.ID(), proto)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	start := time.Now()
	n, err := io.Copy(io.Discard, s)
	if err != nil {
		t.Fatal(err)
	}
	if n != payload {
		t.Fatalf("read %d bytes, want %d", n, payload)
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("%d bytes from a %d byte/s subject took %s; it was not shaped", payload, perSecond, took)
	}
}
