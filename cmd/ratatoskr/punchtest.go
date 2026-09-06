package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/achmadss/ratatoskr/internal/stun"
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

	local := c.LocalAddr().(*net.UDPAddr)
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
		return fmt.Errorf("no reflector answered, so this machine cannot name itself")
	}

	fmt.Printf("local port %d\n\n", local.Port)
	fmt.Printf("  MY PUNCH ADDRESS:  %s\n\n", mapped)
	fmt.Print("paste the other machine's punch address, then press enter: ")

	// Carrying an address to the other machine by hand takes minutes,
	// and a NAT forgets an idle UDP mapping in about one. Without this
	// the address printed above expires while the operator is still
	// typing it, and the test would measure nothing but that.
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
		return err
	}
	peer, err := net.ResolveUDPAddr("udp4", strings.TrimSpace(line))
	if err != nil {
		return fmt.Errorf("not an address: %w", err)
	}

	fmt.Printf("\nsending to %s every 300ms for 30s, and printing anything that arrives.\n\n", peer)

	stop := time.Now().Add(30 * time.Second)
	go func() {
		for time.Now().Before(stop) {
			if _, err := c.WriteToUDP([]byte("ratatoskr punch"), peer); err != nil {
				fmt.Printf("  send failed: %v\n", err)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}()

	got := 0
	buf := make([]byte, 1500)
	for time.Now().Before(stop) {
		c.SetReadDeadline(stop)
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if !from.IP.Equal(peer.IP) {
			continue // a reflector answering the keepalive, not a punch
		}
		got++
		if got <= 3 {
			fmt.Printf("  RECEIVED %d bytes from %s\n", n, from)
		}
	}

	fmt.Println()
	if got == 0 {
		fmt.Println("nothing arrived. Either the far side never sent, or a NAT on the")
		fmt.Println("path drops punched packets. Compare with the other machine's count.")
	} else {
		fmt.Printf("%d packets arrived. The path punches, so the fault is in how we\n", got)
		fmt.Println("configure libp2p, not in the carriers.")
	}
	return nil
}
