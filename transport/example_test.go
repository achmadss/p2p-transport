// The import block below is every package a consumer of this module has
// to know about. If libp2p or multiaddr ever appears in it, the surface
// has leaked something it was meant to hide.
package transport_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/achmadss/p2p-transport/transport"
)

// Example is the whole of what an application does with this module:
// start, serve a protocol of its own, reach another machine at the
// addresses it published, and ask which path the bytes are taking.
func Example() {
	// Two machines. A real pair would each have their own config
	// directory and find each other with OnLAN; here they share a
	// process, so the addresses are handed over directly.
	server, cleanup := host()
	defer cleanup()
	client, cleanup2 := host()
	defer cleanup2()

	const proto = "/example/echo/1.0.0"
	server.Handle(proto, func(s transport.Stream) {
		defer s.Close()
		fmt.Println("serving", s.Peer() == client.ID())
		io.Copy(s, s)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The addresses are opaque: out of Addrs over there, into Connect
	// over here, and nothing in between parses one.
	if err := client.Connect(ctx, server.ID(), server.Addrs()); err != nil {
		fmt.Println("connect:", err)
		return
	}
	fmt.Println("connected to", len(client.Peers()), "machine")

	s, err := client.Open(ctx, server.ID(), proto)
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer s.Close()

	io.WriteString(s, "hello\n")
	s.CloseWrite() // done sending, still reading
	line, err := bufio.NewReader(s).ReadString('\n')
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Print(line)

	// Which path the bytes took. A small request need never ask; a
	// caller moving gigabytes is who this is for.
	fmt.Println("path:", s.Path(), s.Path().BetterThan(transport.PathRelay))

	// Output:
	// connected to 1 machine
	// serving true
	// hello
	// path: lan true
}

func host() (*transport.Host, func()) {
	dir, err := os.MkdirTemp("", "transport-example")
	if err != nil {
		panic(err)
	}
	// NoLAN so the example opens no multicast socket on whatever
	// machine runs the tests.
	h, err := transport.New(transport.Config{Dir: dir, NoLAN: true})
	if err != nil {
		panic(err)
	}
	return h, func() { h.Close(); os.RemoveAll(dir) }
}
