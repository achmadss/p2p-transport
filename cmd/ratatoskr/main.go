// Command ratatoskr is both the agent and the client. One keypair, one
// peer id, both roles.
//
// The dev-listen and dev-dial subcommands are step 0 scaffolding. They
// prove a Noise-secured QUIC stream carries bytes between two machines,
// and they go away once run/connect of PLAN.md §16 replace them.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/achmadss/ratatoskr/internal/transport"
	"github.com/libp2p/go-libp2p/core/network"
)

const version = "0.0.1"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println("ratatoskr", version)
	case "dev-listen":
		err = devListen()
	case "dev-dial":
		if len(os.Args) < 3 {
			err = fmt.Errorf("dev-dial needs an address")
			break
		}
		err = devDial(os.Args[2])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ratatoskr:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ratatoskr <command>

  version              print the version
  dev-listen           listen and echo (step 0 scaffold)
  dev-dial <addr>      dial an address and echo a line (step 0 scaffold)
`)
}

func devListen() error {
	h, err := transport.New(transport.Options{})
	if err != nil {
		return err
	}
	defer h.Close()

	h.Handle(transport.EchoProto, func(s network.Stream) {
		defer s.Close()
		c := transport.Describe(s.Conn())
		fmt.Printf("stream from %s over %s (%s) at %s\n",
			c.Peer, c.Transport, c.Path, c.Addr)
		if _, err := io.Copy(s, s); err != nil {
			fmt.Fprintln(os.Stderr, "echo:", err)
		}
	})

	fmt.Println("peer id:", h.ID())
	fmt.Println("dial one of:")
	for _, a := range h.Addrs() {
		fmt.Println("  ", a)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("\nstopping")
	return nil
}

func devDial(addr string) error {
	h, err := transport.New(transport.Options{})
	if err != nil {
		return err
	}
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := h.Dial(ctx, addr, transport.EchoProto)
	if err != nil {
		return err
	}
	defer s.Close()

	c := transport.Describe(s.Conn())
	fmt.Printf("connected to %s over %s (%s) at %s\n",
		c.Peer, c.Transport, c.Path, c.Addr)

	const msg = "ratatoskr says hello\n"
	if _, err := io.WriteString(s, msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := s.CloseWrite(); err != nil {
		return fmt.Errorf("half close: %w", err)
	}

	back, err := bufio.NewReader(s).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if back != msg {
		return fmt.Errorf("echo mismatch: sent %q, got %q", msg, back)
	}
	fmt.Printf("echo ok: %q\n", back)
	return nil
}
