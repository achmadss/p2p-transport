package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/stun"
)

// natcheck answers the only question that decides whether two machines
// can ever talk directly: what does each NAT do with the source port of
// one socket when that socket writes to different places?
//
// RFC 4787 names the three answers. Endpoint-independent mapping reuses
// one external port for every destination, which is what hole punching
// needs, because the port a third party observed is the port the far
// peer may use. Address-dependent mapping picks a new port per
// destination address, address-and-port-dependent per address and port;
// either way the observed port is worthless to anyone else, and no
// amount of signalling recovers it.
//
// The test needs one socket, several public reflectors, and at least
// two distinct addresses among them — plus two ports on one address, or
// the last two answers cannot be told apart.

func natcheck() error {
	got, c := stun.Reflect()
	if c == nil {
		return fmt.Errorf("cannot open a UDP socket")
	}
	defer c.Close()
	local := c.LocalAddr().(*net.UDPAddr).Port
	fmt.Printf("one socket, local port %d\n\n", local)

	// One observer that is not a reflector, on the same socket.
	//
	// Three public reflectors agreeing is what this command used to
	// call endpoint-independent mapping, and it was wrong on a carrier
	// that gave the very next port to a fourth destination: the reply
	// said hole punching can work, the punch published one port, the
	// socket was behind another, and nothing arrived. The reflectors
	// agreed with each other because they are alike — large providers
	// reached the same way out of the carrier. A disagreement only
	// appears when an observer that is not one of them is asked too,
	// and the rendezvous is one this project already runs.
	got = append(got, askRendezvous(c))

	for _, r := range got {
		if r.Err != nil {
			fmt.Printf("  %-32s  unreachable: %v\n", r.Server, r.Err)
			continue
		}
		fmt.Printf("  %-32s  seen as %s\n", r.Server, r.Mapped)
	}
	fmt.Println()
	fmt.Println(verdict(got, local))

	// The address itself is not taken from here. A reflector names the
	// socket that asked it, and a NAT that renumbers ports gives the
	// socket libp2p punches from a different external port; the relay is
	// asked for that one instead. What this command settles is whether
	// any single address exists to be found at all.
	return nil
}

// askRendezvous asks the pairing server what it sees, shaped as a
// reflection so it is weighed with the rest. It speaks its own two-word
// reply rather than STUN, which is why it cannot go in that package.
func askRendezvous(c *net.UDPConn) stun.Reflection {
	at := config.Str("RATATOSKR_PUNCH_RENDEZVOUS", "103.181.143.222:9600")
	r := stun.Reflection{Server: "rendezvous " + at}

	addr, err := net.ResolveUDPAddr("udp4", at)
	if err != nil {
		r.Err = err
		return r
	}
	r.IP = addr.IP

	defer c.SetReadDeadline(time.Time{})
	if _, err := c.WriteToUDP([]byte("natcheck observe"), addr); err != nil {
		r.Err = err
		return r
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	for {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			r.Err = err
			return r
		}
		if !from.IP.Equal(addr.IP) {
			continue
		}
		f := strings.Fields(string(buf[:n]))
		if len(f) == 0 {
			r.Err = fmt.Errorf("empty reply")
			return r
		}
		r.Mapped = f[0]
		return r
	}
}

// verdict classifies the mapping, and says plainly when the evidence is
// not enough to classify it rather than guessing.
func verdict(got []stun.Reflection, localPort int) string {
	byIP := map[string]map[string]bool{} // server ip -> mapped values
	all := map[string]bool{}
	for _, r := range got {
		if r.Err != nil {
			continue
		}
		k := r.IP.String()
		if byIP[k] == nil {
			byIP[k] = map[string]bool{}
		}
		byIP[k][r.Mapped] = true
		all[r.Mapped] = true
	}
	if len(all) == 0 {
		return "no reflector answered: the result is unknown, not good."
	}
	if len(byIP) < 2 {
		return "only one reflector address answered: cannot tell endpoint-independent from address-dependent. Add a second."
	}

	sameWithinIP := true
	for _, m := range byIP {
		if len(m) > 1 {
			sameWithinIP = false
		}
	}

	switch {
	case len(all) == 1:
		var one string
		for m := range all {
			one = m
		}
		return "Endpoint-Independent Mapping: one external port (" + one +
			") for every destination.\nHole punching can work: the port a third party observes is the port the far peer may use."
	case !sameWithinIP:
		return "Address-and-Port-Dependent Mapping (symmetric): the external port changes even between two ports on one address.\n" +
			"Hole punching cannot work: the observed port is valid only for the observer, so no address can be exchanged that the far peer may use.\n" +
			"Observed: " + strings.Join(sorted(all), " ")
	default:
		return "Address-Dependent Mapping: the external port is stable per destination address but differs between addresses.\n" +
			"Hole punching cannot work through it for the same reason: the far peer is a different address from the reflector.\n" +
			"Observed: " + strings.Join(sorted(all), " ")
	}
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
