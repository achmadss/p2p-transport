// Package stun asks a public reflector what address the world sees this
// socket as.
//
// It answers two questions. Asked from several reflectors on one socket,
// it classifies the NAT's mapping behaviour (RFC 4787), which decides
// whether a hole punch can work here at all; that is `ratatoskr
// natcheck` and `punch-quic`, both diagnostics.
//
// It also supplies the addresses to advertise, but only when asked
// through the socket that will do the punching. The port a reflector
// reports belongs to the socket that asked, so a throwaway socket
// learns nothing about libp2p's — which is why transport/selfaddr.go
// wraps quic-go's own socket before asking, and why asking the relay
// over transport.ObservedProto is the other half rather than the
// answer: the relay names a door opened at startup, and this names one
// opened now.
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
)

// Servers are the reflectors. Two are the minimum, because one address
// cannot tell an endpoint-independent mapping from an address-dependent
// one. The list is long because `natcheck` spends one of them per round
// and never reuses it: a destination this socket has already spoken to
// answers from the mapping it was given then, so only a destination met
// for the first time can show that the allocator has moved. Six rounds
// over three minutes needs six unused reflectors, and several providers
// rather than one, since hosts of the same provider can share an
// address and would not be distinct destinations at all. Every entry
// here answered from this house on 7 Sep 2026 and every one resolved to
// a different address; the five `stunN.l.google.com` hosts that used to
// be here share one, which made a round look like agreement while
// asking nobody new.
func Servers() []string {
	if s := config.List("RATATOSKR_STUN"); len(s) > 0 {
		return s
	}
	return []string{
		"stun.l.google.com:19302",
		"stun.cloudflare.com:3478",
		"stun.nextcloud.com:3478",
		"stun.sipgate.net:3478",
		"stun.voipbuster.com:3478",
		"stun.antisip.com:3478",
		"stun.t-online.de:3478",
		"stun.voip.blackberry.com:3478",
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

	req, txid, err := Request()
	if err != nil {
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
		if !from.IP.Equal(addr.IP) {
			continue
		}
		if m := ParseResponse(buf[:n], txid); m != "" {
			r.Mapped = m
			return r
		}
	}
	r.Err = fmt.Errorf("no usable reply")
	return r
}

// Request builds one binding request and returns it with its
// transaction id.
//
// It is exported because the socket libp2p punches from is not a
// *net.UDPConn this package can be handed: it belongs to quic-go, and
// the only way to ask a reflector about it is to write the request into
// that socket and recognise the answer coming back out. Building the
// bytes here is what keeps one implementation of STUN in this repo.
func Request() (req, txid []byte, err error) {
	req = make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001) // binding request
	binary.BigEndian.PutUint16(req[2:], 0)      // no attributes
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	if _, err := rand.Read(req[8:20]); err != nil {
		return nil, nil, err
	}
	return req, req[8:20], nil
}

// ParseResponse returns the mapped address a reply carries, or "" if the
// bytes are not a reply to this transaction. A caller sharing a socket
// with other traffic uses the empty answer to mean "not mine, pass it
// on" — which is the whole reason both halves are separable.
func ParseResponse(b, txid []byte) string {
	if len(b) < 20 || string(b[8:20]) != string(txid) {
		return ""
	}
	return parseMapped(b)
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

// Reflect asks every server from one socket and hands the socket back
// still open.
//
// The caller closes it. It has to stay open because the reflectors are
// not the only observer worth asking, and an answer from a second socket
// belongs to that socket: a carrier that gives a new external port per
// destination is invisible unless every question is asked down the same
// one. That carrier is exactly what this command exists to catch.
func Reflect() ([]Reflection, *net.UDPConn) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, nil
	}
	var got []Reflection
	for _, s := range Servers() {
		got = append(got, Ask(c, s))
	}
	return got, c
}
