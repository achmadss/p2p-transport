package transport

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/multiformats/go-multiaddr"
)

// The escape hatch. SPEC.md §4 keeps every word below the seam out of
// ordinary output, and then allows exactly one place where all of it
// appears: RATATOSKR_DIAG. A bug report needs the mechanism; a file
// manager does not.
//
// It lives in this package rather than in the harness because the facts
// it prints — the reachability verdict, whether a relay reservation
// exists, the address of every live connection — are below the seam, and
// reaching them from outside would mean exposing the libp2p host to do
// it. Without it a failed hole punch is one line of "no direct path"
// with nothing behind it, and the three separate things that must all
// succeed cannot be told apart.
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
