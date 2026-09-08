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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/discovery"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/achmadss/p2p-transport/internal/transport"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

const version = "0.0.1"

// Defaults for the timings. Every one is overridable; see usage below.
//
//	lanTimeout    how long a command waits for mDNS. Answers arrive in
//	              milliseconds on a working network; this is giving up.
//	lanHeadStart  how long --via auto waits for the LAN before trying
//	              the relay. PLAN.md §6.
//	dialTimeout   the whole attempt. A relayed dial has a reservation
//	              and a hole punch to get through first.
//	benchTimeout  a whole measurement, which moves real bytes.
//	punchWindow   how long a relayed connection is watched for an
//	              upgrade to a direct one before the punch is called a
//	              failure. Longer than it looks it needs to be: the
//	              address set a punch aims at is re-measured every 27
//	              seconds, so a window shorter than that reports a
//	              failure the retry would have fixed.
var (
	lanTimeout   = config.Duration("RATATOSKR_LAN_TIMEOUT", 3*time.Second)
	lanHeadStart = config.Duration("RATATOSKR_LAN_HEAD_START", 400*time.Millisecond)
	dialTimeout  = config.Duration("RATATOSKR_DIAL_TIMEOUT", 30*time.Second)
	benchTimeout = config.Duration("RATATOSKR_BENCH_TIMEOUT", 10*time.Minute)
	punchWindow  = config.Duration("RATATOSKR_PUNCH_WINDOW", 90*time.Second)

	// A relayed byte crosses the relay's host twice, in and out, and
	// TODO.md step 3 measured a network where no direct path is ever
	// reachable: against a symmetric carrier NAT the relay is not a
	// fallback, it is the only route. A transfer there can spend a
	// month of someone's egress without ever looking wrong. The cap
	// bounds one transfer; zero, the default, means no cap, because
	// refusing a transfer the user asked for is worse than the bill
	// until they have said which bill they mind.
	relayCap = config.Bytes("RATATOSKR_RELAY_CAP", 0)
)

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
	case "natcheck":
		err = natcheck()
	case "punch-quic":
		if len(args) == 0 {
			err = fmt.Errorf("punch-quic needs a role: listen on one machine, dial on the other")
			break
		}
		err = punchQUIC(args[0])
	case "run":
		err = run()
	case "discover":
		err = discover(len(args) > 0 && args[0] == "--full")
	case "bench":
		if len(args) == 0 {
			err = fmt.Errorf("bench needs a machine id")
			break
		}
		err = bench(args[0], via(args[1:]), size(args[1:]))
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
  bench ID [--via ...] [--mb N]
                       measure throughput to a machine, default 100 MB
  natcheck             classify this network's NAT, which decides
                       whether a direct connection is possible at all
  punch-quic listen|dial
                       punch a hole without libp2p, then handshake QUIC
                       over it, to tell a closed network apart from a
                       wrong configuration. Run listen on one machine,
                       dial on the other; scripts/punchpair.sh pairs
                       them so neither has to wait for the other.

environment (empty means the default):
  RATATOSKR_CONFIG_DIR      where identity.key and config.json live
  RATATOSKR_RELAYS          comma-separated relays, overriding config.json
  RATATOSKR_LAN_TIMEOUT     wait for mDNS                        (3s)
  RATATOSKR_LAN_HEAD_START  --via auto's LAN head start          (400ms)
  RATATOSKR_DIAL_TIMEOUT    whole connect attempt                (30s)
  RATATOSKR_BENCH_TIMEOUT   whole benchmark                      (10m)
  RATATOSKR_PUNCH_WINDOW    wait for a hole punch                (90s)
  RATATOSKR_UPGRADE_EVERY   retry the punch on a relayed peer      (5s)
  RATATOSKR_UPGRADE_DIAL    how long one retry may take            (5s)
  RATATOSKR_BENCH_MB        default benchmark size               (100)
  RATATOSKR_RELAY_CAP       bytes one relayed transfer may move,
                            K/M/G suffixes. 0 means no cap.        (0)
  RATATOSKR_ASSUME_PUBLIC   skip the relay reservation on a machine
                            that is genuinely reachable from outside
  RATATOSKR_STUN            natcheck reflectors, comma separated.
                            Needs two addresses, and two ports on
                            one of them to separate the last two
                            mapping kinds.        (three public STUN)
  RATATOSKR_DIAG            print AutoNAT's verdict, the relay
                            reservation and every advertised address
                            while run is serving                   (off)
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
	relays := cfg.Relays
	if env := config.List("RATATOSKR_RELAYS"); len(env) > 0 {
		relays = env
	}
	return transport.New(id.PrivateKey(), relays)
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

	diagCtx, stopDiag := context.WithCancel(context.Background())
	defer stopDiag()
	diagnose(diagCtx, h)

	h.Handle(transport.BenchProto, func(s network.Stream) {
		defer s.Close()
		n, err := io.Copy(guard(s.Conn(), io.Discard), s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
			return
		}
		fmt.Fprintf(s, "%d\n", n)
	})

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
	if len(cfg.Relays) > 0 || len(config.List("RATATOSKR_RELAYS")) > 0 {
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

	s, err := open(ctx, h, lan, want, path, transport.EchoProto)
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
func open(ctx context.Context, h *transport.Host, lan *discovery.LAN, want, path string, proto protocol.ID) (network.Stream, error) {
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
			s, dialErr := h.DialPeer(ctx, info, proto)
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

	return dialRelay(ctx, h, want, proto)
}

// dialRelay reaches a machine through heimdall. Discovering its address
// is mimir's job, which does not exist yet, so the circuit address is
// built from a configured relay plus a full peer id.
func dialRelay(ctx context.Context, h *transport.Host, want string, proto protocol.ID) (network.Stream, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if env := config.List("RATATOSKR_RELAYS"); len(env) > 0 {
		cfg.Relays = env
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
	return h.DialRelayed(ctx, peer.AddrInfo{ID: id, Addrs: addrs}, proto)
}

// size reads --mb. 100 MB is long enough to leave the slow start behind
// and short enough to run over a relay without regret.
func size(args []string) int64 {
	for i, a := range args {
		if a == "--mb" && i+1 < len(args) {
			if n, err := strconv.ParseInt(args[i+1], 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return int64(config.Int("RATATOSKR_BENCH_MB", 100))
}

// bench measures a path and watches for a hole punch. These are the
// numbers TODO step 3 exists to produce: throughput, and whether a
// relayed connection becomes a direct one and how long that took.
func bench(want, path string, mb int64) error {
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

	ctx, cancel := context.WithTimeout(context.Background(), benchTimeout)
	defer cancel()

	s, err := open(ctx, h, lan, want, path, transport.BenchProto)
	if err != nil {
		return err
	}
	defer s.Close()

	peerID := s.Conn().RemotePeer()
	first := transport.Describe(s.Conn()).Path
	fmt.Printf("connected to %s over %s\n", identity.Short(peerID.String()), first)

	total := mb << 20
	// Refuse before sending, not part way through. The far end has
	// already agreed to receive by here, so a mid-transfer failure
	// would have cost the relay every byte up to the limit.
	if first == transport.PathRelay && relayCap > 0 && total > relayCap {
		return fmt.Errorf("%d MB over a relayed link exceeds the %s cap; raise or clear RATATOSKR_RELAY_CAP",
			mb, human(relayCap))
	}

	start := time.Now()
	if _, err := io.CopyN(guard(s.Conn(), s), zeros{}, total); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := s.CloseWrite(); err != nil {
		return fmt.Errorf("half close: %w", err)
	}
	reply, err := bufio.NewReader(s).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	elapsed := time.Since(start)

	got, err := strconv.ParseInt(strings.TrimSpace(reply), 10, 64)
	if err != nil {
		return fmt.Errorf("far end reported %q, not a byte count", strings.TrimSpace(reply))
	}
	if got != total {
		return fmt.Errorf("sent %d bytes, far end received %d", total, got)
	}

	fmt.Printf("%d MB in %s = %.1f MB/s\n", mb, elapsed.Round(time.Millisecond),
		float64(total)/(1<<20)/elapsed.Seconds())

	if first == transport.PathRelay {
		watchUpgrade(h, peerID)
	}
	return nil
}

// watchUpgrade waits to see whether the relayed connection becomes a
// direct one, and how long it takes. A punch that never lands is as much
// a result as one that does.
//
// It watches rather than acts: the punching is the transport's, it runs
// on both ends for as long as the peer is relayed, and it would go on
// whether or not anybody was measuring it.
func watchUpgrade(h *transport.Host, id peer.ID) {
	start := time.Now()
	deadline := time.After(punchWindow)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			fmt.Printf("still relayed after %s: no direct path yet\n", punchWindow)
			return
		case <-tick.C:
			if p := h.PathTo(id); p == transport.PathDirect || p == transport.PathLAN {
				fmt.Printf("upgraded to %s after %s\n", p, time.Since(start).Round(time.Millisecond))
				return
			}
		}
	}
}

// human writes a byte count the way the cap was most likely typed.
func human(n int64) string {
	for _, u := range []struct {
		suffix string
		scale  int64
	}{{"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}} {
		if n >= u.scale {
			return fmt.Sprintf("%d %sB", n/u.scale, u.suffix)
		}
	}
	return fmt.Sprintf("%d bytes", n)
}

// guard applies the relay cap, and only when the connection is actually
// relayed: a direct transfer costs nobody anything and is never capped.
// Both ends guard, because both ends pay.
func guard(c network.Conn, w io.Writer) io.Writer {
	if relayCap > 0 && transport.Describe(c).Path == transport.PathRelay {
		return &capped{w: w, left: relayCap}
	}
	return w
}

// capped fails the transfer at the limit rather than truncating it. A
// copy that stops early and reports success is how a half-written file
// gets mistaken for a whole one.
type capped struct {
	w    io.Writer
	left int64
}

func (c *capped) Write(p []byte) (int, error) {
	c.left -= int64(len(p))
	if c.left < 0 {
		return 0, fmt.Errorf("relayed transfer hit the %s limit; raise or clear RATATOSKR_RELAY_CAP",
			human(relayCap))
	}
	return c.w.Write(p)
}

// zeros is an endless reader. The bytes are incompressible enough for
// this: nothing on the path compresses, so their content does not matter.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { return len(p), nil }
