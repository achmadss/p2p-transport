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
	"sync"
	"syscall"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/identity"
	"github.com/achmadss/p2p-transport/transport"
)

// The harness registers its own protocols, the way any application
// does: a name it picked, and no framing but its own. PLAN.md §3.3.
const (
	echoProto  = "/ratatoskr/echo/1.0.0"
	benchProto = "/ratatoskr/bench/1.0.0"
)

const version = "0.0.1"

// Defaults for the timings. Every one is overridable; see usage below.
//
//	lanTimeout    how long a command waits for mDNS. Answers arrive in
//	              milliseconds on a working network; this is giving up.
//	lanHeadStart  how long --via auto waits for the LAN before trying
//	              the relay. PLAN.md §5.
//	dialTimeout   the whole attempt. A relayed dial has a reservation
//	              and a hole punch to get through first.
//	benchTimeout  a whole measurement, which moves real bytes.
//	punchWindow   how long `bench` watches a relayed connection for an
//	              upgrade to a direct one before it stops watching. It
//	              bounds the reporting, not the punching: the transport
//	              goes on retrying for the life of the session either
//	              way. Longer than it looks it needs to be, because the
//	              address set a punch aims at is re-measured every 27
//	              seconds and a shorter window would report nothing
//	              where the next attempt had something.
var (
	lanTimeout   = config.Duration("RATATOSKR_LAN_TIMEOUT", 3*time.Second)
	lanHeadStart = config.Duration("RATATOSKR_LAN_HEAD_START", 400*time.Millisecond)
	dialTimeout  = config.Duration("RATATOSKR_DIAL_TIMEOUT", 30*time.Second)
	benchTimeout = config.Duration("RATATOSKR_BENCH_TIMEOUT", 10*time.Minute)
	punchWindow  = config.Duration("RATATOSKR_PUNCH_WINDOW", 90*time.Second)
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
                       measure throughput to a machine, default 100 MB.
                       A transfer moves onto a better path by itself
                       when one opens while it is running
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
  RATATOSKR_LAN_TIMEOUT     wait for the local network           (3s)
  RATATOSKR_LAN_HEAD_START  --via auto's LAN head start          (400ms)
  RATATOSKR_DIAL_TIMEOUT    whole connect attempt                (30s)
  RATATOSKR_BENCH_TIMEOUT   whole benchmark                      (10m)
  RATATOSKR_PUNCH_WINDOW    wait for a direct path               (90s)
  RATATOSKR_UPGRADE_EVERY   retry the punch on a relayed peer      (5s)
  RATATOSKR_UPGRADE_DIAL    how long one retry may take            (5s)
  RATATOSKR_BENCH_MB        default benchmark size               (100)
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
// is what --full is. SPEC.md §4.
func showID(full bool) error {
	id, err := identity.LoadOrCreate("")
	if err != nil {
		return err
	}
	if !full {
		fmt.Println(id.Fingerprint())
		return nil
	}
	dir, err := config.Dir("")
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

// start brings up this machine's transport with whatever relays its
// config names. The identity is the transport's business now: it loads
// or generates the key in the config directory and never hands it out.
func start() (*transport.Host, error) {
	r, err := relays()
	if err != nil {
		return nil, err
	}
	return transport.New(transport.Config{Relays: r})
}

// relays is where this machine keeps the heimdall nodes it may use:
// config.json, with RATATOSKR_RELAYS overriding it whole. A relay is a
// hundred characters of address that does not change between runs, so
// it is a file rather than something retyped.
//
// Read once. Three callers want the same answer — the host that dials
// them, `run` deciding whether to print an id worth reaching from
// another network, and `dialRelay` refusing a path that was never
// configured — and reading the file three times to answer one question
// invites the three to disagree.
//
// Nothing above the seam does this. `transport.New` is handed the list
// in code; where an application keeps it is the application's.
var relays = sync.OnceValues(func() ([]string, error) {
	if env := config.List("RATATOSKR_RELAYS"); len(env) > 0 {
		return env, nil
	}
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	return cfg.Relays, nil
})

// configured answers the two callers that only want to know whether
// there is a relay at all. Both run after start(), which has already
// refused a config.json that does not parse.
func configured() bool {
	r, _ := relays()
	return len(r) > 0
}

// lanPeers is what OnLAN has reported so far.
//
// The transport pushes; the harness remembers. Keeping the list here
// rather than below the seam is the point of OnLAN — matching a
// fingerprint a person typed is the application's question, and layer 4
// has no business holding a lookup table for it.
type lanPeers struct {
	mu   sync.Mutex
	seen map[transport.PeerID][]string
}

func watchLAN(h *transport.Host) *lanPeers {
	l := &lanPeers{seen: map[transport.PeerID][]string{}}
	h.OnLAN(func(id transport.PeerID, addrs []string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.seen[id] = addrs
	})
	return l
}

func (l *lanPeers) all() map[transport.PeerID][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[transport.PeerID][]string, len(l.seen))
	for k, v := range l.seen {
		out[k] = v
	}
	return out
}

// find waits for a machine whose id or fingerprint matches want. It
// polls rather than plumbing a channel through every caller; answers
// arrive in milliseconds and the caller is a person at a prompt.
func (l *lanPeers) find(ctx context.Context, want string) (transport.PeerID, []string, error) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		for id, addrs := range l.all() {
			if string(id) == want || id.Short() == want {
				return id, addrs, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", nil, fmt.Errorf("no machine matching %q answered on this network", want)
		case <-tick.C:
		}
	}
}

// run serves this machine. It answers the echo and benchmark protocols
// only, which is enough to prove a peer reached us and over which path;
// an application registers its own.
func run() error {
	h, err := start()
	if err != nil {
		return err
	}
	defer h.Close()

	h.Handle(benchProto, func(s transport.Stream) {
		defer s.Close()
		n, err := io.Copy(io.Discard, s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
			return
		}
		fmt.Fprintf(s, "%d\n", n)
	})

	h.Handle(echoProto, func(s transport.Stream) {
		defer s.Close()
		fmt.Printf("%s connected over %s\n", s.Peer().Short(), s.Path())
		if _, err := io.Copy(s, s); err != nil {
			fmt.Fprintln(os.Stderr, "stream:", err)
		}
	})

	fmt.Printf("serving as %s on this network\n", h.ID().Short())
	if configured() {
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

	lan := watchLAN(h)
	time.Sleep(lanTimeout)

	peers := lan.all()
	if len(peers) == 0 {
		fmt.Println("no machines found on this network")
		return nil
	}
	for id, addrs := range peers {
		fmt.Printf("%s  local network\n", id.Short())
		if full {
			fmt.Println("  ", id)
			for _, a := range addrs {
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

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	s, err := open(ctx, h, watchLAN(h), want, path, echoProto)
	if err != nil {
		return err
	}
	defer s.Close()

	fmt.Printf("connected to %s over %s\n", s.Peer().Short(), s.Path())

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
// first choice. PLAN.md §5.
func open(ctx context.Context, h *transport.Host, lan *lanPeers, want, path, proto string) (transport.Stream, error) {
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
		id, addrs, err := lan.find(head, want)
		cancel()

		switch {
		case err == nil:
			// Only the addresses this network answered with. Handing
			// over exactly those is what keeps a session found here
			// from leaving here — the relay is never in this dial set,
			// so a failure is a real failure rather than a quiet trip
			// through heimdall. PLAN.md §5.3.
			dialErr := h.Connect(ctx, id, addrs)
			if dialErr == nil {
				return h.Open(ctx, id, proto)
			}
			// Found but unreachable is a fallback trigger, not a dead
			// end: the machine may have moved networks mid-announcement.
			if path == "lan" {
				return nil, dialErr
			}
		case path == "lan":
			return nil, err
		}
	}

	return dialRelay(ctx, h, want, proto)
}

// dialRelay reaches a machine the local network did not answer for.
//
// It hands over no addresses at all, which is how a caller says "use
// whatever you know": the transport falls back to the relays it was
// configured with. A fingerprint cannot be used here — nothing on this
// network has offered the full id to match it against, and a relay is
// given an id rather than asked to search.
func dialRelay(ctx context.Context, h *transport.Host, want, proto string) (transport.Stream, error) {
	if !configured() {
		return nil, fmt.Errorf("no machine matching %q on this network, and no relay configured", want)
	}
	if len(want) < 20 {
		return nil, fmt.Errorf("no machine matching %q on this network, and a relay connection needs the full machine id rather than a fingerprint", want)
	}
	id := transport.PeerID(want)
	if err := h.Connect(ctx, id, nil); err != nil {
		return nil, err
	}
	return h.Open(ctx, id, proto)
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

// moveCheck is how often a running transfer looks up from sending to
// ask whether a better path has opened.
//
// The check itself is free — it reads the live connections — so this is
// only the granularity of the move, and it is small on purpose. Four
// megabytes is a fifth of a second on a LAN and twenty seconds on the
// worst relay measured, which is the right way round: the slower the
// path being escaped, the more there is left to move.
const moveCheck = 4 << 20

// bench measures a path and watches for a hole punch. These are the
// numbers TODO step 3 exists to produce: throughput, and whether a
// relayed connection becomes a direct one and how long that took.
func bench(want, path string, mb int64) error {
	h, err := start()
	if err != nil {
		return err
	}
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), benchTimeout)
	defer cancel()

	s, err := open(ctx, h, watchLAN(h), want, path, benchProto)
	if err != nil {
		return err
	}
	defer s.Close()

	peerID := s.Peer()
	first := s.Path()
	fmt.Printf("connected to %s over %s\n", peerID.Short(), first)

	total := mb << 20
	if err := transfer(ctx, h, s, peerID, mb, total); err != nil {
		return err
	}
	if first != transport.PathRelay {
		return nil
	}

	// The pass above began on the relay, so its rate is an average of
	// however many paths it used. The number that answers "is the relay
	// the normal data path" is a whole transfer on the new one, which is
	// also what an application does for the next request on a session that
	// has been up a while.
	//
	// There is only something to wait for if the transfer did not
	// already find the better path itself.
	upgraded := h.PathTo(peerID)
	if upgraded == transport.PathRelay {
		upgraded = watchUpgrade(h, peerID)
	}
	if upgraded == transport.PathRelay || upgraded == transport.PathUnknown {
		return nil
	}
	s2, err := h.Open(ctx, peerID, benchProto)
	if err != nil {
		return fmt.Errorf("second pass: %w", err)
	}
	defer s2.Close()
	fmt.Printf("second pass, all of it over %s\n", s2.Path())
	return transfer(ctx, h, s2, peerID, mb, total)
}

// transfer sends the bytes and reports the rate the far end confirms,
// moving onto a better path if one opens while it is still sending.
//
// Nothing migrates. A libp2p stream is bound to the connection it was
// opened on, and QUIC's own migration moves one connection between
// local addresses rather than between two connections to two different
// remote endpoints — the relay and the peer are two endpoints with two
// handshakes, so there is nothing there to migrate along. What moves is
// the transfer: every `moveCheck` bytes it asks what the best path to
// the peer is now, and when that beats the path it is on it finishes
// the stream it has, opens a new one — which the muxer hands the better
// connection — and sends the rest there.
//
// The check costs a look at the live connections, so a transfer that
// never has anywhere better to go pays nothing and stays on one stream
// from start to finish. The granularity is one check interval, and that
// is less a limitation than a preview: an application that reads in
// ranges is already one request per range, so it gets this for free.
func transfer(ctx context.Context, h *transport.Host, s transport.Stream, id transport.PeerID, mb, total int64) error {
	start := time.Now()
	path := s.Path()
	used := []string{string(path)}
	sentOnStream := int64(0)

	for sent := int64(0); sent < total; {
		n := min(int64(moveCheck), total-sent)
		if _, err := io.CopyN(s, zeros{}, n); err != nil {
			return fmt.Errorf("send: %w", err)
		}
		sent += n
		sentOnStream += n
		if sent == total {
			break
		}

		best := h.PathTo(id)
		if !best.BetterThan(path) {
			continue
		}
		if err := confirm(s, sentOnStream); err != nil {
			return err
		}
		next, err := h.Open(ctx, id, benchProto)
		if err != nil {
			// The path improved and the new stream would not open. The
			// old connection is finished, so there is nothing left to
			// fall back to and saying so beats a silent stall.
			return fmt.Errorf("moving from %s to %s after %s: %w", path, best, human(sent), err)
		}
		s, sentOnStream = next, 0
		path = s.Path()
		fmt.Printf("moved from %s to %s after %s\n", used[len(used)-1], path, human(sent))
		used = append(used, string(path))
	}
	if err := confirm(s, sentOnStream); err != nil {
		return err
	}
	elapsed := time.Since(start)

	// A run that moved is an average of the paths it used and not the
	// speed of any one of them, so it says which ones went into it
	// rather than naming the one it happened to end on.
	rate := fmt.Sprintf("%.1f MB/s", float64(total)/(1<<20)/elapsed.Seconds())
	if len(used) > 1 {
		rate += fmt.Sprintf(", averaged across %s", strings.Join(used, " then "))
	}
	fmt.Printf("%d MB over %s in %s = %s\n", mb, used[len(used)-1], elapsed.Round(time.Millisecond), rate)
	return nil
}

// confirm half-closes a stream and checks the far end counted every
// byte that went down it. The far end counts to end of stream, so this
// is also what ends one.
func confirm(s transport.Stream, want int64) error {
	defer s.Close()
	if err := s.CloseWrite(); err != nil {
		return fmt.Errorf("half close: %w", err)
	}
	reply, err := bufio.NewReader(s).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	got, err := strconv.ParseInt(strings.TrimSpace(reply), 10, 64)
	if err != nil {
		return fmt.Errorf("far end reported %q, not a byte count", strings.TrimSpace(reply))
	}
	if got != want {
		return fmt.Errorf("sent %d bytes, far end received %d", want, got)
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
func watchUpgrade(h *transport.Host, id transport.PeerID) transport.Path {
	start := time.Now()
	deadline := time.After(punchWindow)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			// Not a failed transfer: the bytes above went through. Only
			// the upgrade is outstanding, and it keeps being retried
			// for as long as the session lasts.
			fmt.Printf("still relayed after %s; the transfer worked, the direct path has not opened yet\n", punchWindow)
			return transport.PathRelay
		case <-tick.C:
			if p := h.PathTo(id); p != transport.PathRelay && p != transport.PathUnknown {
				fmt.Printf("upgraded to %s after %s\n", p, time.Since(start).Round(time.Millisecond))
				return p
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

// zeros is an endless reader. The bytes are incompressible enough for
// this: nothing on the path compresses, so their content does not matter.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { return len(p), nil }
