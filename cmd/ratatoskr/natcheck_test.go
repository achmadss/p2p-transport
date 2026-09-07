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

// The numbers are this carrier's, on 7 Sep 2026: a relay connection
// opened at startup was still observed as 35749 minutes later, while a
// reflector asked on the same socket seconds before a punch answered
// 61482. One round cannot tell those apart from a stable NAT, because
// every observer asked inside one second agrees. Two rounds can.
func TestDriftSeparatesHeldMappingFromFreshDestination(t *testing.T) {
	at := func(server, mapped string) stun.Reflection {
		return stun.Reflection{Server: server, Mapped: mapped}
	}
	round1 := []stun.Reflection{at("a", "182.6.166.95:35749"), at("b", "182.6.166.95:35749")}

	steady := drift(round1, at("a", "182.6.166.95:35749"), at("c", "182.6.166.95:35749"))
	if !strings.Contains(steady, "Hole punching can work") {
		t.Errorf("a NAT that does not move must pass: got %q", steady)
	}

	moved := drift(round1, at("a", "182.6.166.95:35749"), at("c", "182.6.166.95:61482"))
	if !strings.Contains(moved, "defeats publishing an address") {
		t.Errorf("a held mapping beside a moved fresh one is the failing class: got %q", moved)
	}

	gone := drift(round1, at("a", "182.6.166.95:61482"), at("c", "182.6.166.95:61483"))
	if !strings.Contains(gone, "did not survive") {
		t.Errorf("a mapping that expired is a third answer: got %q", gone)
	}
}
