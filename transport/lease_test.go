package transport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// A relay address becomes something to dial a peer through, and the set
// says when it actually changed. Everything the lease loop does hangs off
// that answer: a set that reports a change it did not have re-dials the
// relay on every renewal.
func TestRelaySetChanges(t *testing.T) {
	relay := newHost(t)
	info := peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}

	set := newRelaySet()
	if !set.set([]peer.AddrInfo{info}) {
		t.Fatal("the first relay was not a change")
	}
	got := set.dialAddrs()
	if len(got) == 0 {
		t.Fatal("no way to dial through the relay")
	}
	for _, a := range got {
		if !strings.HasSuffix(a.String(), "/p2p-circuit") {
			t.Fatalf("%s does not go through the relay", a)
		}
		if !strings.Contains(a.String(), relay.ID().String()) {
			t.Fatalf("%s does not name the relay", a)
		}
	}

	if set.set([]peer.AddrInfo{info}) {
		t.Fatal("the same relay was reported as a change")
	}
	if !set.set(nil) {
		t.Fatal("losing the relay was not a change")
	}
	if len(set.dialAddrs()) != 0 {
		t.Fatal("a relay that was taken away is still being dialled")
	}
}

// A change has to wake the machinery that holds the relay open, or a
// machine moved to another relay waits out the next tick before it can
// be reached there.
func TestRelaySetWakesOnChange(t *testing.T) {
	relay := newHost(t)
	set := newRelaySet()
	set.set([]peer.AddrInfo{{ID: relay.ID(), Addrs: relay.Addrs()}})

	select {
	case <-set.changed:
	default:
		t.Fatal("a new relay did not wake anything")
	}
}

// Several addresses for one coordinator are one machine with several
// ways in. Two different machines are a mistake worth refusing, since
// only the first would ever be asked.
func TestMergeInfos(t *testing.T) {
	a, b := newHost(t), newHost(t)
	first := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()[:1]}
	second := peer.AddrInfo{ID: a.ID(), Addrs: a.Addrs()[:1]}

	got, err := mergeInfos([]peer.AddrInfo{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID() || len(got.Addrs) != 2 {
		t.Fatalf("merged into %s with %d addresses, want %s with 2", got.ID, len(got.Addrs), a.ID())
	}

	if _, err := mergeInfos([]peer.AddrInfo{first, {ID: b.ID(), Addrs: b.Addrs()}}); err == nil {
		t.Fatal("two different machines merged into one")
	}
}

// The whole round trip: a coordinator answers, and the machine ends up
// dialling through the relay it was given. This is also the check that
// the two ends agree on the wire format, since the coordinator's reply
// is built here exactly as bifrost builds it.
func TestAskLease(t *testing.T) {
	coord, relay := newHost(t), newHost(t)
	relayAddrs := p2pAddrs(peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()})

	coord.SetStreamHandler(wire.LeaseProto, func(s network.Stream) {
		defer s.Close()
		json.NewEncoder(s).Encode(wire.Lease{Relay: relayAddrs, TTL: 120 * time.Second})
	})

	h := wrap(newHost(t))
	defer h.Close()

	next, err := h.askLease(peer.AddrInfo{ID: coord.ID(), Addrs: coord.Addrs()})
	if err != nil {
		t.Fatal(err)
	}
	if next != 60*time.Second {
		t.Fatalf("asking again in %s, want half of the two minute lease", next)
	}
	if got := h.relays.dialAddrs(); len(got) == 0 {
		t.Fatal("the leased relay is not being dialled through")
	}
	if got := h.relays.peers(); len(got) != 1 || got[0].ID != relay.ID() {
		t.Fatalf("leased %v, want the relay %s", got, relay.ID())
	}
}

// Being told no relay is an answer, not a failure. The machine keeps
// running on the local network and on whatever it can dial directly.
func TestAskLeaseWithNoRelay(t *testing.T) {
	coord := newHost(t)
	coord.SetStreamHandler(wire.LeaseProto, func(s network.Stream) {
		defer s.Close()
		json.NewEncoder(s).Encode(wire.Lease{TTL: 120 * time.Second})
	})

	h := wrap(newHost(t))
	defer h.Close()

	if _, err := h.askLease(peer.AddrInfo{ID: coord.ID(), Addrs: coord.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if got := h.relays.dialAddrs(); len(got) != 0 {
		t.Fatalf("got %v, want nothing to dial through", got)
	}
}

// A coordinator that cannot be reached must leave the relay alone. The
// machine is transferring through it right now, and an expired promise
// is no reason to tear a working path down.
func TestLeaseFailureKeepsTheRelay(t *testing.T) {
	relay, gone := newHost(t), newHost(t)
	h := wrap(newHost(t))
	defer h.Close()
	h.relays.set([]peer.AddrInfo{{ID: relay.ID(), Addrs: relay.Addrs()}})
	before := len(h.relays.dialAddrs())

	// Nothing is listening for a lease on that host, so the ask fails
	// on the stream rather than on the dial.
	if _, err := h.askLease(peer.AddrInfo{ID: gone.ID(), Addrs: gone.Addrs()}); err == nil {
		t.Fatal("a coordinator with no lease handler answered one")
	}
	if got := len(h.relays.dialAddrs()); got != before {
		t.Fatalf("%d ways through the relay after a failed ask, want the %d from before", got, before)
	}
}

// A relay that dies must be asked about now, not at the next renewal:
// until another one is placed, nothing outside this network can reach
// this machine.
func TestRelayLossWakesTheLeaseLoop(t *testing.T) {
	relay, other := newHost(t), newHost(t)
	set := newRelaySet()
	set.set([]peer.AddrInfo{{ID: relay.ID(), Addrs: relay.Addrs()}})

	set.gone(other.ID())
	select {
	case <-set.lost:
		t.Fatal("some unrelated machine leaving woke the lease loop")
	default:
	}

	set.gone(relay.ID())
	select {
	case <-set.lost:
	default:
		t.Fatal("the relay going away did not wake the lease loop")
	}
}

// A relay answers on several transports and the coordinator names it
// once per address. Kept apart, one relay looks like several machines
// with one address each, and whatever asks for a single candidate gets
// one address instead of one relay.
func TestOneRelayWithManyAddressesStaysOneRelay(t *testing.T) {
	h := newHost(t)
	defer h.Close()

	// One relay, listed once per transport it answers on, which is what
	// the coordinator sends.
	var apart []peer.AddrInfo
	for _, a := range []string{"/ip4/10.0.0.1/udp/4001/quic-v1", "/ip4/10.0.0.1/tcp/4001", "/ip4/10.0.0.1/tcp/443"} {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			t.Fatal(err)
		}
		apart = append(apart, peer.AddrInfo{ID: h.ID(), Addrs: []multiaddr.Multiaddr{ma}})
	}

	set := newRelaySet()
	set.set(apart)
	got := set.peers()
	if len(got) != 1 {
		t.Fatalf("one relay came back as %d machines", len(got))
	}
	if len(got[0].Addrs) != len(apart) {
		t.Fatalf("the merged relay kept %d of %d addresses", len(got[0].Addrs), len(apart))
	}
}
