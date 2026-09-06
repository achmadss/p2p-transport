package config

import (
	"testing"
	"time"
)

func TestEnvFallsBackToDefaults(t *testing.T) {
	for _, bad := range []string{"", "nonsense", "0", "-5"} {
		t.Setenv("RT_TEST", bad)
		if got := Int("RT_TEST", 7); got != 7 {
			t.Errorf("Int(%q) = %d, want the default 7", bad, got)
		}
		if got := Duration("RT_TEST", time.Second); got != time.Second {
			t.Errorf("Duration(%q) = %v, want the default 1s", bad, got)
		}
		if got := Bytes("RT_TEST", 99); got != 99 {
			t.Errorf("Bytes(%q) = %d, want the default 99", bad, got)
		}
	}
}

func TestEnvReadsValues(t *testing.T) {
	t.Setenv("RT_TEST", "12")
	if got := Int("RT_TEST", 7); got != 12 {
		t.Errorf("Int = %d, want 12", got)
	}
	t.Setenv("RT_TEST", "90s")
	if got := Duration("RT_TEST", time.Second); got != 90*time.Second {
		t.Errorf("Duration = %v, want 90s", got)
	}
	for in, want := range map[string]int64{"512": 512, "4K": 4 << 10, "8m": 8 << 20, "2G": 2 << 30} {
		t.Setenv("RT_TEST", in)
		if got := Bytes("RT_TEST", 1); got != want {
			t.Errorf("Bytes(%q) = %d, want %d", in, got, want)
		}
	}
	t.Setenv("RT_TEST", " a , ,b ")
	got := List("RT_TEST")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("List = %q, want [a b]", got)
	}
}
