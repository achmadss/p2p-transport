package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/achmadss/p2p-transport/internal/config"
)

// Metering.
//
// Every relay already says what each subject moved, once a period,
// because the allowance loop cannot divide a rate without it. Adding
// those up is the whole of the meter: no scrape, no second port on the
// relays and no metrics library, because the numbers already arrive and
// they arrive at the period the operator chose rather than at a scrape
// interval nobody tuned.

const usageFile = "usage.json"

// usageFlush is how often the counters reach the disk. A crash costs at
// most this much counting, which is the same bargain a relay already
// makes: it holds a period of counts that die with it.
const usageFlush = 30 * time.Second

// counter is one subject's meter.
//
// Bytes only ever grows, and Since travels with it, so an application
// reading the difference between two calls can tell "moved nothing"
// from "the counter went back to zero", which is the one thing a bare
// number cannot say.
type counter struct {
	Bytes int64     `json:"bytes"`
	Since time.Time `json:"since"`
}

// meter is what every subject has moved through the whole fleet.
//
// ponytail: held in memory, written whole on a timer. One file and one
// mutex, like the subject store beside it, and the same day it stops
// being enough is the day that store becomes a database.
type meter struct {
	path  string
	mu    sync.Mutex
	used  map[string]*counter
	dirty bool
}

func openMeter(dir string) (*meter, error) {
	d, err := config.Dir(dir)
	if err != nil {
		return nil, err
	}
	m := &meter{path: filepath.Join(d, usageFile), used: map[string]*counter{}}
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", m.path, err)
	}
	if err := json.Unmarshal(b, &m.used); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", m.path, err)
	}
	return m, nil
}

// counterFor is a subject's counter, started now if this is the first
// anyone has heard of it. A read starts it as well as a byte does, which
// is what keeps Since stable: a caller subtracting two readings must be
// able to trust that Since moves only when the counting really began
// again. The caller holds the lock.
func (m *meter) counterFor(subject string) *counter {
	c, ok := m.used[subject]
	if !ok {
		c = &counter{Since: time.Now().UTC().Truncate(time.Second)}
		m.used[subject], m.dirty = c, true
	}
	return c
}

// add counts bytes against a subject. It counts what a relay reports
// whether or not the coordinator still has a share for that subject:
// bytes that crossed a relay crossed it, and a placement that has just
// expired does not make them free.
func (m *meter) add(subject string, n int64) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counterFor(subject).Bytes += n
	m.dirty = true
}

// read is one subject's total, zero for a subject that has moved
// nothing yet.
func (m *meter) read(subject string) counter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.counterFor(subject)
}

// forget drops a subject's counter, which happens when the subject
// itself is deleted. Keeping it would grow the file for ever on a fleet
// whose subjects come and go, and whoever deleted the subject had every
// chance to read the number first.
func (m *meter) forget(subject string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.used[subject]; !ok {
		return
	}
	delete(m.used, subject)
	m.dirty = true
}

// flush writes the counters if any have moved since the last write.
func (m *meter) flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirty {
		return nil
	}
	if err := config.WriteJSON(m.path, m.used); err != nil {
		return err
	}
	m.dirty = false
	return nil
}

// serve flushes on a timer until done. The last flush is the caller's,
// on the way out of main, because a goroutine woken by a closing
// channel races the process leaving.
func (m *meter) serve(done <-chan struct{}) {
	tick := time.NewTicker(usageFlush)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			if err := m.flush(); err != nil {
				fmt.Fprintln(os.Stderr, "usage not written:", err)
			}
		}
	}
}
