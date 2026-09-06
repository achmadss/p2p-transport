// Package identity owns this machine's long-lived keypair.
//
// The key is generated once and never leaves the machine. The peer id is
// a multihash of the public key, so it proves itself: a Noise handshake
// cannot succeed against a peer that does not hold the matching private
// key. This is deliberately not a hardware fingerprint — those break
// when a VM is cloned or a disk is swapped, and they leak facts about
// the machine. PLAN.md §5.1.
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/achmadss/ratatoskr/internal/config"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// FileName is the key file inside the config directory.
const FileName = "identity.key"

const keyPerm = 0o600

// Identity is a loaded keypair and the peer id derived from it.
type Identity struct {
	priv crypto.PrivKey
	id   peer.ID
}

// PrivateKey is handed to libp2p when the host starts. It is never
// written anywhere but identity.key and never sent over any wire.
func (i *Identity) PrivateKey() crypto.PrivKey { return i.priv }

// ID is the full peer id. Diagnostics and protocol only — SPEC.md §30.4
// keeps it out of ordinary user-facing output, where Fingerprint goes
// instead.
func (i *Identity) ID() peer.ID { return i.id }

// Fingerprint is the short form for a person to read aloud or compare on
// screen: the last eight characters of the peer id, in two groups.
//
// It is for display only. Nothing authorises on a fingerprint — the full
// id is what the trust list stores and what the handshake proves. Eight
// characters would be far too few to resist a deliberate collision, and
// it is never asked to.
func (i *Identity) Fingerprint() string {
	s := i.id.String()
	if len(s) < 8 {
		return s
	}
	t := s[len(s)-8:]
	return t[:4] + "-" + t[4:]
}

// LoadOrCreate returns this machine's identity, generating one on first
// run. Later runs return the same key, so the peer id is stable.
func LoadOrCreate() (*Identity, error) {
	path, err := config.Path(FileName)
	if err != nil {
		return nil, err
	}

	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := checkPerm(path); err != nil {
			return nil, err
		}
		return fromBytes(b, path)
	case errors.Is(err, os.ErrNotExist):
		return create(path)
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
}

// Load returns the identity but refuses to create one. Commands that
// must not silently mint a new identity for a mistyped config directory
// use this.
func Load() (*Identity, error) {
	path, err := config.Path(FileName)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no identity yet: run `ratatoskr id` to create one")
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := checkPerm(path); err != nil {
		return nil, err
	}
	return fromBytes(b, path)
}

func create(path string) (*Identity, error) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	b, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encode key: %w", err)
	}
	if err := writeKey(path, b); err != nil {
		return nil, err
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("derive peer id: %w", err)
	}
	return &Identity{priv: priv, id: id}, nil
}

// writeKey creates the file with O_EXCL so a concurrent first run cannot
// have two processes each generate a key and one overwrite the other.
// Losing that race would change the peer id under a running agent.
func writeKey(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyPerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("another process created %s at the same time; try again", path)
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dir, err)
	}
	return nil
}

// fromBytes fails loudly. A key file that does not parse is either
// corrupt or someone else's, and both cases change the peer id — which
// would silently break every trust list that names this machine.
func fromBytes(b []byte, path string) (*Identity, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("%s is empty: delete it to generate a new identity, but every peer that trusts this machine will need to trust the new id", path)
	}
	priv, err := crypto.UnmarshalPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid key: %w", path, err)
	}
	if priv.Type() != crypto.Ed25519 {
		return nil, fmt.Errorf("%s holds a %s key, but Ratatoskr uses Ed25519", path, priv.Type())
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("derive peer id from %s: %w", path, err)
	}
	return &Identity{priv: priv, id: id}, nil
}

// checkPerm refuses to start when the private key is readable by anyone
// but its owner. Continuing would mean serving files under an identity
// another account on this machine can steal.
//
// Windows has no meaningful Unix mode bits — os.Stat reports a synthetic
// one — so the check is skipped there. Its access control lives in ACLs,
// which is a separate piece of work and is noted as such.
func checkPerm(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %04o): run `chmod 600 %s`", path, mode, path)
	}
	return nil
}
