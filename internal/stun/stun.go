// Package stun asks a public reflector what address the world sees this
// socket as.
//
// It answers one question and not the other. Asked from several
// reflectors on one socket, it classifies the NAT's mapping behaviour
// (RFC 4787), which decides whether a hole punch can work here at all.
// It cannot supply the address to advertise: the port it reports belongs
// to the socket that asked, and a NAT that renumbers ports gives the
// socket libp2p punches from a different one. That address is asked of
// the relay instead; see transport.ObservedProto.
//
// Its callers are therefore `ratatoskr natcheck` and `punchtest`, both
// diagnostics.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/achmadss/ratatoskr/internal/config"
)

// Servers are the reflectors. Three by default, on at least two
// distinct addresses, because one address cannot tell an
// endpoint-independent mapping from an address-dependent one.
func Servers() []string {
	if s := config.List("RATATOSKR_STUN"); len(s) > 0 {
		return s
	}
	return []string{
		"stun.l.google.com:19302",
		"stun1.l.google.com:19302",
		"stun.cloudflare.com:3478",
	}
}

// Reflection is one server's answer.
type Reflection struct {
	Server string
	IP     net.IP // the server's own address, to group answers by
	Mapped string // what it saw us as, "ip:port"
	Err    error
}

// Ask sends one binding request on an existing socket and reads the
// mapped address back. The socket is shared across servers on purpose:
// a fresh socket per server would measure nothing.
func Ask(c *net.UDPConn, server string) Reflection {
	r := Reflection{Server: server}

	addr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		r.Err = err
		return r
	}
	r.IP = addr.IP

	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001) // binding request
	binary.BigEndian.PutUint16(req[2:], 0)      // no attributes
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	if _, err := rand.Read(req[8:20]); err != nil {
		r.Err = err
		return r
	}

	if _, err := c.WriteToUDP(req, addr); err != nil {
		r.Err = err
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
			r.Err = err
			return r
		}
		if n < 20 || !from.IP.Equal(addr.IP) {
			continue
		}
		if string(buf[8:20]) != string(req[8:20]) {
			continue
		}
		if m := parseMapped(buf[:n]); m != "" {
			r.Mapped = m
			return r
		}
	}
	r.Err = fmt.Errorf("no usable reply")
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

// Reflect asks every server from one socket and returns the answers
// alongside the local port they were sent from.
func Reflect() ([]Reflection, int) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, 0
	}
	defer c.Close()

	var got []Reflection
	for _, s := range Servers() {
		got = append(got, Ask(c, s))
	}
	return got, c.LocalAddr().(*net.UDPAddr).Port
}
