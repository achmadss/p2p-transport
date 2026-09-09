package transport

import (
	"net"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/stun"
)

// The socket is shared with quic-go, so the reply to our own question
// has to be taken out of the stream and everything else left in it.
// Getting that wrong either loses the answer or feeds quic-go a packet
// it will close the connection over.
func TestSelfAddrClaimsOnlyItsOwnReply(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := &selfAddr{PacketConn: c, waiting: map[string]chan string{}}

	// A reflector on loopback that answers whatever it is asked.
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := srv.ReadFromUDP(buf)
			if err != nil {
				return
			}
			srv.WriteToUDP(reply(buf[:n], from), from)
		}
	}()

	// Something that is not our reply must reach the reader untouched.
	go func() {
		time.Sleep(300 * time.Millisecond)
		other, _ := net.DialUDP("udp4", nil, c.LocalAddr().(*net.UDPAddr))
		other.Write([]byte("quic would like this please"))
		other.Close()
	}()

	got := make(chan string, 1)
	go func() { got <- s.ask(srv.LocalAddr().String(), 3*time.Second) }()

	buf := make([]byte, 1024)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := s.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if string(buf[:n]) != "quic would like this please" {
		t.Errorf("the reader got %q, which is not the packet meant for it", buf[:n])
	}

	select {
	case a := <-got:
		if a != "203.0.113.9:4242" {
			t.Errorf("measured %q, want 203.0.113.9:4242", a)
		}
	case <-time.After(time.Second):
		t.Error("the answer never arrived: the reply was passed on instead of claimed")
	}
}

// reply builds a binding response for a request, naming a fixed address.
func reply(req []byte, _ *net.UDPAddr) []byte {
	b := make([]byte, 20+12)
	copy(b, []byte{0x01, 0x01, 0x00, 0x0c})
	copy(b[4:], req[4:20]) // cookie and transaction id
	// XOR-MAPPED-ADDRESS for 203.0.113.9:4242
	copy(b[20:], []byte{0x00, 0x20, 0x00, 0x08, 0x00, 0x01})
	port := uint16(4242) ^ 0x2112
	b[26], b[27] = byte(port>>8), byte(port)
	ip := net.IPv4(203, 0, 113, 9).To4()
	cookie := []byte{0x21, 0x12, 0xA4, 0x42}
	for i := 0; i < 4; i++ {
		b[28+i] = ip[i] ^ cookie[i]
	}
	return b
}

// Guard the assumption the test above rests on: this is the same parser
// the agent uses, not a second one written to agree with the fixture.
func TestFixtureParsesWithTheRealParser(t *testing.T) {
	req, txid, err := stun.Request()
	if err != nil {
		t.Fatal(err)
	}
	if got := stun.ParseResponse(reply(req, nil), txid); got != "203.0.113.9:4242" {
		t.Fatalf("parser read %q from the fixture", got)
	}
}

// TestKeepCorroborated pins the rule that decides what gets published.
// Two reflectors agreeing on a port is evidence of a door; one reflector
// alone is a door minted for that reflector, and publishing it costs
// every peer a dial into nothing. The first answer is kept regardless,
// or an ordinary NAT whose reflectors happen to disagree would advertise
// no address at all.
func TestKeepCorroborated(t *testing.T) {
	cases := []struct {
		name  string
		order []string
		seen  map[string]int
		want  []string
	}{
		{
			name:  "one port, everyone agrees",
			order: []string{"203.0.113.9:4242"},
			seen:  map[string]int{"203.0.113.9:4242": 8},
			want:  []string{"203.0.113.9:4242"},
		},
		{
			name:  "a second door, seen twice, is offered too",
			order: []string{"203.0.113.9:4242", "203.0.113.9:5555"},
			seen:  map[string]int{"203.0.113.9:4242": 5, "203.0.113.9:5555": 2},
			want:  []string{"203.0.113.9:4242", "203.0.113.9:5555"},
		},
		{
			name:  "a port only one reflector saw is noise",
			order: []string{"203.0.113.9:4242", "203.0.113.9:6001"},
			seen:  map[string]int{"203.0.113.9:4242": 4, "203.0.113.9:6001": 1},
			want:  []string{"203.0.113.9:4242"},
		},
		{
			name:  "the symmetric carrier: every answer differs, the first still stands",
			order: []string{"203.0.113.9:1", "203.0.113.9:2", "203.0.113.9:3"},
			seen:  map[string]int{"203.0.113.9:1": 1, "203.0.113.9:2": 1, "203.0.113.9:3": 1},
			want:  []string{"203.0.113.9:1"},
		},
		{name: "nobody answered", order: nil, seen: map[string]int{}, want: nil},
	}
	for _, c := range cases {
		got := keepCorroborated(c.order, c.seen)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}
