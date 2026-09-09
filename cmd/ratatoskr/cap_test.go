package main

import (
	"io"
	"sync/atomic"
	"testing"
)

// The cap must refuse rather than truncate: a short copy reported as a
// success is how a half-written file gets mistaken for a whole one.
func TestCappedRefusesRatherThanTruncates(t *testing.T) {
	left := new(atomic.Int64)
	left.Store(10)
	c := &capped{w: io.Discard, left: left}

	if _, err := c.Write(make([]byte, 6)); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
	n, err := c.Write(make([]byte, 6))
	if err == nil {
		t.Fatal("over the cap: wrote through, want an error")
	}
	if n != 0 {
		t.Fatalf("over the cap: reported %d bytes written, want 0", n)
	}
}

// The allowance is shared by every stream on one connection. A chunked
// transfer is many streams, and a cap that started over with each of
// them would let a relayed transfer spend the cap once per chunk.
func TestCappedAllowanceIsSharedAcrossStreams(t *testing.T) {
	left := new(atomic.Int64)
	left.Store(10)
	first := &capped{w: io.Discard, left: left}
	second := &capped{w: io.Discard, left: left}

	if _, err := first.Write(make([]byte, 6)); err != nil {
		t.Fatalf("first chunk, under the cap: %v", err)
	}
	if _, err := second.Write(make([]byte, 6)); err == nil {
		t.Fatal("second chunk spent the cap over again")
	}
}
