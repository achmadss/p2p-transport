// Package transport wraps Pion WebRTC. It owns peer connection setup, the
// control DataChannel, and the answer to "did this end up direct or relayed".
//
// Nothing above this package should import pion directly.
package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"
)

// CtrlLabel is the DataChannel that carries JSON requests and replies.
// File bytes travel on separate "xfer-<id>" channels, added in step 4.
const CtrlLabel = "ctrl"

// DefaultICEServers is the STUN set used until heimdall hands out its own.
func DefaultICEServers() []webrtc.ICEServer {
	return []webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
		{URLs: []string{"stun:stun.cloudflare.com:3478"}},
	}
}

// Path is how the two peers ended up talking to each other.
type Path string

const (
	PathUnknown Path = "unknown"
	PathDirect  Path = "direct"
	PathRelay   Path = "relay"
)

// Conn is one WebRTC connection to one peer.
type Conn struct {
	pc *webrtc.PeerConnection

	// Ctrl is closed-over by CtrlReady: read it only after CtrlReady fires.
	Ctrl *webrtc.DataChannel

	// CtrlReady closes once Ctrl is open and usable.
	CtrlReady chan struct{}

	// Closed closes when the peer connection fails or disconnects.
	Closed chan struct{}

	ctrlOnce   sync.Once
	closedOnce sync.Once
}

// New builds a peer connection that has not been offered or answered yet.
func New(ice []webrtc.ICEServer) (*Conn, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, fmt.Errorf("new peer connection: %w", err)
	}

	c := &Conn{pc: pc, CtrlReady: make(chan struct{}), Closed: make(chan struct{})}

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateClosed:
			c.closedOnce.Do(func() { close(c.Closed) })
		}
	})

	// The answering side receives the channel rather than creating it.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != CtrlLabel {
			return
		}
		c.Ctrl = dc
		dc.OnOpen(func() { c.ctrlOnce.Do(func() { close(c.CtrlReady) }) })
	})

	return c, nil
}

// Offer creates the control channel and returns an encoded SDP offer.
//
// ICE gathering runs to completion before returning, so the offer carries
// every candidate. That keeps step 0 to a single paste in each direction.
// Trickle ICE arrives with heimdall in step 1.
func (c *Conn) Offer(ctx context.Context) (string, error) {
	dc, err := c.pc.CreateDataChannel(CtrlLabel, nil)
	if err != nil {
		return "", fmt.Errorf("create %s channel: %w", CtrlLabel, err)
	}
	c.Ctrl = dc
	dc.OnOpen(func() { c.ctrlOnce.Do(func() { close(c.CtrlReady) }) })

	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("create offer: %w", err)
	}
	return c.localAfterGathering(ctx, offer)
}

// Answer consumes an encoded offer and returns an encoded answer.
func (c *Conn) Answer(ctx context.Context, encodedOffer string) (string, error) {
	offer, err := decode(encodedOffer)
	if err != nil {
		return "", fmt.Errorf("decode offer: %w", err)
	}
	if err := c.pc.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("set remote offer: %w", err)
	}

	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}
	return c.localAfterGathering(ctx, answer)
}

// Accept consumes the encoded answer on the offering side.
func (c *Conn) Accept(encodedAnswer string) error {
	answer, err := decode(encodedAnswer)
	if err != nil {
		return fmt.Errorf("decode answer: %w", err)
	}
	if err := c.pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}
	return nil
}

// Path reports how the connection was actually established, and a human
// readable description of the winning candidate pair.
//
// This must come from the real selected pair, never from a guess, because the
// relay rate is the number that decides what this system costs to run.
func (c *Conn) Path() (Path, string) {
	sctp := c.pc.SCTP()
	if sctp == nil {
		return PathUnknown, "no sctp transport"
	}
	dtls := sctp.Transport()
	if dtls == nil {
		return PathUnknown, "no dtls transport"
	}
	ice := dtls.ICETransport()
	if ice == nil {
		return PathUnknown, "no ice transport"
	}
	pair, err := ice.GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return PathUnknown, "no selected candidate pair"
	}

	kind := PathDirect
	if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		kind = PathRelay
	}
	desc := fmt.Sprintf("local %s %s:%d  <->  remote %s %s:%d",
		pair.Local.Typ, pair.Local.Address, pair.Local.Port,
		pair.Remote.Typ, pair.Remote.Address, pair.Remote.Port)
	return kind, desc
}

func (c *Conn) Close() error { return c.pc.Close() }

// localAfterGathering sets the local description and waits for ICE gathering
// to finish, then returns the encoded full description.
func (c *Conn) localAfterGathering(ctx context.Context, sd webrtc.SessionDescription) (string, error) {
	done := webrtc.GatheringCompletePromise(c.pc)
	if err := c.pc.SetLocalDescription(sd); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		return "", fmt.Errorf("ice gathering: %w", ctx.Err())
	}

	local := c.pc.LocalDescription()
	if local == nil {
		return "", errors.New("no local description after gathering")
	}
	return encode(*local), nil
}

func encode(sd webrtc.SessionDescription) string {
	b, err := json.Marshal(sd)
	if err != nil {
		// SessionDescription is two strings; marshalling it cannot fail.
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decode(s string) (webrtc.SessionDescription, error) {
	var sd webrtc.SessionDescription
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return sd, err
	}
	err = json.Unmarshal(b, &sd)
	return sd, err
}
