// Package config owns where this machine keeps its state on each
// operating system, and the file naming the relays it may use.
//
// Every entry point takes a directory, with "" meaning the environment
// override or the per-OS default, so a caller says where its state lives
// rather than inheriting a process-wide variable it did not set.
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
// It is 0700 because identity.key lives in it: on some systems a 0600
// file in a 0755 directory is still readable by anyone who can list it.
// MkdirAll leaves an existing directory's mode alone, hence the Chmod.
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

// Config is the whole of config.json. It belongs to the command-line
// tools: transport.New is given its relays in code, and an application
// embedding the transport keeps its own settings wherever it likes.
type Config struct {
	Version int `json:"v"`

	// Relays are heimdall nodes, as full multiaddrs. Empty means LAN
	// only, which is a complete and supported way to run.
	//
	// Set these or Coordinator, not both. These name the relay to use
	// and never change; a coordinator hands one out and can change it.
	Relays []string `json:"relays"`

	// Coordinator is a bifrost node, as full multiaddrs — several for
	// the same one are fine. A machine with a coordinator is told which
	// relay to use and keeps asking, so the answer can change while it
	// runs. Empty means the Relays above, or LAN only.
	Coordinator []string `json:"coordinator,omitempty"`
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
	return WriteJSON(p, c)
}

// WriteJSON writes v to path as indented JSON, and either the whole new
// file is there afterwards or the whole old one still is.
//
// It writes a temporary file beside the target and renames it, because a
// rename is the one write a crash cannot leave half done. Written in
// place instead, a process killed mid-write leaves a truncated file, and
// the next start refuses to parse it — which turns losing the last few
// seconds of state into losing all of it.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	// Cleans up after any failure below, and is a no-op once the rename
	// has moved the file out from under this name.
	defer os.Remove(tmp.Name())

	// CreateTemp makes the file 0600 already; this says so out loud, so
	// the permission does not depend on that staying true.
	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	// Synced before the rename, or the rename can land before the bytes
	// do and the atomic write is only atomic on paper.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
