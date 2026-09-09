package transport

import (
	"github.com/libp2p/go-libp2p/core/peer"
)

// Telling a caller where a peer is, as it changes.
//
// The connection hooks already fire on every connect and disconnect,
// which is every event that can change a path: a punch landing is a new
// connection, a relay dying is a disconnection. So a watch is those
// hooks forwarded, and nothing here polls.

// watcher is one caller's view of one peer. The channel holds the latest
// path only — a reader that falls behind wants where the peer is now,
// not the sequence it took to get there.
type watcher struct {
	ch   chan Path
	last Path
}

// watch registers a watcher and returns its channel with the cancel that
// ends it. The current path is delivered before the channel is returned,
// so a caller never has to ask for the state it is watching from.
//
// The order is register, then read the path — never the other way round.
// Reading first leaves a window in which a connection opens, is published
// to a watcher not yet in the map, and the caller is left holding a path
// that will not be corrected until the next change. So the channel is
// seeded with unknown, which never blocks on a buffer of one, and the
// real path goes through publish like any other.
func (t *Host) watch(id peer.ID) (<-chan Path, func()) {
	w := &watcher{ch: make(chan Path, 1), last: PathUnknown}
	w.ch <- PathUnknown

	t.watchMu.Lock()
	if t.watchers == nil {
		t.watchers = make(map[peer.ID][]*watcher)
	}
	t.watchers[id] = append(t.watchers[id], w)
	t.watchMu.Unlock()

	t.publish(id, t.pathTo(id))
	return w.ch, func() { t.unwatch(id, w) }
}

// unwatch removes a watcher and closes its channel. Being in the map is
// what says the channel is still open, so cancelling twice, or after
// Close has ended every watch, does nothing rather than panicking.
func (t *Host) unwatch(id peer.ID, w *watcher) {
	t.watchMu.Lock()
	defer t.watchMu.Unlock()
	ws := t.watchers[id]
	for i, have := range ws {
		if have != w {
			continue
		}
		if t.watchers[id] = append(ws[:i], ws[i+1:]...); len(t.watchers[id]) == 0 {
			delete(t.watchers, id)
		}
		close(w.ch)
		return
	}
}

// publish tells everyone watching a peer where it is now, skipping those
// already told. The send cannot block: the buffer holds one value, any
// stale one is dropped first, and the lock keeps a second publisher out
// in between — so a reader that has stopped reading costs a skipped
// intermediate path rather than a stalled connection hook.
func (t *Host) publish(id peer.ID, p Path) {
	t.watchMu.Lock()
	defer t.watchMu.Unlock()
	for _, w := range t.watchers[id] {
		if w.last == p {
			continue
		}
		w.last = p
		select {
		case <-w.ch:
		default:
		}
		w.ch <- p
	}
}

// closeWatchers ends every watch, so a caller ranging over a channel
// stops when the host does instead of waiting for an event that can no
// longer arrive.
func (t *Host) closeWatchers() {
	t.watchMu.Lock()
	defer t.watchMu.Unlock()
	for _, ws := range t.watchers {
		for _, w := range ws {
			close(w.ch)
		}
	}
	t.watchers = nil
}
