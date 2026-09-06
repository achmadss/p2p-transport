// Command ratatoskr is both the agent and the client. One keypair, one
// peer id, both roles.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/achmadss/ratatoskr/internal/config"
	"github.com/achmadss/ratatoskr/internal/discovery"
	"github.com/achmadss/ratatoskr/internal/identity"
	"github.com/achmadss/ratatoskr/internal/transport"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const version = "0.0.1"

// lanTimeout is how long a command waits for mDNS. Answers arrive in
// milliseconds on a working network; this is the give-up point.
const lanTimeout = 3 * time.Second

// lanHeadStart is how long `--via auto` waits for the local network
// before trying the relay. PLAN.md §6.
const lanHeadStart = 400 * time.Millisecond

// dialTimeout covers the whole attempt. A relayed dial has a reservation
// and a hole punch to get through first, so it needs far longer than the
// local network does.
const dialTimeout = 30 * time.Second

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("ratatoskr", version)
	case "id":
		err = showID(len(args) > 0 && args[0] == "--full")
	case "run":
		err = run()
	case "discover":
		err = discover(len(args) > 0 && args[0] == "--full")
	case "connect":
		if len(args) == 0 {
			err = fmt.Errorf("connect needs a machine id or fingerprint")
			break
		}
		err = connect(args[0], via(args[1:]))
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ratatoskr:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ratatoskr <command>

  version              print the version
  id [--full]          print this machine's identity
  run                  serve this machine on the local network
  discover [--full]    list Ratatoskr machines on this network
  connect ID [--via lan|relay|auto]
                       connect to a machine by id or fingerprint
`)
}

// showID prints the short fingerprint by default. The full peer id is
// long and nobody reads it correctly; it belongs in diagnostics, which
// is what --full is. SPEC.md §30.4.
func showID(full bool) error {
	id, err := identity.LoadOrCreate()
	if err != nil {
		return err
	}
	if !full {
		fmt.Println(id.Fingerprint())
		return nil
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	fmt.Println("peer id:    ", id.ID())
	fmt.Println("fingerprint:", id.Fingerprint())
	fmt.Println("config dir: ", dir)
	return nil
}

// via reads --via. auto is LAN first, then the relay.
func via(args []string) string {
	for i, a := range args {
		if a == "--via" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "auto"
}

// start brings up this machine's host under its stored identity and any
// relays its config names.
func start() (*transport.Host, error) {
	id, err := identity.LoadOrCreate()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return transport.New(id.PrivateKey(), cfg.Relays)
}

// run serves this machine. Until the File API lands it answers the echo
// protocol only, which is enough to prove a peer reached us and over
// which path.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	h, err := start()
	if err != nil {
		return err
	}
	defer h.Close()

	h.Handle(transport.EchoProto, func(s network.Stream) {
		defer s.Close()
		c := transport.Describe(s.Conn())
		fmt.Printf("%s connected over %s\n", identity.Short(c.Peer.String()), c.Path)
		if _, err := io.Copy(s, s); err != nil {
			fmt.Fprintln(os.Stderr, "stream:", err)
		}
	})

	lan, err := discovery.Start(h.Host())
	if err != nil {
		return err
	}
	defer lan.Close()

	fmt.Printf("serving as %s on this network\n", identity.Short(h.ID().String()))
	if len(cfg.Relays) > 0 {
		fmt.Printf("from another network, connect to:\n   %s\n", h.ID())
	}
	fmt.Println("waiting. ctrl-c to stop.")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("\nstopping")
	return nil
}

// discover lists what is on this network. It needs no server and no
// Internet: everything here is mDNS on the local link.
func discover(full bool) error {
	h, err := start()
	if err != nil {
		return err
	}
	defer h.Close()

	lan, err := discovery.Start(h.Host())
	if err != nil {
		return err
	}
	defer lan.Close()

	time.Sleep(lanTimeout)

	peers := lan.Peers()
	if len(peers) == 0 {
		fmt.Println("no machines found on this network")
		return nil
	}
	for _, p := range peers {
		fmt.Printf("%s  local network\n", identity.Short(p.ID.String()))
		if full {
			fmt.Println("  ", p.ID)
			for _, a := range p.Addrs {
				fmt.Println("   ", a)
			}
		}
	}
	return nil
}

// connect finds a machine on the local network and opens a stream to it.
// The dial carries no relay address, so a failure here is a real failure
// rather than a quiet trip through heimdall.
func connect(want, path string) error {
	h, err := start()
	if err != nil {
		return err
	}
	defer h.Close()

	lan, err := discovery.Start(h.Host())
	if err != nil {
		return err
	}
	defer lan.Close()

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	s, err := open(ctx, h, lan, want, path)
	if err != nil {
		return err
	}
	defer s.Close()

	c := transport.Describe(s.Conn())
	fmt.Printf("connected to %s over %s\n", identity.Short(c.Peer.String()), c.Path)

	const msg = "ratatoskr says hello\n"
	if _, err := io.WriteString(s, msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := s.CloseWrite(); err != nil {
		return fmt.Errorf("half close: %w", err)
	}
	back, err := bufio.NewReader(s).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if back != msg {
		return fmt.Errorf("echo mismatch: sent %q, got %q", msg, back)
	}
	fmt.Println("link is good")
	return nil
}

// open picks a path to the far end. LAN gets a head start because a
// machine on this network should be reached on this network: nothing
// leaves it, and it is faster. The relay is the fallback, never the
// first choice. PLAN.md §6.
func open(ctx context.Context, h *transport.Host, lan *discovery.LAN, want, path string) (network.Stream, error) {
	switch path {
	case "lan", "relay", "auto":
	default:
		return nil, fmt.Errorf("--via must be lan, relay or auto, not %q", path)
	}

	if path != "relay" {
		wait := lanTimeout
		if path == "auto" {
			wait = lanHeadStart
		}
		head, cancel := context.WithTimeout(ctx, wait)
		info, err := lan.Find(head, want)
		cancel()

		switch {
		case err == nil:
			s, dialErr := h.DialPeer(ctx, info, transport.EchoProto)
			if dialErr == nil {
				return s, nil
			}
			// Found but unreachable is a fallback trigger, not a dead
			// end: the peer may have moved networks mid-announcement.
			if path == "lan" {
				return nil, dialErr
			}
		case path == "lan":
			return nil, err
		}
	}

	return dialRelay(ctx, h, want)
}

// dialRelay reaches a machine through heimdall. Discovering its address
// is mimir's job, which does not exist yet, so the circuit address is
// built from a configured relay plus a full peer id.
func dialRelay(ctx context.Context, h *transport.Host, want string) (network.Stream, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if len(cfg.Relays) == 0 {
		return nil, fmt.Errorf("no machine matching %q on this network, and no relay configured", want)
	}
	id, err := peer.Decode(want)
	if err != nil {
		return nil, fmt.Errorf("no machine matching %q on this network, and a relay connection needs the full machine id rather than a fingerprint", want)
	}

	var addrs []multiaddr.Multiaddr
	for _, r := range cfg.Relays {
		a, err := multiaddr.NewMultiaddr(r + "/p2p-circuit")
		if err != nil {
			return nil, fmt.Errorf("bad relay %q: %w", r, err)
		}
		addrs = append(addrs, a)
	}
	return h.DialRelayed(ctx, peer.AddrInfo{ID: id, Addrs: addrs}, transport.EchoProto)
}
