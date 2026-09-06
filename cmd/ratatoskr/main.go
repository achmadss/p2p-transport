// Command ratatoskr is the peer agent: it serves files from this machine and
// acts as a client against another agent.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/achmadss/ratatoskr/internal/transport"
	"github.com/pion/webrtc/v4"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `ratatoskr - peer file agent

Usage:
  ratatoskr version
  ratatoskr dev-offer     start a connection; prints an offer, reads an answer
  ratatoskr dev-answer    join a connection; reads an offer, prints an answer

dev-offer and dev-answer exchange SDP by hand. They are replaced by heimdall
in step 1.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "dev-offer":
		err = devOffer()
	case "dev-answer":
		err = devAnswer()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ratatoskr: %v\n", err)
		os.Exit(1)
	}
}

// devOffer is the client side of the step 0 echo test.
func devOffer() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := transport.New(transport.DefaultICEServers())
	if err != nil {
		return err
	}
	defer conn.Close()

	offer, err := conn.Offer(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "--- copy this OFFER into `ratatoskr dev-answer` ---")
	fmt.Println(offer)
	fmt.Fprintln(os.Stderr, "--- paste the ANSWER here, then press enter ---")

	answer, err := readBlob()
	if err != nil {
		return err
	}
	if err := conn.Accept(answer); err != nil {
		return err
	}

	if err := waitCtrl(conn, 30*time.Second); err != nil {
		return err
	}
	reportPath(conn)

	replies := make(chan string, 1)
	conn.Ctrl.OnMessage(func(m webrtc.DataChannelMessage) { replies <- string(m.Data) })

	const sent = "hello from ratatoskr"
	if err := conn.Ctrl.SendText(sent); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	fmt.Fprintf(os.Stderr, "sent:  %s\n", sent)

	select {
	case got := <-replies:
		fmt.Fprintf(os.Stderr, "got:   %s\n", got)
		if got != "echo: "+sent {
			return fmt.Errorf("unexpected reply %q", got)
		}
		fmt.Fprintln(os.Stderr, "echo ok")
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("no reply within 10s")
	case <-conn.Closed:
		return fmt.Errorf("connection lost before reply")
	}
}

// devAnswer is the serving side of the step 0 echo test.
func devAnswer() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := transport.New(transport.DefaultICEServers())
	if err != nil {
		return err
	}
	defer conn.Close()

	fmt.Fprintln(os.Stderr, "--- paste the OFFER here, then press enter ---")
	offer, err := readBlob()
	if err != nil {
		return err
	}

	answer, err := conn.Answer(ctx, offer)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "--- copy this ANSWER back into `ratatoskr dev-offer` ---")
	fmt.Println(answer)

	if err := waitCtrl(conn, 30*time.Second); err != nil {
		return err
	}
	reportPath(conn)

	done := make(chan error, 1)
	conn.Ctrl.OnMessage(func(m webrtc.DataChannelMessage) {
		fmt.Fprintf(os.Stderr, "got:   %s\n", m.Data)
		done <- conn.Ctrl.SendText("echo: " + string(m.Data))
	})

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("echo: %w", err)
		}
		fmt.Fprintln(os.Stderr, "echo ok")
		// Give SCTP a moment to flush before the deferred Close.
		time.Sleep(500 * time.Millisecond)
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("no message within 30s")
	case <-conn.Closed:
		return fmt.Errorf("connection lost before any message")
	}
}

func waitCtrl(conn *transport.Conn, d time.Duration) error {
	select {
	case <-conn.CtrlReady:
		return nil
	case <-conn.Closed:
		return fmt.Errorf("connection failed before the control channel opened")
	case <-time.After(d):
		return fmt.Errorf("control channel did not open within %s", d)
	}
}

func reportPath(conn *transport.Conn) {
	kind, desc := conn.Path()
	fmt.Fprintf(os.Stderr, "connected (%s): %s\n", kind, desc)
}

// readBlob reads one base64 SDP line from stdin.
func readBlob() (string, error) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			return line, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return "", fmt.Errorf("no input on stdin")
}
