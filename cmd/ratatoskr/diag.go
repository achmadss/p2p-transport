package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
	"github.com/achmadss/p2p-transport/internal/transport"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multiaddr"
)

// diagnose prints what the NAT traversal stack believes about this
// machine while it serves: AutoNAT's verdict, whether a relay
// reservation exists, and every address the host is willing to
// advertise. Without it a failed hole punch is a single line of
// "no hole punch" with nothing behind it, and the three separate
// things that must all succeed cannot be told apart.
//
// SPEC.md §4 keeps this language out of ordinary output, which is
// why it is behind RATATOSKR_DIAG rather than always on.
func diagnose(ctx context.Context, h *transport.Host) {
	if config.Str("RATATOSKR_DIAG", "") == "" {
		return
	}

	sub, err := h.Host().EventBus().Subscribe([]interface{}{
		new(event.EvtLocalReachabilityChanged),
		new(event.EvtLocalAddressesUpdated),
	})
	if err != nil {
		fmt.Println("diag: cannot subscribe:", err)
		return
	}

	fmt.Println("diag: reachability unknown until AutoNAT answers")
	go func() {
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-sub.Out():
				if !ok {
					return
				}
				switch v := e.(type) {
				case event.EvtLocalReachabilityChanged:
					fmt.Printf("diag: AutoNAT says this machine is %s\n", v.Reachability)
				case event.EvtLocalAddressesUpdated:
					fmt.Println("diag: advertised addresses changed:")
					printAddrs(h)
				}
			}
		}
	}()

	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				printAddrs(h)
				printConns(h)
			}
		}
	}()
}

func printAddrs(h *transport.Host) {
	var relayed, direct []string
	for _, a := range h.Host().Addrs() {
		if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err == nil {
			relayed = append(relayed, a.String())
			continue
		}
		direct = append(direct, a.String())
	}
	fmt.Printf("diag:   direct addrs (%d): %s\n", len(direct), strings.Join(direct, " "))
	if len(relayed) == 0 {
		fmt.Println("diag:   relay reservation: none")
		return
	}
	fmt.Printf("diag:   relay reservation: yes, %s\n", strings.Join(relayed, " "))
}

func printConns(h *transport.Host) {
	for _, c := range h.Host().Network().Conns() {
		limited := ""
		if c.Stat().Limited {
			limited = " limited"
		}
		fmt.Printf("diag:   conn %s  %s -> %s  %s%s\n",
			transport.Describe(c).Path,
			c.LocalMultiaddr(), c.RemoteMultiaddr(),
			shortPeer(c), limited)
	}
}

func shortPeer(c network.Conn) string {
	s := c.RemotePeer().String()
	if len(s) > 8 {
		return s[len(s)-8:]
	}
	return s
}
