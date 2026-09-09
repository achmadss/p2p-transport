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

// meetPeer names this socket with a reflector, prints the address for
// the operator to carry to the other machine, and reads back the far
// side's.
//
// The socket that asks STUN is the socket that punches. An address
// measured on any other one belongs to that one, which is the mistake
// that cost this project an evening.
// The second return is the address this socket published, which is what
// the far side will aim at. Whoever punches needs it: an address that
// was true when it was published and false a second later fails the
// test in a way that reads exactly like a shut path.
func meetPeer(c *net.UDPConn) (*net.UDPAddr, string, error) {
	if room := os.Getenv("RATATOSKR_PUNCH_ROOM"); room != "" {
		return meetInRoom(c, room)
	}
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
		return nil, "", fmt.Errorf("no reflector answered, so this machine cannot name itself")
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
		return nil, "", err
	}
	peer, err := net.ResolveUDPAddr("udp4", strings.TrimSpace(line))
	if err != nil {
		return nil, "", fmt.Errorf("not an address: %w", err)
	}
	// Go resolves the empty string to :0 and reports no error, so an
	// operator who presses enter on a blank line gets a test that sends
	// every packet to nowhere and reports that nothing arrived — a
	// failure indistinguishable from the one being investigated.
	if peer.IP == nil || peer.IP.IsUnspecified() || peer.Port == 0 {
		return nil, "", fmt.Errorf("no address given: paste the other machine's punch address")
	}

	// Ask again now the waiting is over. A keepalive holds the mapping
	// open but cannot promise the carrier kept the same external port, and
	// an address that went stale while it was carried across the room
	// would fail the test for a reason that is not punching.
	if r := stun.Ask(c, keepalive.String()); r.Err == nil && r.Mapped != mapped {
		fmt.Printf("\n  WARNING: my address changed while waiting, %s -> %s\n", mapped, r.Mapped)
		fmt.Println("  the other machine is aiming at the old one; start over.")
	}
	return peer, mapped, nil
}

// meetInRoom pairs the two machines through a rendezvous rather than an
// operator carrying an address by hand.
//
// Doing it by hand is a variable, not just slow: two sides that started
// ninety seconds apart fail exactly like a path that was shut. The
// rendezvous names this socket the way a reflector does and returns the
// other machine's address as soon as it joins, so both start within one
// poll of each other.
func meetInRoom(c *net.UDPConn, room string) (*net.UDPAddr, string, error) {
	at := config.Str("RATATOSKR_PUNCH_RENDEZVOUS", "103.181.143.222:9600")
	server, err := net.ResolveUDPAddr("udp4", at)
	if err != nil {
		return nil, "", fmt.Errorf("rendezvous address %q: %w", at, err)
	}
	fmt.Printf("waiting in room %q at %s for the other machine.\n", room, server)

	// Who this machine is, for the life of the process. Without it the
	// rendezvous can only tell members apart by source address, and a
	// rerun arrives on a fresh port looking like a second machine: it
	// pairs at once, punches at its own dead mapping, and reports
	// nothing arrived — the failure being investigated, manufactured by
	// the tool investigating it.
	me := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	hello := []byte(room + " " + me)

	deadline := time.Now().Add(config.Duration("RATATOSKR_PUNCH_WAIT", 3*time.Minute))
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		if _, err := c.WriteToUDP(hello, server); err != nil {
			return nil, "", err
		}
		c.SetReadDeadline(time.Now().Add(time.Second))
		n, from, err := c.ReadFromUDP(buf)
		if err != nil || !from.IP.Equal(server.IP) {
			continue
		}
		parts := strings.Fields(string(buf[:n]))
		if len(parts) != 2 {
			continue
		}
		fmt.Printf("  MY PUNCH ADDRESS:  %s\n", parts[0])
		if parts[1] == "-" {
			// The read deadline paces this loop only while the server
			// stays quiet. It answers every poll, so without a wait the
			// loop runs at the round trip and floods the rendezvous for
			// however long the far side takes to arrive.
			time.Sleep(time.Second)
			continue
		}
		peer, err := net.ResolveUDPAddr("udp4", parts[1])
		if err != nil {
			return nil, "", fmt.Errorf("rendezvous gave %q: %w", parts[1], err)
		}
		c.SetReadDeadline(time.Time{})
		fmt.Printf("  PAIRED WITH:       %s\n", peer)
		// Same public address on both sides means one carrier, and a
		// punch that never crosses between two networks measures the
		// carrier hairpinning to itself. Worth a run, never worth
		// mistaking for the two-network result.
		if mine, _, err := net.SplitHostPort(parts[0]); err == nil && mine == peer.IP.String() {
			fmt.Println("  WARNING: both machines are behind the same public address.")
			fmt.Println("  this measures hairpinning, not a punch between two networks.")
		}
		fmt.Println()
		return peer, parts[0], nil
	}
	return nil, "", fmt.Errorf("no other machine joined room %q; start it there too", room)
}
