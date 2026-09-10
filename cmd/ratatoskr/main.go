// Command ratatoskr exercises and diagnoses the transport. One keypair,
// one machine id, serving and connecting from the same binary.
//
// It is a test harness, not a product: it is also the first consumer of
// the transport package, so every command below goes through the
// exported surface and nothing else.
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

// Protocols the harness registers, the way any application does: names
// it picked, with no framing but its own.
const (
	echoProto  = "/ratatoskr/echo/1.0.0"
	benchProto = "/ratatoskr/bench/1.0.0"
)

const version = "0.0.1"

// Defaults for the timings. Every one is overridable; see usage below.
//
//	lanTimeout    how long to wait for the local network. Answers come
//	              in milliseconds; this is giving up.
//	lanHeadStart  how long --via auto waits for the local network
//	              before trying the relay.
//	dialTimeout   the whole connect attempt.
//	benchTimeout  a whole measurement, which moves real bytes.
//	punchWindow   how long bench watches a relayed connection for a
//	              direct one to open. It bounds the reporting only; the
//	              transport keeps retrying either way. Longer than it
//	              looks it needs to be, because the addresses aimed at
//	              are re-measured every 27 seconds and a shorter window
//	              reports nothing where the next attempt had something.
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
  RATATOSKR_COORDINATOR     comma-separated addresses of the coordinator
                            that hands relays out, overriding
                            config.json. Set relays or a coordinator,
                            not both.
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

// showID prints the short fingerprint by default. The full id is long
// and nobody reads it correctly, so it is behind --full.
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

// via reads --via. auto means the local network first, then the relay.
func via(args []string) string {
	for i, a := range args {
		if a == "--via" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "auto"
}

// start brings up this machine's transport with whatever relays its
// config names. The identity is the transport's: it loads or generates
// the key in the config directory and never hands it out.
func start() (*transport.Host, error) {
	r, err := relays()
	if err != nil {
		return nil, err
	}
	c, err := coordinator()
	if err != nil {
		return nil, err
	}
	return transport.New(transport.Config{Relays: r, Coordinator: c})
}

// relays is where this machine keeps the relays it may use: config.json,
// with RATATOSKR_RELAYS overriding it whole. A relay address is a
// hundred characters that do not change between runs, so it is a file
// rather than something retyped.
//
// Read once, because three callers want the same answer and reading the
// file three times invites them to disagree. transport.New is handed the
// list in code; where an application keeps its own is its business.
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

// coordinator is where this machine asks which relay to use, read the
// same way and from the same file as the relays above.
var coordinator = sync.OnceValues(func() ([]string, error) {
	if env := config.List("RATATOSKR_COORDINATOR"); len(env) > 0 {
		return env, nil
	}
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	return cfg.Coordinator, nil
})

// configured answers the two callers that only want to know whether
// this machine can be reached from another network at all. A
// coordinator counts: it has not handed out a relay yet, but it is
// going to. Both run after start(), which has already refused a
// config.json that does not parse.
func configured() bool {
	r, _ := relays()
	c, _ := coordinator()
	return len(r) > 0 || len(c) > 0
}

// lanPeers is what OnLAN has reported so far. The transport pushes and
// the harness remembers: matching a fingerprint a person typed is the
// application's question, not the transport's.
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
// polls rather than plumbing a channel through every caller: answers
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

// run serves this machine until interrupted. It answers the echo and
// benchmark protocols only, which is enough to show that a machine
// reached this one and over which path.
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

// discover lists the machines on this network. No server and no
// Internet is involved.
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

// connect reaches a machine and echoes a line off it, reporting which
// path the bytes took.
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

// open reaches the far end and returns a stream. The local network gets
// a head start because a machine here should be reached here: it is
// faster and nothing leaves the network. The relay is the fallback,
// never the first choice.
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
			// Only the addresses this network answered with, so the
			// relay is never in the dial set and a failure here is a
			// real failure rather than a quiet trip through a relay.
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
// Passing no addresses is how a caller says "use whatever you know", so
// the transport falls back to its configured relays. A fingerprint will
// not do here: nothing has offered the full id to match it against, and
// a relay is given an id rather than asked to search.
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

// writeChunk is how much goes down the stream between two looks at the
// path. Watch does the waiting, so this is only how long a write can
// keep the transfer from noticing an answer that has already arrived:
// five milliseconds on a local network, a seventh of a second on the
// slowest relay measured.
const writeChunk = 512 << 10

// bench measures throughput to a machine, and when the transfer starts
// out relayed, how long a direct path takes to open and what it then
// measures on its own.
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

	// One watch for the whole run: the transfer moves on what it
	// delivers, and the wait afterwards is the same channel with nothing
	// left to send down it.
	paths, stop := h.Watch(peerID)
	defer stop()

	total := mb << 20
	if err := transfer(ctx, h, s, peerID, paths, mb, total); err != nil {
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
		upgraded = watchUpgrade(paths)
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
	return transfer(ctx, h, s2, peerID, paths, mb, total)
}

// transfer sends the bytes and reports the rate the far end confirms,
// moving onto a better path if one opens while it is still sending.
//
// Nothing migrates: a stream is bound to the connection it was opened
// on. What moves is the transfer. Every writeChunk bytes it reads
// whatever Watch has delivered, and when that beats the path it is on it
// finishes the current stream, opens a new one on the better connection,
// and sends the rest there. This is the recipe any bulk caller follows,
// and an application that reads in ranges gets it for free.
func transfer(ctx context.Context, h *transport.Host, s transport.Stream, id transport.PeerID, paths <-chan transport.Path, mb, total int64) error {
	start := time.Now()
	path := s.Path()
	best := path
	used := []string{string(path)}
	sentOnStream := int64(0)

	// Nothing on the path compresses, so the content does not matter and
	// one buffer serves the whole run.
	buf := make([]byte, writeChunk)

	for sent := int64(0); sent < total; {
		n := min(int64(len(buf)), total-sent)
		if _, err := s.Write(buf[:n]); err != nil {
			return fmt.Errorf("send: %w", err)
		}
		sent += n
		sentOnStream += n
		if sent == total {
			break
		}

		// Taken rather than waited for: the transfer has bytes to send
		// either way, and the channel holds the latest path, so an empty
		// one means nothing has changed since the last look.
		select {
		case p, ok := <-paths:
			if ok {
				best = p
			}
		default:
		}
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
		// The move is spent, whether or not it reached the path that
		// prompted it. Without this a stream that opens on a worse path
		// than the one just reported moves again every chunk, and a real
		// improvement arrives as its own event anyway.
		best = path
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

// confirm half-closes a stream and checks the far end counted every byte
// that went down it. The far end counts to end of stream, so this is
// also what ends one.
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

// watchUpgrade waits for a relayed connection to be replaced by a
// direct one, and reports how long that took. It only watches: the
// transport keeps trying for as long as the machine stays relayed,
// measured or not, and a path that never opens is as much a result as
// one that does.
func watchUpgrade(paths <-chan transport.Path) transport.Path {
	start := time.Now()
	deadline := time.After(punchWindow)
	for {
		select {
		case <-deadline:
			// Not a failed transfer: the bytes above went through. Only
			// the upgrade is outstanding, and it keeps being retried
			// for as long as the session lasts.
			fmt.Printf("still relayed after %s; the transfer worked, the direct path has not opened yet\n", punchWindow)
			return transport.PathRelay
		case p, ok := <-paths:
			if !ok {
				return transport.PathUnknown // the host closed
			}
			if p != transport.PathRelay && p != transport.PathUnknown {
				fmt.Printf("upgraded to %s after %s\n", p, time.Since(start).Round(time.Millisecond))
				return p
			}
		}
	}
}

// human writes a byte count the way it was most likely typed.
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
