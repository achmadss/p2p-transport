package main

import (
	"context"

	"github.com/achmadss/p2p-transport/internal/shape"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// shapedHost hands the relay streams that are rate limited, and leaves
// the rest of this machine alone.
//
// Every byte a relay forwards arrives through one of two calls: the
// handler it registers for machines dialling in, and the stream it opens
// to the machine being dialled. Wrapping those two is therefore the
// whole byte path, with nothing else on this host affected — the
// coordinator link and the address protocols run on the real host and
// are not shaped.
//
// The alternative was forking the relay to add a hook inside its copy
// loop, which is a thousand lines of somebody else's code to keep in
// step with upstream. This is twenty lines and shapes the same bytes.
type shapedHost struct {
	host.Host
	limits *shape.Limits
}

func (h shapedHost) SetStreamHandler(p protocol.ID, fn network.StreamHandler) {
	h.Host.SetStreamHandler(p, func(s network.Stream) { fn(h.limits.Stream(s)) })
}

func (h shapedHost) NewStream(ctx context.Context, p peer.ID, protos ...protocol.ID) (network.Stream, error) {
	s, err := h.Host.NewStream(ctx, p, protos...)
	if err != nil {
		return nil, err
	}
	return h.limits.Stream(s), nil
}
