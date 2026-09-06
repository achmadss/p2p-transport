package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/achmadss/ratatoskr/internal/config"
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

var stunServers = config.List("RATATOSKR_STUN")

func defaultStun() []string {
	if len(stunServers) > 0 {
		return stunServers
	}
	return []string{
		"stun.l.google.com:19302",
		"stun1.l.google.com:19302",
		"stun.cloudflare.com:3478",
	}
}

type reflection struct {
	server string
	ip     net.IP
	mapped string
	err    error
}

// stunAsk sends one binding request on an existing socket and reads the
// mapped address back. The socket is shared across servers on purpose:
// a fresh socket per server would measure nothing.
func stunAsk(c *net.UDPConn, server string) reflection {
	r := reflection{server: server}

	addr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		r.err = err
		return r
	}
	r.ip = addr.IP

	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001) // binding request
	binary.BigEndian.PutUint16(req[2:], 0)      // no attributes
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	if _, err := rand.Read(req[8:20]); err != nil {
		r.err = err
		return r
	}

	if _, err := c.WriteToUDP(req, addr); err != nil {
		r.err = err
		return r
	}

	// Read until a reply carrying our transaction id arrives: another
	// server's reply may be in flight on the same socket.
	deadline := time.Now().Add(3 * time.Second)
	c.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	for time.Now().Before(deadline) {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			r.err = err
			return r
		}
		if n < 20 || !from.IP.Equal(addr.IP) {
			continue
		}
		if string(buf[8:20]) != string(req[8:20]) {
			continue
		}
		if m := parseMapped(buf[:n]); m != "" {
			r.mapped = m
			return r
		}
	}
	r.err = fmt.Errorf("no usable reply")
	return r
}

// parseMapped reads XOR-MAPPED-ADDRESS, falling back to the older
// MAPPED-ADDRESS that some servers still answer with.
func parseMapped(b []byte) string {
	n := int(binary.BigEndian.Uint16(b[2:]))
	if 20+n > len(b) {
		n = len(b) - 20
	}
	for p := 20; p+4 <= 20+n; {
		typ := binary.BigEndian.Uint16(b[p:])
		l := int(binary.BigEndian.Uint16(b[p+2:]))
		v := b[p+4:]
		if l > len(v) {
			return ""
		}
		v = v[:l]
		if (typ == 0x0020 || typ == 0x0001) && l >= 8 && v[1] == 0x01 {
			port := binary.BigEndian.Uint16(v[2:])
			ip := net.IP(append([]byte(nil), v[4:8]...))
			if typ == 0x0020 {
				port ^= 0x2112
				for i := range ip {
					ip[i] ^= b[4+i]
				}
			}
			return fmt.Sprintf("%s:%d", ip, port)
		}
		p += 4 + l
		p += (4 - l%4) % 4 // attributes are padded to four bytes
	}
	return ""
}

func natcheck() error {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return err
	}
	defer c.Close()

	local := c.LocalAddr().(*net.UDPAddr)
	fmt.Printf("one socket, local port %d\n\n", local.Port)

	var got []reflection
	for _, s := range defaultStun() {
		r := stunAsk(c, s)
		got = append(got, r)
		if r.err != nil {
			fmt.Printf("  %-32s  unreachable: %v\n", s, r.err)
			continue
		}
		fmt.Printf("  %-32s  seen as %s\n", s, r.mapped)
	}
	fmt.Println()
	fmt.Println(verdict(got, local.Port))
	return nil
}

// verdict classifies the mapping, and says plainly when the evidence is
// not enough to classify it rather than guessing.
func verdict(got []reflection, localPort int) string {
	byIP := map[string]map[string]bool{} // server ip -> mapped values
	all := map[string]bool{}
	for _, r := range got {
		if r.err != nil {
			continue
		}
		k := r.ip.String()
		if byIP[k] == nil {
			byIP[k] = map[string]bool{}
		}
		byIP[k][r.mapped] = true
		all[r.mapped] = true
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
