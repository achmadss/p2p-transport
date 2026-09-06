package transport

import (
	"context"
	"crypto/rand"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// TestUsableObservedRefusesWhatItCannotPromise pins the filter. Every
// refusal here is an address that would cost a peer a dial that can
// never arrive.
func TestUsableObservedRefusesWhatItCannotPromise(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"/ip4/203.0.113.7/udp/4242/quic-v1", true},
		{" /ip4/203.0.113.7/udp/4242/quic-v1\n", true}, // trimmed
		{"/ip4/203.0.113.7/tcp/4242", false},           // UDP was measured, not TCP
		{"/ip4/192.168.1.5/udp/4242/quic-v1", false},   // private
		{"/ip4/127.0.0.1/udp/4242/quic-v1", false},     // loopback
		{"/ip4/100.100.1.5/udp/4242/quic-v1", false},   // carrier-grade NAT
		{"/ip4/203.0.113.7/udp/4242/quic-v1/p2p-circuit", false},
		{"not an address", false},
		{"", false},
	}
	for _, c := range cases {
		if _, ok := usableObserved(c.addr); ok != c.want {
			t.Errorf("usableObserved(%q) = %v, want %v", c.addr, ok, c.want)
		}
	}
}

// TestHandleObservedReportsTheDialingSocket is the round trip: the
// address the server reports must be the port the client actually dialled
// from, because guessing it from another socket is the bug this replaces.
func TestHandleObservedReportsTheDialingSocket(t *testing.T) {
	server, client := newHost(t), newHost(t)
	HandleObserved(server)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := client.NewStream(ctx, server.ID(), ObservedProto)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := io.ReadAll(io.LimitReader(s, 256))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := s.Conn().LocalMultiaddr().String()
	if !strings.Contains(got, want) {
		t.Fatalf("server saw %q, client dialled from %q", got, want)
	}
	// Loopback is not an address to advertise, and the filter must say so
	// even when the measurement itself was correct.
	if _, ok := usableObserved(got); ok {
		t.Fatalf("advertised a loopback address: %q", got)
	}
}

func newHost(t *testing.T) host.Host {
	t.Helper()
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(
		libp2p.Identity(key),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// TestPunchFilterDropsWhatItCannotRoute pins the rule that cost a whole
// evening: a machine with no IPv6 must not spend its punch window
// dialling a peer's IPv6 addresses, and a machine with IPv6 must keep
// them, because that path has no NAT in it at all.
func TestPunchFilterDropsWhatItCannotRoute(t *testing.T) {
	offered := []multiaddr.Multiaddr{
		multiaddr.StringCast("/ip4/182.6.161.1/udp/41654/quic-v1"),
		multiaddr.StringCast("/ip6/2404:c0:5d10:355b::1/udp/54032/quic-v1"),
		multiaddr.StringCast("/ip6/2404:c0:5d10:355b::1/tcp/62469"),
	}

	no := punchFilter{hasIPv6: func() bool { return false }}
	got := no.FilterRemote("", offered)
	if len(got) != 1 || got[0].String() != "/ip4/182.6.161.1/udp/41654/quic-v1" {
		t.Fatalf("without IPv6, punch set = %v, want the IPv4 address alone", got)
	}

	yes := punchFilter{hasIPv6: func() bool { return true }}
	if got := yes.FilterRemote("", offered); len(got) != len(offered) {
		t.Fatalf("with IPv6, punch set = %v, want all %d", got, len(offered))
	}
}
