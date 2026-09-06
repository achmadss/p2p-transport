package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/stun"
)

// punchtest takes libp2p out of the question.
//
// Every failure so far has been reported by DCUtR, which leaves two
// explanations standing: the carriers do not pass a punched packet, or
// we hand DCUtR something it cannot use. One UDP socket, one reflector
// to name it, and the operator carrying each address to the other
// machine settles which. If bytes arrive here, the network punches and
// the fault is ours. If nothing arrives, no library would have done
// better.
//
// The socket that asks STUN is the socket that sends and listens, which
// is the whole point: an address measured on one socket says nothing
// about another.
func punchtest() error {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return err
	}
	defer c.Close()

	peer, err := meetPeer(c)
	if err != nil {
		return err
	}

	// Two people typing addresses to each other cannot start within
	// thirty seconds of one another reliably, and a test that fails on
	// that looks exactly like a test that failed on the network. The
	// side that can wait longer should.
	stop := time.Now().Add(config.Duration("RATATOSKR_PUNCH_SECONDS", 30*time.Second))

	fmt.Printf("\nsending to %s every 300ms until %s, and printing anything that arrives.\n\n", peer, stop.Format("15:04:05"))

	// Two sizes, because that is the last difference left between this
	// test and libp2p. A QUIC handshake packet is padded to 1200 bytes
	// and this one was fifteen, so if the small packets land and the
	// large ones do not, the carrier has a size limit and no amount of
	// hole punching is the problem.
	small := append([]byte{'S'}, make([]byte, 14)...)
	large := append([]byte{'L'}, make([]byte, 1279)...)
	sizes := [][]byte{small, large}
	if n := config.Int("RATATOSKR_PUNCH_BYTES", 0); n > 0 {
		sizes = [][]byte{append([]byte{'S'}, make([]byte, n-1)...)}
		if n > 640 {
			sizes[0][0] = 'L'
		}
	}

	// A witness on a public host, written to on this very socket while
	// the punch is running. Every earlier explanation for a failed punch
	// — the mapping expired, the carrier renumbered the port, the socket
	// went quiet — predicts that this socket stops appearing at the
	// witness, or appears under a different port. If instead the witness
	// logs the published port throughout, then at the moment the punch
	// failed both sockets were alive and correctly mapped, and the
	// packets were simply not crossing between the two networks. That is
	// proof rather than inference, and it costs one packet a second.
	if w := os.Getenv("RATATOSKR_PUNCH_WITNESS"); w != "" {
		if addr, err := net.ResolveUDPAddr("udp4", w); err == nil {
			fmt.Printf("witnessing to %s once a second.\n", addr)
			go func() {
				for time.Now().Before(stop) {
					c.WriteToUDP([]byte("witness"), addr)
					time.Sleep(time.Second)
				}
			}()
		}
	}

	go func() {
		for time.Now().Before(stop) {
			for _, p := range sizes {
				if _, err := c.WriteToUDP(p, peer); err != nil {
					fmt.Printf("  send failed: %v\n", err)
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
	}()

	var gotSmall, gotLarge int
	buf := make([]byte, 2000)
	for time.Now().Before(stop) {
		c.SetReadDeadline(stop)
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if !from.IP.Equal(peer.IP) || n == 0 {
			continue // a reflector answering the keepalive, not a punch
		}
		if buf[0] == 'L' {
			gotLarge++
		} else {
			gotSmall++
		}
		if gotSmall+gotLarge <= 2 {
			fmt.Printf("  RECEIVED %d bytes from %s\n", n, from)
		}
	}

	fmt.Printf("\n  small (15 bytes):   %d arrived\n", gotSmall)
	fmt.Printf("  large (1280 bytes): %d arrived\n\n", gotLarge)
	switch {
	case gotSmall == 0 && gotLarge == 0:
		fmt.Println("nothing arrived. Either the far side never sent, or a NAT on the")
		fmt.Println("path drops punched packets outright.")
	case gotLarge == 0:
		fmt.Println("only the small packets survived. The path punches, but something")
		fmt.Println("on it drops a packet the size of a QUIC handshake — which is every")
		fmt.Println("packet libp2p punches with.")
	default:
		fmt.Println("both sizes arrived. The path punches at libp2p's own packet size,")
		fmt.Println("so the fault is in how we drive libp2p, not in the carriers.")
	}
	return nil
}

// meetPeer names this socket with a reflector, prints the address for
// the operator to carry to the other machine, and reads back the far
// side's.
//
// The socket that asks STUN is the socket that punches. An address
// measured on any other one belongs to that one, which is the mistake
// that cost this project an evening.
func meetPeer(c *net.UDPConn) (*net.UDPAddr, error) {
	var mapped string
	var keepalive *net.UDPAddr
	for _, s := range stun.Servers() {
		if r := stun.Ask(c, s); r.Err == nil {
			mapped = r.Mapped
			keepalive, _ = net.ResolveUDPAddr("udp4", s)
			break
		}
	}
	if mapped == "" || keepalive == nil {
		return nil, fmt.Errorf("no reflector answered, so this machine cannot name itself")
	}

	fmt.Printf("local port %d\n\n", c.LocalAddr().(*net.UDPAddr).Port)
	fmt.Printf("  MY PUNCH ADDRESS:  %s\n\n", mapped)
	fmt.Print("paste the other machine's punch address, then press enter: ")

	// Carrying an address by hand takes minutes and a NAT forgets an idle
	// UDP mapping in about one. Without this the address printed above
	// expires while the operator is still typing it, and the test would
	// measure nothing but that.
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(15 * time.Second):
				c.WriteToUDP([]byte{0}, keepalive)
			}
		}
	}()

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return nil, err
	}
	peer, err := net.ResolveUDPAddr("udp4", strings.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("not an address: %w", err)
	}

	// Ask again now the waiting is over. A keepalive holds the mapping
	// open but cannot promise the carrier kept the same external port, and
	// an address that went stale while it was carried across the room
	// would fail the test for a reason that is not punching.
	if r := stun.Ask(c, keepalive.String()); r.Err == nil && r.Mapped != mapped {
		fmt.Printf("\n  WARNING: my address changed while waiting, %s -> %s\n", mapped, r.Mapped)
		fmt.Println("  the other machine is aiming at the old one; start over.")
	}
	return peer, nil
}
