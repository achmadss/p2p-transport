package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/quic-go/quic-go"
)

const alpn = "ratatoskr-punch"

// punchQUIC is punchtest with the last difference removed.
//
// punchtest proves a UDP packet can cross a pair of NATs. It does not
// prove a QUIC handshake can, and that is what the agent needs: several
// packets each way, inside one window, on a mapping both NATs hold open
// for the whole exchange. A path that passes one datagram and loses the
// fourth looks like a working punch to punchtest and like a failure to
// libp2p.
//
// So this runs quic-go — the same library libp2p runs, over the same
// socket that asked STUN and punched. Nothing of libp2p is in the way:
// no DCUtR, no relay, no identify, no address discovery. If this
// connects and `ratatoskr connect` does not, the fault is in how we
// drive libp2p and nowhere else. If this fails too, no configuration
// would have saved it.
//
// One side listens and one dials, named on the command line, because
// with two machines and one operator there is nothing to negotiate.
func punchQUIC(role string) error {
	if role != "listen" && role != "dial" {
		return fmt.Errorf("punch-quic needs a role: listen on one machine, dial on the other")
	}

	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return err
	}
	defer c.Close()

	peer, err := meetPeer(c)
	if err != nil {
		return err
	}

	// Punch first, in the open, and say how many packets crossed.
	//
	// Without this the run reports "no QUIC connection" and means two
	// different things at once: the path was shut, or the path was open
	// and QUIC alone could not cross it. Those are opposite answers, and
	// telling them apart used to need a second machine, a second operator
	// and a capture. quic.Transport takes the socket over, so the count
	// has to be taken before it does.
	// Setting the window to zero makes QUIC the first thing this socket
	// ever sends to the peer, which is the one condition the agent is
	// always in and this test never was.
	// config.Duration reads a zero as "unset" and hands back the default,
	// which is right everywhere else and wrong here: zero is the setting.
	raw := -1
	if w := config.Duration("RATATOSKR_PUNCH_RAW", 15*time.Second); os.Getenv("RATATOSKR_PUNCH_RAW") != "0" && w > 0 {
		raw = rawPunch(c, peer, w)
		fmt.Printf("  raw punch: %d packets arrived from the peer\n", raw)
		if raw == 0 {
			fmt.Println("  the path is shut, so this run says nothing about QUIC.")
		}
	} else {
		fmt.Print("\nskipping the bare punch: QUIC goes first, as it does in the agent.\n")
	}

	tr := &quic.Transport{Conn: c}
	defer tr.Close()

	// Open this side's NAT before either side tries to handshake. A
	// listener that has not sent anything is a listener nothing reaches:
	// the mapping only exists once a packet has left. Sixty-four random
	// bytes is what libp2p sends here, so it is what this sends.
	//
	// QUIC drops the junk as unparseable, which is the point — it opens
	// the hole without pretending to be a handshake.
	stop := make(chan struct{})
	go func() {
		junk := make([]byte, 64)
		for {
			select {
			case <-stop:
				return
			default:
			}
			rand.Read(junk)
			tr.WriteTo(junk, peer)
			time.Sleep(200 * time.Millisecond)
		}
	}()

	wait := config.Duration("RATATOSKR_PUNCH_SECONDS", 30*time.Second)
	fmt.Printf("\npunching at %s, then speaking QUIC as the %ser. up to %s.\n\n", peer, role, wait)

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	if err := speakQUIC(ctx, tr, peer, role); err != nil {
		close(stop)
		fmt.Printf("  no QUIC connection: %v\n\n", err)
		if raw < 0 {
			fmt.Println("QUIC went first and got nowhere. Run it again with the bare punch")
			fmt.Println("in front: if that succeeds, the path only opens for a flow that")
			fmt.Println("does not begin with a QUIC handshake, and the agent must open it")
			fmt.Println("the same way before DCUtR dials.")
		} else if raw > 0 {
			fmt.Println("Bare packets crossed this path seconds ago and a handshake could")
			fmt.Println("not. The path passes datagrams but not QUIC, and no library or")
			fmt.Println("configuration would have got through it.")
		} else {
			fmt.Println("Nothing crossed at all, so the path was shut and QUIC was never")
			fmt.Println("tested. Start both sides closer together and run it again.")
		}
		return nil
	}
	close(stop)

	fmt.Print("\n  a stream carried bytes both ways.\n\n")
	fmt.Println("QUIC completes a handshake through this hole. The path is not the")
	fmt.Println("problem, and neither is quic-go: whatever fails in `connect` fails")
	fmt.Println("above them both.")
	return nil
}

// rawPunch sends and counts bare packets for a fixed window, so the QUIC
// result that follows can be read against a known path.
func rawPunch(c *net.UDPConn, peer *net.UDPAddr, window time.Duration) int {
	stop := time.Now().Add(window)
	fmt.Printf("\npunching bare packets at %s for %s.\n", peer, window)

	done := make(chan struct{})
	defer close(done)
	go func() {
		p := append([]byte{'S'}, make([]byte, 14)...)
		for {
			select {
			case <-done:
				return
			default:
			}
			c.WriteToUDP(p, peer)
			time.Sleep(200 * time.Millisecond)
		}
	}()

	var got int
	buf := make([]byte, 2000)
	for time.Now().Before(stop) {
		c.SetReadDeadline(stop)
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if n > 0 && from.IP.Equal(peer.IP) {
			got++
		}
	}
	c.SetReadDeadline(time.Time{})
	return got
}

// speakQUIC handshakes over an already-punched socket and echoes one
// message, which is the smallest exchange that proves more than a
// handshake: bytes crossed in both directions on a stream.
func speakQUIC(ctx context.Context, tr *quic.Transport, peer net.Addr, role string) error {
	cert, err := selfSigned()
	if err != nil {
		return err
	}

	var conn *quic.Conn
	if role == "listen" {
		ln, err := tr.Listen(&tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{alpn},
		}, nil)
		if err != nil {
			return err
		}
		defer ln.Close()
		if conn, err = ln.Accept(ctx); err != nil {
			return err
		}
	} else {
		// The certificate is not checked. This measures a network path,
		// not an identity; the agent proves identity with Noise.
		if conn, err = tr.Dial(ctx, peer, &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{alpn},
		}, nil); err != nil {
			return err
		}
	}
	defer conn.CloseWithError(0, "done")
	fmt.Printf("  QUIC connected: %s <-> %s\n", conn.LocalAddr(), conn.RemoteAddr())

	const msg = "ratatoskr punched a hole\n"
	if role == "listen" {
		s, err := conn.AcceptStream(ctx)
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}
		if _, err := io.Copy(s, s); err != nil {
			return fmt.Errorf("echo: %w", err)
		}
		s.Close()
		// Wait for the far side to hang up. Returning here would run the
		// deferred close first and cut the echo off before it is read,
		// which on a fast path is a race and on a slow one is a lie.
		select {
		case <-conn.Context().Done():
		case <-ctx.Done():
		}
		return nil
	}

	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	if _, err := io.WriteString(s, msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	s.Close()
	back, err := io.ReadAll(s)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if string(back) != msg {
		return fmt.Errorf("echo mismatch: sent %q, got %q", msg, back)
	}
	return nil
}

// selfSigned is a throwaway certificate. QUIC requires TLS and this test
// has nothing to authenticate — the agent's identity comes from Noise
// over a libp2p stream, which is not what is being measured here.
func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ratatoskr-punch"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
