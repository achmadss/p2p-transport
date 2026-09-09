// Command heimdall is a relay node.
//
// It forwards an encrypted session it holds no key for, so it cannot
// read a byte of what it carries. What it does unavoidably learn is
// which machines talked, when, and how many bytes — true of any relay,
// and stated here rather than implied.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"
)

func toMultiaddrs(in []string) ([]multiaddr.Multiaddr, error) {
	out := make([]multiaddr.Multiaddr, 0, len(in))
	for _, a := range in {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			return nil, fmt.Errorf("HEIMDALL_ANNOUNCE: bad address %q: %w", a, err)
		}
		out = append(out, ma)
	}
	return out, nil
}

// Defaults. Every one is overridable; see usage below.
const (
	defaultPort     = 4001
	defaultData     = 64 << 30
	defaultDuration = time.Hour

	// fallbackTCP is the second TCP port heimdall answers on. Some
	// networks pass 443 and drop everything else, and a relay nobody on
	// such a network can reach is not a fallback. Binding it needs root
	// or CAP_NET_BIND_SERVICE; without either, that listener does not
	// come up and the others still do.
	fallbackTCP = 443
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Print(`usage: heimdall

environment (empty means the default):
  RATATOSKR_CONFIG_DIR       where identity.key lives
  HEIMDALL_PORT              listen port, UDP and TCP          (4001)
                             TCP 443 is answered as well when the
                             process may bind it, for networks that
                             pass only 443.
  HEIMDALL_ANNOUNCE          comma-separated public multiaddrs to
                             advertise instead of what the machine
                             sees. Needed behind a cloud NAT, and it
                             replaces the whole set — list the 443
                             address too if you want it reachable.
  HEIMDALL_CIRCUIT_DATA      bytes per circuit, K/M/G suffixes  (64G)
  HEIMDALL_CIRCUIT_DURATION  lifetime per circuit               (1h)
`)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "heimdall:", err)
		os.Exit(1)
	}
}

func run() error {
	id, err := identity.LoadOrCreate("")
	if err != nil {
		return err
	}

	port := config.Int("HEIMDALL_PORT", defaultPort)

	opts := []libp2p.Option{
		libp2p.Identity(id.PrivateKey()),
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port),
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", fallbackTCP),
		),
	}

	// A relay behind a cloud provider's NAT sees only its private
	// address, and announcing that sends every machine at an address
	// that reaches nothing. HEIMDALL_ANNOUNCE names the public one.
	if announce := config.List("HEIMDALL_ANNOUNCE"); len(announce) > 0 {
		addrs, err := toMultiaddrs(announce)
		if err != nil {
			return err
		}
		opts = append(opts, libp2p.AddrsFactory(func([]multiaddr.Multiaddr) []multiaddr.Multiaddr {
			return addrs
		}))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("start host: %w", err)
	}
	defer h.Close()

	// The default circuit allows 128 KB over two minutes, sized for
	// signalling rather than for files. A relayed transfer here is a
	// whole video, so the limit is raised to one a person might reach.
	//
	// ponytail: one limit for everyone. Per-machine limits and metering
	// come with the relay fleet, and need a coordinator to push them.
	res := relay.DefaultResources()
	res.Limit = &relay.RelayLimit{
		Duration: config.Duration("HEIMDALL_CIRCUIT_DURATION", defaultDuration),
		Data:     config.Bytes("HEIMDALL_CIRCUIT_DATA", defaultData),
	}
	if _, err := relay.New(h, relay.WithResources(res)); err != nil {
		return fmt.Errorf("start relay: %w", err)
	}

	// A machine whose carrier renumbers ports cannot learn its own
	// external address from any socket but the one it punches with. This
	// reports what that socket looks like from here, which costs one
	// round trip and reveals nothing the relay did not already know.
	wire.HandleObserved(h)

	addrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if err != nil {
		return err
	}
	// Printing what actually bound is the only way to tell whether 443
	// came up: failing to bind it is not an error, so its absence from
	// this list is the report.
	fmt.Println("heimdall relaying. put one of these in each agent's config.json:")
	for _, a := range addrs {
		fmt.Println("  ", a)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}
