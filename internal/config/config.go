// Package config owns where this machine keeps its state on each
// operating system, and the one file that names the relays it may use.
//
// Nothing here imports libp2p. The config is a plain description of
// intent; turning a peer id string into a live connection is the
// transport's job, a layer above.
//
// Every entry point takes a directory, with "" meaning the environment
// override or the per-OS default. An application embedding this module
// says where its state lives rather than inheriting a choice from a
// process-wide variable it did not set.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
func Dir(override string) (string, error) {
	d := override
	if d == "" {
		d = os.Getenv(EnvDir)
	}
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
func Path(override, name string) (string, error) {
	d, err := Dir(override)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// Config is the whole of config.json.
//
// It is the harness's file, not the library's: `transport.New` is given
// its relays in code. Shares, a trust list and machine aliases used to
// live here and were removed with step 4 — they answer "who may read my
// files" and "what do I call this machine", which are the application's
// questions and belong in the application's own state.
type Config struct {
	Version int `json:"v"`

	// Relays are heimdall nodes, as full multiaddrs. Empty means LAN
	// only, which is a complete and supported way to run.
	Relays []string `json:"relays"`
}

// Default is what a first run writes: no relays, so LAN only.
func Default() *Config { return &Config{Version: 1, Relays: []string{}} }

const fileName = "config.json"

// Load reads config.json, writing a default one if it is missing.
func Load(dir string) (*Config, error) {
	p, err := Path(dir, fileName)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		c := Default()
		if err := c.Save(dir); err != nil {
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
	if c.Version != 1 {
		return nil, fmt.Errorf("%s: unknown version %d", p, c.Version)
	}
	return &c, nil
}

// Save writes config.json.
func (c *Config) Save(dir string) error {
	p, err := Path(dir, fileName)
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
