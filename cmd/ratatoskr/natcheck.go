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
	servers := stun.Servers()
	if len(servers) < 2 {
		return fmt.Errorf("need at least two reflectors")
	}
	// The last reflector is held back. It is the only destination this
	// socket will not have spoken to when the second round asks it, and
	// a destination contacted for the first time is the only thing that
	// can show an allocator that has moved.
	held := servers[len(servers)-1]
	first := servers[:len(servers)-1]

	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return fmt.Errorf("cannot open a UDP socket: %w", err)
	}
	defer c.Close()
	local := c.LocalAddr().(*net.UDPAddr).Port
	fmt.Printf("one socket, local port %d\n\n", local)

	// Round one: every observer inside a second, which is the test this
	// command used to be.
	//
	// A rendezvous that is not a reflector is asked too. Three public
	// reflectors agreeing is what this command used to call
	// endpoint-independent mapping, and it was wrong on a carrier that
	// gave the very next port to a fourth destination: the reply said
	// hole punching can work, the punch published one port, the socket
	// was behind another, and nothing arrived. The reflectors agreed
	// with each other because they are alike — large providers reached
	// the same way out of the carrier.
	fmt.Println("round one, all at once:")
	var got []stun.Reflection
	for _, s := range first {
		got = append(got, stun.Ask(c, s))
	}
	got = append(got, askRendezvous(c))
	report(got)
	fmt.Println(verdict(got, local))

	// Round two, later. Two questions that one round cannot separate.
	//
	// Re-asking a reflector from round one says whether a mapping this
	// socket already holds survives. Asking the held-back reflector
	// says what a destination contacted for the first time is given
	// *now*. On this project's carrier the first answer holds and the
	// second does not, and that pair is the whole of why a published
	// address stops working: the mapping is minted when a destination
	// is first spoken to, and the allocator has moved on since the
	// address was published. A peer is always a first-time destination.
	wait := config.Duration("RATATOSKR_NATCHECK_WAIT", 30*time.Second)
	if wait <= 0 {
		return nil
	}
	fmt.Printf("\nwaiting %s, then asking again...\n\n", wait)
	time.Sleep(wait)

	fmt.Printf("round two, %s later:\n", wait)
	again := stun.Ask(c, first[0])
	fresh := stun.Ask(c, held)
	report([]stun.Reflection{again, fresh})
	fmt.Println(drift(got, again, fresh))
	return nil
}

func report(got []stun.Reflection) {
	for _, r := range got {
		if r.Err != nil {
			fmt.Printf("  %-32s  unreachable: %v\n", r.Server, r.Err)
			continue
		}
		fmt.Printf("  %-32s  seen as %s\n", r.Server, r.Mapped)
	}
	fmt.Println()
}

// drift compares the second round with the first and names the class
// that a single round cannot see: a NAT whose existing mappings hold
// while a destination met for the first time is given a different port.
// No RFC 4787 term covers it, because RFC 4787 asks its questions at one
// moment. It is the class that defeats every form of address
// publication, since a peer is always a first-time destination and the
// address reaches it later than it was measured.
func drift(round1 []stun.Reflection, again, fresh stun.Reflection) string {
	if again.Err != nil || fresh.Err != nil {
		return "round two did not complete: the result is unknown, not good."
	}
	var was string
	for _, r := range round1 {
		if r.Err == nil && r.Server == again.Server {
			was = r.Mapped
		}
	}
	held := was == again.Mapped

	switch {
	case held && fresh.Mapped == again.Mapped:
		return "The mapping held and a first-time destination was given the same port.\n" +
			"An address measured now is still an address a peer may dial later. Hole punching can work."
	case held:
		return "The mapping held (" + again.Mapped + ") but a destination met for the first time was given " +
			fresh.Mapped + ".\n" +
			"This is the class that defeats publishing an address: the port is minted when a destination is\n" +
			"first spoken to, and a peer is always a first-time destination reached later than the measurement.\n" +
			"Publish one address and it will be wrong. Several observed addresses, refreshed while the agent\n" +
			"runs, are the only thing that can be right."
	default:
		return "The mapping did not survive: " + again.Server + " saw " + was + " and now sees " + again.Mapped + ".\n" +
			"Nothing this socket is seen as can be published, because it does not last long enough to be dialled."
	}
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
