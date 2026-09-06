// Package config owns where Ratatoskr keeps its state on each operating
// system, and the one file that describes what this machine shares and
// who it trusts.
//
// Nothing here imports libp2p. The config is a plain description of
// intent; turning a peer id string into a live connection is the
// transport's job, a layer above.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// EnvDir overrides the config directory. It exists so tests can run
// against a temporary directory, and so a portable install can keep its
// state beside the binary.
const EnvDir = "RATATOSKR_CONFIG_DIR"

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Dir returns the config directory, creating it if it does not exist.
//
// The directory is 0700 because identity.key lives in it. A 0600 key
// inside a 0755 directory is still readable by anyone who can list the
// directory on some systems, so the directory carries the same
// restriction as the file.
func Dir() (string, error) {
	if d := os.Getenv(EnvDir); d != "" {
		return d, ensureDir(d)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no config directory on this system: %w", err)
	}
	d := filepath.Join(base, "ratatoskr")
	return d, ensureDir(d)
}

func ensureDir(d string) error {
	if err := os.MkdirAll(d, dirPerm); err != nil {
		return fmt.Errorf("create %s: %w", d, err)
	}
	// MkdirAll leaves an existing directory's mode alone, so tighten it.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(d, dirPerm); err != nil {
			return fmt.Errorf("secure %s: %w", d, err)
		}
	}
	return nil
}

// Path returns the full path of a file inside the config directory.
func Path(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// Mode is how much a share allows.
type Mode string

const (
	ReadOnly  Mode = "ro"
	ReadWrite Mode = "rw"
)

func (m Mode) valid() bool { return m == ReadOnly || m == ReadWrite }

// A Share is one folder this machine offers, under a short name. The
// name is what appears in a path like home:/Documents; the real
// location on disk is never shown to a remote peer.
type Share struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mode Mode   `json:"mode"`
}

// A Peer is a peer id this machine will serve without a mimir grant.
// This is the local, serverless authorisation path of PLAN.md §5.2.
type Peer struct {
	ID    string    `json:"id"`
	Name  string    `json:"name,omitempty"`
	Added time.Time `json:"added"`
}

// Config is the whole of config.json.
//
// Trusted and Aliases point in opposite directions and are deliberately
// separate: Trusted answers "who may read my files", Aliases answers
// "what do I call the machines I connect to". A device is commonly in
// one and not the other.
type Config struct {
	Version int               `json:"v"`
	Shares  []Share           `json:"shares"`
	Trusted []Peer            `json:"trusted"`
	Aliases map[string]string `json:"aliases"` // alias -> peer id
}

// Default is what a first run writes: nothing shared, nobody trusted.
// Sharing is opted into, never inherited from a default.
func Default() *Config {
	return &Config{Version: 1, Shares: []Share{}, Trusted: []Peer{}, Aliases: map[string]string{}}
}

const fileName = "config.json"

// Load reads config.json, writing a default one if it is missing.
func Load() (*Config, error) {
	p, err := Path(fileName)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		c := Default()
		if err := c.Save(); err != nil {
			return nil, err
		}
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}

	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", p, err)
	}
	if c.Aliases == nil {
		c.Aliases = map[string]string{}
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &c, nil
}

// validate rejects a config rather than silently correcting it. A share
// with an unreadable mode is a question for the user, not something to
// guess at — guessing here would mean guessing about access.
func (c *Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unknown version %d", c.Version)
	}
	seen := map[string]bool{}
	for _, s := range c.Shares {
		switch {
		case s.Name == "":
			return fmt.Errorf("a share has no name")
		case seen[s.Name]:
			return fmt.Errorf("two shares are both named %q", s.Name)
		case !filepath.IsAbs(s.Path):
			return fmt.Errorf("share %q: path %q is not absolute", s.Name, s.Path)
		case !s.Mode.valid():
			return fmt.Errorf("share %q: mode %q is not ro or rw", s.Name, s.Mode)
		}
		seen[s.Name] = true
	}
	for _, p := range c.Trusted {
		if p.ID == "" {
			return fmt.Errorf("a trusted entry has no peer id")
		}
	}
	return nil
}

// Save writes config.json atomically: a temp file in the same directory,
// fsynced, then renamed over the old one. A half-written config would
// mean a machine that will not start.
func (c *Config) Save() error {
	p, err := Path(fileName)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')
	return writeFileAtomic(p, b, filePerm)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename below succeeds

	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("set permissions on %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return syncDir(dir)
}

// syncDir makes the rename itself durable. Without it a crash can leave
// the directory entry pointing at the old file.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil && runtime.GOOS != "windows" {
		// Windows cannot fsync a directory handle opened this way, and
		// reports an error for it. Elsewhere the failure is real.
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}
