package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	return dir
}

func TestFirstRunWritesADefaultConfig(t *testing.T) {
	dir := isolate(t)
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Relays) != 0 {
		t.Fatal("a first run named a relay; LAN only is the default")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	isolate(t)
	c := Default()
	c.Relays = append(c.Relays, "/ip4/203.0.113.7/udp/4001/quic-v1/p2p/12D3KooWfake")
	if err := c.Save(""); err != nil {
		t.Fatal(err)
	}

	got, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Relays) != 1 || got.Relays[0] != c.Relays[0] {
		t.Fatalf("relays did not survive a round trip: %+v", got.Relays)
	}
}

// A config that does not parse is a question for the user rather than
// something to guess at: guessing would mean starting under a relay
// nobody named.
func TestBadConfigIsRefused(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, "config.json")

	for name, body := range map[string]string{
		"unknown version": `{"v":99}`,
		"not json":        `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(""); err == nil {
				t.Fatal("accepted a config it should have refused")
			}
		})
	}
}

// An explicit directory wins over the environment, because an
// application embedding this module says where its state lives rather
// than inheriting a variable it did not set.
func TestExplicitDirBeatsTheEnvironment(t *testing.T) {
	isolate(t)
	want := t.TempDir()
	got, err := Dir(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Dir(%q) = %q", want, got)
	}
}

func TestDirIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are synthetic on Windows")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "loose")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDir, sub)

	if _, err := Dir(""); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Fatalf("config dir is mode %04o, want 0700; identity.key lives here", got)
	}
}
