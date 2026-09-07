package main

import (
	"testing"
	"time"
)

// The walk-down this drives writes a plain number of seconds, and a zero
// means QUIC goes first. Both were silently read as fifteen seconds once.
func TestBareWindow(t *testing.T) {
	for _, c := range []struct {
		set  string
		want time.Duration
	}{
		{"", 15 * time.Second},
		{"5", 5 * time.Second},
		{"0", 0},
		{"3s", 3 * time.Second},
		{"nonsense", 15 * time.Second},
	} {
		t.Setenv("RATATOSKR_PUNCH_RAW", c.set)
		if got := bareWindow(); got != c.want {
			t.Errorf("RATATOSKR_PUNCH_RAW=%q: got %v, want %v", c.set, got, c.want)
		}
	}
}
