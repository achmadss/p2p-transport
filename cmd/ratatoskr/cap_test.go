package main

import (
	"io"
	"testing"
)

// The cap must refuse rather than truncate: a short copy reported as a
// success is how a half-written file gets mistaken for a whole one.
func TestCappedRefusesRatherThanTruncates(t *testing.T) {
	c := &capped{w: io.Discard, left: 10}

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
