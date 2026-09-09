// Package config owns where this machine keeps its state on each
// operating system, and the one file that names the relays it may use.
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
// restriction as the file. MkdirAll leaves an existing directory's mode
// alone, hence the Chmod.
func Dir() (string, error) {
	d := os.Getenv(EnvDir)
	if d == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("no config directory on this system: %w", err)
		}
		d = filepath.Join(base, "ratatoskr")
	}
	if err := os.MkdirAll(d, dirPerm); err != nil {
		return "", fmt.Errorf("create %s: %w", d, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(d, dirPerm); err != nil {
			return "", fmt.Errorf("secure %s: %w", d, err)
		}
	}
	return d, nil
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

// A Peer is a peer id an application above chose to trust.
// Nothing in this repository reads these: authorisation is the
// application's, not layer 4's, and step 4 of TODO.md removes them.
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

	// Relays are heimdall nodes, as full multiaddrs. Empty means LAN
	// only, which is a complete and supported way to run.
	Relays []string `json:"relays"`
}

// Default is what a first run writes: nothing shared, nobody trusted.
// Sharing is opted into, never inherited from a default.
func Default() *Config {
	return &Config{Version: 1, Shares: []Share{}, Trusted: []Peer{}, Aliases: map[string]string{}, Relays: []string{}}
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

// Save writes config.json.
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
	return os.WriteFile(p, b, filePerm)
}
