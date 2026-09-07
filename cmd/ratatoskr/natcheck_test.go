package main

import (
	"net"
	"strings"
	"testing"

	"github.com/achmadss/p2p-transport/internal/stun"
)

// The carrier this was written for gives the very next external port to
// the next destination, and three lookalike reflectors all saw the same
// one. A verdict drawn from them alone said hole punching can work,
// which cost an evening; the fourth observer is what makes it false.
func TestVerdictCatchesPerDestinationPort(t *testing.T) {
	at := func(ip, mapped string) stun.Reflection {
		return stun.Reflection{Server: ip, IP: net.ParseIP(ip), Mapped: mapped}
	}
	agree := []stun.Reflection{
		at("1.1.1.1", "182.6.166.95:9238"),
		at("2.2.2.2", "182.6.166.95:9238"),
	}
	if got := verdict(agree, 9238); !strings.Contains(got, "Endpoint-Independent") {
		t.Errorf("agreeing observers: got %q", got)
	}

	split := append(agree, at("3.3.3.3", "182.6.166.95:9239"))
	got := verdict(split, 9238)
	if !strings.Contains(got, "Address-Dependent") || !strings.Contains(got, "cannot work") {
		t.Errorf("one dissenting observer must sink the verdict: got %q", got)
	}
}
