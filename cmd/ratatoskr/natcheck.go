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
	interval := config.Duration("RATATOSKR_NATCHECK_WAIT", 30*time.Second)
	total := config.Duration("RATATOSKR_NATCHECK_FOR", 3*time.Minute)

	// The anchor is re-asked every round. It answers from the mapping it
	// was given in round one, so it says whether that mapping survives.
	//
	// Every other reflector is spent once and never asked again. A
	// destination this socket has already spoken to cannot show that the
	// allocator moved, because it still holds the port it was given; only
	// a destination met for the first time is given the current value.
	// A peer is always such a destination, which is why this is the
	// question that matters and why the pool has to be long enough for
	// one reflector per round.
	anchor := servers[0]
	fresh := servers[2:]

	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return fmt.Errorf("cannot open a UDP socket: %w", err)
	}
	defer c.Close()
	local := c.LocalAddr().(*net.UDPAddr).Port
	fmt.Printf("one socket, local port %d\n", local)
	fmt.Printf("asking every %s for %s\n\n", interval, total)

	// Round one: every observer inside a second, which is the test this
	// command used to be, and the whole of it.
	//
	// A rendezvous that is not a reflector is asked too. Three public
	// reflectors agreeing is what this command used to call
	// endpoint-independent mapping, and it was wrong on a carrier that
	// gave the very next port to a fourth destination: the reply said
	// hole punching can work, the punch published one port, the socket
	// was behind another, and nothing arrived. The reflectors agreed
	// with each other because they are alike — large providers reached
	// the same way out of the carrier.
	fmt.Println("round 0, all at once:")
	round1 := []stun.Reflection{stun.Ask(c, servers[0]), stun.Ask(c, servers[1]), askRendezvous(c)}
	report(round1)
	fmt.Println(verdict(round1, local))
	fmt.Println()

	base := ""
	for _, r := range round1 {
		if r.Err == nil && r.Server == anchor {
			base = r.Mapped
		}
	}
	if base == "" {
		return fmt.Errorf("the anchor reflector %s did not answer, so nothing can be compared against it", anchor)
	}

	seen := map[string]bool{}
	spoken := map[string]bool{}
	for _, r := range round1 {
		if r.Err == nil {
			seen[r.Mapped] = true
		}
		if r.IP != nil {
			spoken[r.IP.String()] = true
		}
	}

	var rounds []round
	for at := interval; at <= total; at += interval {
		if len(fresh) == 0 {
			fmt.Println("out of unused reflectors: stopping rather than re-asking one that already holds a mapping.")
			break
		}
		time.Sleep(interval)

		// A reflector that is down costs the round its only evidence,
		// and the round cannot be taken again — the next one is a
		// different moment. So spend another from the pool instead.
		r := round{at: at, anchor: stun.Ask(c, anchor)}
		var next string
		for len(fresh) > 0 {
			next, fresh = fresh[0], fresh[1:]
			if r.fresh = stun.Ask(c, next); r.fresh.Err == nil {
				break
			}
			fmt.Printf("  %-32s  unreachable: %v\n", next, r.fresh.Err)
		}
		if r.fresh.IP != nil && spoken[r.fresh.IP.String()] {
			r.reused = true
		}
		rounds = append(rounds, r)
		fmt.Printf("round %s:\n", at)
		report([]stun.Reflection{r.anchor, r.fresh})
		if r.reused {
			fmt.Printf("  (%s shares an address with a reflector already asked, so its answer is not evidence)\n\n", next)
		}
		for _, x := range []stun.Reflection{r.anchor, r.fresh} {
			if x.Err == nil {
				seen[x.Mapped] = true
			}
			if x.IP != nil {
				spoken[x.IP.String()] = true
			}
		}
	}

	fmt.Println(classify(base, rounds))
	fmt.Printf("\ndistinct addresses this one socket was seen as: %d\n  %s\n",
		len(seen), strings.Join(sorted(seen), "\n  "))
	return nil
}

// round is one later question, asked twice: of a reflector that already
// holds a mapping for this socket, and of one meeting it for the first
// time.
type round struct {
	at            time.Duration
	anchor, fresh stun.Reflection
	// reused is set when the "fresh" reflector turned out to resolve to
	// an address this socket had already spoken to. Two hostnames of one
	// provider often share an address, and such a reflector answers from
	// the mapping it was already given — it looks like agreement and
	// proves nothing. Counting it would manufacture the passing verdict.
	reused bool
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

// classify names the class that a single round cannot see: a NAT whose
// existing mappings hold while a destination met for the first time is
// given a different port. No RFC 4787 term covers it, because RFC 4787
// asks its questions at one moment. It is the class that defeats every
// form of address publication, since a peer is always a first-time
// destination and the address reaches it later than it was measured.
func classify(base string, rounds []round) string {
	var moved []string
	for _, r := range rounds {
		if r.anchor.Err == nil && r.anchor.Mapped != base {
			return "The mapping did not survive: the anchor saw " + base + " and at " +
				r.at.String() + " sees " + r.anchor.Mapped + ".\n" +
				"Nothing this socket is seen as can be published, because it does not last long enough to be dialled."
		}
		if r.reused {
			continue
		}
		if r.fresh.Err == nil && r.fresh.Mapped != base {
			moved = append(moved, r.at.String()+" "+r.fresh.Mapped)
		}
	}
	asked := 0
	for _, r := range rounds {
		if !r.reused && r.fresh.Err == nil {
			asked++
		}
	}
	if asked == 0 {
		return "no first-time destination answered in any later round: the result is unknown, not good."
	}
	if len(moved) == 0 {
		return "The mapping held for the whole run and every first-time destination was given the same port.\n" +
			"An address measured now is still an address a peer may dial later. Hole punching can work."
	}
	return "The mapping held (" + base + ") but a destination met for the first time was given another port:\n  " +
		strings.Join(moved, "\n  ") + "\n" +
		"This is the class that defeats publishing an address: the port is minted when a destination is\n" +
		"first spoken to, and a peer is always a first-time destination reached later than the measurement.\n" +
		"Publish one address and it will be wrong. Several observed addresses, refreshed while the agent\n" +
		"runs, are the only thing that can be right."
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
