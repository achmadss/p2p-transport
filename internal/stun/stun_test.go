package stun

import "net"

import "testing"

func ref(server, ip, mapped string) Reflection {
	return Reflection{Server: server, IP: net.ParseIP(ip), Mapped: mapped}
}

// The whole point of the guard is that it says no more often than yes.
// Advertising a wrong address is worse than advertising none: the far
// peer spends its punch window dialling somewhere nothing listens.
func TestPublicFromRefusesEverythingItCannotProve(t *testing.T) {
	good := []Reflection{
		ref("a", "1.1.1.1", "203.0.113.7:4242"),
		ref("b", "2.2.2.2", "203.0.113.7:4242"),
	}
	if ip, ok := publicFrom(good, 4242); !ok || ip != "203.0.113.7" {
		t.Fatalf("endpoint-independent and port-preserving should pass, got %q %v", ip, ok)
	}

	for name, tc := range map[string]struct {
		got  []Reflection
		port int
	}{
		"one reflector cannot tell the mapping apart": {good[:1], 4242},
		"ports differ between reflectors": {[]Reflection{
			ref("a", "1.1.1.1", "203.0.113.7:4242"),
			ref("b", "2.2.2.2", "203.0.113.7:5555"),
		}, 4242},
		"port renumbered, so QUIC's port is unknown": {good, 5555},
		"carrier-grade NAT address is not public": {[]Reflection{
			ref("a", "1.1.1.1", "100.90.1.2:4242"),
			ref("b", "2.2.2.2", "100.90.1.2:4242"),
		}, 4242},
		"nothing answered": {nil, 4242},
	} {
		if ip, ok := publicFrom(tc.got, tc.port); ok {
			t.Errorf("%s: expected refusal, advertised %q", name, ip)
		}
	}
}
