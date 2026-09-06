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
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Shares) != 0 || len(c.Trusted) != 0 {
		t.Fatal("a first run shared or trusted something; both must be opted into")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	isolate(t)
	c := Default()
	c.Shares = append(c.Shares, Share{Name: "docs", Path: absPath(t), Mode: ReadWrite})
	c.Aliases["laptop"] = "12D3KooWfake"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Shares) != 1 || got.Shares[0].Name != "docs" || got.Shares[0].Mode != ReadWrite {
		t.Fatalf("shares did not survive a round trip: %+v", got.Shares)
	}
	if got.Aliases["laptop"] != "12D3KooWfake" {
		t.Fatalf("aliases did not survive a round trip: %+v", got.Aliases)
	}
}

// A bad config is a question for the user. Guessing here would mean
// guessing about who can read what.
func TestBadConfigIsRefused(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, "config.json")
	abs := absPath(t)

	cases := map[string]string{
		"unknown version": `{"v":99}`,
		"unknown mode":    `{"v":1,"shares":[{"name":"a","path":"` + abs + `","mode":"rwx"}]}`,
		"relative path":   `{"v":1,"shares":[{"name":"a","path":"docs","mode":"ro"}]}`,
		"nameless share":  `{"v":1,"shares":[{"name":"","path":"` + abs + `","mode":"ro"}]}`,
		"duplicate name":  `{"v":1,"shares":[{"name":"a","path":"` + abs + `","mode":"ro"},{"name":"a","path":"` + abs + `","mode":"rw"}]}`,
		"nameless peer":   `{"v":1,"trusted":[{"id":""}]}`,
		"not json":        `{`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Fatal("accepted a config it should have refused")
			}
		})
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

	if _, err := Dir(); err != nil {
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

func absPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return `C:\docs`
	}
	return "/docs"
}
