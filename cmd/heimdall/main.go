// Command heimdall is a relay node.
//
// It forwards an encrypted session it holds no key for, so it cannot
// read a byte of what it carries. What it does unavoidably learn is
// which machines talked, when, and how many bytes — true of any relay,
// and stated here rather than implied.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/achmadss/p2p-transport/internal/shape"
	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"
)

// Defaults. Every one is overridable; see usage below.
const (
	defaultPort     = 4001
	defaultData     = 64 << 30
	defaultDuration = time.Hour

	// defaultReportEvery is how often this relay tells the coordinator
	// what each subject moved. Shorter follows demand more closely and
	// costs one small message per period; a second is a starting figure
	// and the right one is measured, not assumed.
	defaultReportEvery = time.Second

	// defaultBandwidth is what this relay tells the coordinator it can
	// forward, per direction. It is a starting figure and not a
	// measurement: check what the provider actually sells, remember a
	// relayed byte crosses the machine twice, and set
	// HEIMDALL_BANDWIDTH to what is left.
	defaultBandwidth = 50 << 20

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
  RATATOSKR_CONFIG_DIR       where identity.key lives. Only read when
                             HEIMDALL_BIFROST is unset: a relay in a
                             fleet keeps no key and needs no disk.
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
  HEIMDALL_BIFROST           the coordinator's multiaddr. Set it and
                             this relay joins a fleet: it registers,
                             and only the machines the coordinator
                             places here may use it. Unset, the relay
                             is open to anyone who knows its address.
  HEIMDALL_BANDWIDTH         bytes per second, PER DIRECTION, this
                             relay can forward, K/M/G suffixes  (50M)
                             It is both what the coordinator places
                             against and the ceiling this relay
                             enforces on everything it forwards.
                             A relayed byte crosses the machine twice,
                             so this is not the provider's headline
                             number unless that number is per
                             direction. Only used with a coordinator.
  HEIMDALL_REPORT_EVERY      how often this relay reports what each
                             subject moved                       (1s)
                             The coordinator divides a subject's rate
                             between the relays carrying it on this
                             clock. Only used with a coordinator.
`)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "heimdall:", err)
		os.Exit(1)
	}
}

func run() error {
	// With a coordinator, the relay is closed: it carries the machines
	// bifrost places here and refuses everyone else. Without one, it is
	// open to anyone holding its address, which is how a single relay
	// run by hand has always worked.
	coord, err := config.Multiaddrs("HEIMDALL_BIFROST")
	if err != nil {
		return err
	}

	// A relay in a fleet keeps no key. It tells the coordinator its
	// address on every reconnection, and the machines using it are given
	// that address by the coordinator, so nobody anywhere remembers the
	// old peer id and a fresh one each boot costs nothing. That leaves
	// the machine with no state to keep and, more to the point, no key
	// to strip out of a disk image before cloning it.
	//
	// A relay run by hand is named in someone's config.json, so its id
	// has to survive a restart and it keeps the file.
	id, err := identity.LoadOrCreate("")
	if len(coord) > 0 {
		id, err = identity.Ephemeral()
	}
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
	announce, err := config.Multiaddrs("HEIMDALL_ANNOUNCE")
	if err != nil {
		return err
	}
	if len(announce) > 0 {
		opts = append(opts, libp2p.AddrsFactory(func([]multiaddr.Multiaddr) []multiaddr.Multiaddr {
			return announce
		}))
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("start host: %w", err)
	}
	defer h.Close()

	// relayHost is what the relay is built on: the host itself when
	// there is no coordinator, and a shaped view of it when there is.
	relayHost := host.Host(h)
	var relayOpts []relay.Option
	if len(coord) > 0 {
		info, err := peer.AddrInfoFromP2pAddr(coord[0])
		if err != nil {
			return fmt.Errorf("HEIMDALL_BIFROST names no peer: %w", err)
		}
		bandwidth := config.Bytes("HEIMDALL_BANDWIDTH", defaultBandwidth)
		limits := shape.New(bandwidth)
		relayHost = shapedHost{Host: h, limits: limits}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		acl := newAdmitted(limits)
		relayOpts = append(relayOpts, relay.WithACL(acl))
		go acl.follow(ctx, h, *info, bandwidth, config.Duration("HEIMDALL_REPORT_EVERY", defaultReportEvery))
	}

	// The default circuit allows 128 KB over two minutes, sized for
	// signalling rather than for files. A relayed transfer here is a
	// whole video, so the limit is raised to one a person might reach.
	//
	// It is a byte cap and a lifetime, not a rate, and it is the same
	// for everyone. With a coordinator the rates come from the shaper
	// above and this stays as the backstop it is.
	res := relay.DefaultResources()
	res.Limit = &relay.RelayLimit{
		Duration: config.Duration("HEIMDALL_CIRCUIT_DURATION", defaultDuration),
		Data:     config.Bytes("HEIMDALL_CIRCUIT_DATA", defaultData),
	}
	if _, err := relay.New(relayHost, append(relayOpts, relay.WithResources(res))...); err != nil {
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
