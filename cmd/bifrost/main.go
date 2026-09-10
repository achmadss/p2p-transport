// Command bifrost is the relay coordinator.
//
// Relays register with it, machines ask it which relay to use, and the
// application tells it what each subject is allowed. It never carries a
// byte of anyone's traffic: it hands out placements and gets out of the
// way.
//
// It knows nothing about accounts. A subject is an opaque id with a
// floor rate, a ceiling rate and a list of machine ids, and who that is
// belongs to whatever put it there.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	defaultPort  = 4002
	defaultAdmin = "127.0.0.1:4080"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Print(`usage: bifrost

environment (empty means the default):
  RATATOSKR_CONFIG_DIR  where identity.key and subjects.json live
  BIFROST_PORT          listen port for relays and machines, UDP and
                        TCP                                    (4002)
  BIFROST_ANNOUNCE      comma-separated public multiaddrs to advertise
                        instead of what the machine sees. Needed behind
                        a cloud NAT.
  BIFROST_ADMIN         address for the application's admin API
                                                    (127.0.0.1:4080)
  BIFROST_SUBJECT_MIN   floor rate a subject gets when the admin call
                        omits one, K/M/G suffixes             (2M)
  BIFROST_SUBJECT_MAX   ceiling rate, same                    (50M)
  BIFROST_HEADROOM      fraction of a relay's bandwidth that is really
                        forwardable                          (0.70)
  BIFROST_PACK_TO       how full a relay is packed; the rest is what a
                        quiet subject bursts into            (0.85)
  BIFROST_LEASE_TTL     how long a placement lasts before the machine
                        must ask again                         (2m)

admin API, plain HTTP and JSON, rates in bytes per second:
  PUT    /v1/subjects/{id}          {"min": 2000000, "max": 50000000}
  PUT    /v1/subjects/{id}/devices  {"peer_ids": ["12D3Koo..."]}
  DELETE /v1/subjects/{id}

It has no authentication. Keep it on loopback, or behind whatever
already guards the application's own admin traffic.
`)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bifrost:", err)
		os.Exit(1)
	}
}

func run() error {
	id, err := identity.LoadOrCreate("")
	if err != nil {
		return err
	}
	st, err := openStore("")
	if err != nil {
		return err
	}

	port := config.Int("BIFROST_PORT", defaultPort)
	opts := []libp2p.Option{
		libp2p.Identity(id.PrivateKey()),
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port),
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
		),
	}
	// A coordinator behind a cloud provider's NAT sees only its private
	// address, and its address is the one fixed thing every relay and
	// every machine is configured with.
	announce, err := config.Multiaddrs("BIFROST_ANNOUNCE")
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

	done := make(chan struct{})
	defer close(done)
	f := newFleet(st, knobsFromEnv())
	f.serve(h, done)

	// The admin API is the only part an operator can get wrong in a way
	// that matters, so its address is printed whether it is loopback or
	// not.
	srv := &http.Server{Addr: config.Str("BIFROST_ADMIN", defaultAdmin), Handler: admin(st,
		config.Bytes("BIFROST_SUBJECT_MIN", defaultSubjectMin),
		config.Bytes("BIFROST_SUBJECT_MAX", defaultSubjectMax),
		f.setLimit)}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "admin API stopped:", err)
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	addrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if err != nil {
		return err
	}
	fmt.Println("bifrost coordinating. give one of these to every relay and machine:")
	for _, a := range addrs {
		fmt.Println("  ", a)
	}
	fmt.Println("admin API on", srv.Addr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	return nil
}
