package transport

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/multiformats/go-multiaddr"
)

// diagnose prints what the connection machinery believes about this
// machine, to stderr, for as long as the host runs. Set RATATOSKR_DIAG
// to switch it on; it is off by default because a caller wants a path,
// not a mechanism.
//
// It prints the reachability verdict, whether a relay reservation
// exists, and the address of every live connection. Without those, a
// failed connection is one line with nothing behind it and the three
// things that must all succeed cannot be told apart. It lives here
// rather than in the harness because reaching these facts from outside
// would mean exposing the underlying host to do it.
func (t *Host) diagnose() {
	if os.Getenv("RATATOSKR_DIAG") == "" {
		return
	}

	sub, err := t.h.EventBus().Subscribe([]interface{}{
		new(event.EvtLocalReachabilityChanged),
		new(event.EvtLocalAddressesUpdated),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "diag: cannot subscribe:", err)
		return
	}

	fmt.Fprintln(os.Stderr, "diag: reachability unknown until AutoNAT answers")
	go func() {
		defer sub.Close()
		for {
			select {
			case <-t.done:
				return
			case e, ok := <-sub.Out():
				if !ok {
					return
				}
				switch v := e.(type) {
				case event.EvtLocalReachabilityChanged:
					fmt.Fprintf(os.Stderr, "diag: AutoNAT says this machine is %s\n", v.Reachability)
				case event.EvtLocalAddressesUpdated:
					fmt.Fprintln(os.Stderr, "diag: advertised addresses changed:")
					t.printAddrs()
				}
			}
		}
	}()

	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-tick.C:
				t.printAddrs()
				t.printConns()
			}
		}
	}()
}

func (t *Host) printAddrs() {
	var relayed, direct []string
	for _, a := range t.h.Addrs() {
		if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
			relayed = append(relayed, a.String())
			continue
		}
		direct = append(direct, a.String())
	}
	fmt.Fprintf(os.Stderr, "diag:   direct addrs (%d): %s\n", len(direct), strings.Join(direct, " "))
	if len(relayed) == 0 {
		fmt.Fprintln(os.Stderr, "diag:   relay reservation: none")
		return
	}
	fmt.Fprintf(os.Stderr, "diag:   relay reservation: yes, %s\n", strings.Join(relayed, " "))
}

func (t *Host) printConns() {
	for _, c := range t.h.Network().Conns() {
		limited := ""
		if c.Stat().Limited {
			limited = " limited"
		}
		fmt.Fprintf(os.Stderr, "diag:   conn %s  %s -> %s  %s%s\n",
			pathOfConn(c), c.LocalMultiaddr(), c.RemoteMultiaddr(),
			PeerID(c.RemotePeer().String()).Short(), limited)
	}
}
