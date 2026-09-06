package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/achmadss/ratatoskr/internal/config"
)

// isolate points the config package at a fresh directory, so a test
// never reads or writes the developer's real identity.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvDir, dir)
	return dir
}

// The whole point of a stored key: the peer id survives a restart. If it
// did not, every trust list naming this machine would break on reboot.
func TestIDStableAcrossRestarts(t *testing.T) {
	isolate(t)

	first, err := LoadOrCreate()
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := LoadOrCreate()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.ID() != second.ID() {
		t.Fatalf("peer id changed across restarts: %s then %s", first.ID(), second.ID())
	}
}

func TestSeparateDirsGetSeparateIdentities(t *testing.T) {
	isolate(t)
	a, err := LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	isolate(t)
	b, err := LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() == b.ID() {
		t.Fatal("two independent machines generated the same peer id")
	}
}

func TestKeyFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are synthetic on Windows")
	}
	dir := isolate(t)
	if _, err := LoadOrCreate(); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("identity.key is mode %04o, want 0600", got)
	}
}

// A key another account can read is a key that can be stolen, so the
// agent must refuse to start rather than serve files under it.
func TestRefusesLooseKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are synthetic on Windows")
	}
	dir := isolate(t)
	if _, err := LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)

	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreate(); err == nil {
			t.Fatalf("mode %04o was accepted; it must be refused", mode)
		}
	}
}

// Corruption must be loud. Quietly generating a replacement would change
// the peer id and break every peer that trusts this machine.
func TestCorruptKeyFailsLoudly(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, FileName)

	cases := map[string][]byte{
		"empty":  {},
		"text":   []byte("this is not a key"),
		"random": {0x08, 0x01, 0x12, 0x40, 0xde, 0xad, 0xbe, 0xef},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := LoadOrCreate()
			if err == nil {
				t.Fatalf("corrupt key was accepted, giving peer id %s", id.ID())
			}
			if !strings.Contains(err.Error(), FileName) {
				t.Errorf("error does not name the offending file: %v", err)
			}
		})
	}
}

func TestFingerprintIsShortAndDerivedFromTheID(t *testing.T) {
	isolate(t)
	id, err := LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	fp := id.Fingerprint()
	if len(fp) != 9 || fp[4] != '-' {
		t.Fatalf("fingerprint %q is not xxxx-xxxx", fp)
	}
	if !strings.HasSuffix(id.ID().String(), strings.ReplaceAll(fp, "-", "")) {
		t.Fatalf("fingerprint %q does not come from peer id %s", fp, id.ID())
	}
}
