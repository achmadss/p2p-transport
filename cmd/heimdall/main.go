// Command heimdall is a libp2p relay node.
//
// It forwards a Noise session it holds no key for, so it cannot tell a
// directory listing from a photograph. What it does unavoidably learn is
// which peer ids talked, when, and how many bytes — true of any relay,
// and stated rather than implied. PLAN.md §4.4.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/achmadss/ratatoskr/internal/identity"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// port is fixed, unlike the agent's. A relay is dialled by an address
// written down somewhere, so it cannot move.
const port = 4001

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "heimdall:", err)
		os.Exit(1)
	}
}

func run() error {
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}

	h, err := libp2p.New(
		libp2p.Identity(id.PrivateKey()),
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port),
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
		),
	)
	if err != nil {
		return fmt.Errorf("start host: %w", err)
	}
	defer h.Close()

	// libp2p's default circuit allows 128 KB over two minutes, which is
	// sized for signalling rather than for files. A relayed transfer here
	// is a whole video, so the limit is raised to something a person
	// would actually hit.
	//
	// ponytail: one limit for everyone. Per-account limits and metering
	// are step 11, and need mimir to have accounts to meter.
	res := relay.DefaultResources()
	res.Limit = &relay.RelayLimit{Duration: time.Hour, Data: 64 << 30}
	if _, err := relay.New(h, relay.WithResources(res)); err != nil {
		return fmt.Errorf("start relay: %w", err)
	}

	addrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if err != nil {
		return err
	}
	fmt.Println("heimdall relaying. put one of these in each agent's config.json:")
	for _, a := range addrs {
		fmt.Println("  ", a)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}
