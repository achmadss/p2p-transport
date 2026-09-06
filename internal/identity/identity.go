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
func (i *Identity) Fingerprint() string { return Short(i.id.String()) }

// Short is Fingerprint's rule, for peer ids that are not ours.
func Short(id string) string {
	if len(id) < 8 {
		return id
	}
	t := id[len(id)-8:]
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

func create(path string) (*Identity, error) {
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	b, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encode key: %w", err)
	}
	if err := os.WriteFile(path, b, keyPerm); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("derive peer id: %w", err)
	}
	return &Identity{priv: priv, id: id}, nil
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
