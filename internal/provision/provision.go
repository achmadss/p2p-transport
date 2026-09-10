// Package provision creates and destroys the machines relays run on.
//
// The interface here is what the scaling loop needs and nothing else. A
// provider that does not fit adapts to this rather than the other way
// round, because which provider a fleet buys from changes for reasons —
// price, an outage, a region — that have nothing to do with the loop.
package provision

import (
	"context"
	"errors"
	"time"
)

// State is how far along a machine is. Three, because the loop acts on
// three: wait for it, use it, forget it.
type State string

const (
	Starting State = "starting" // asked for, not answering yet
	Running  State = "running"
	Gone     State = "gone" // destroyed, or alive and never going to work
)

// A Machine is one VPS, capable of running one relay.
type Machine struct {
	ID      string   // the provider's id, opaque here
	Key     string   // the idempotency key that asked for it
	Addrs   []string // public addresses; may be empty while starting
	Size    string
	Region  string
	State   State
	Created time.Time // zero when the provider does not say unambiguously
}

// A Spec is a machine the loop wants to exist.
type Spec struct {
	Key      string // two Creates with one Key make one machine
	Region   string // from Regions
	Size     string // from Sizes
	Image    string // the pre-baked relay image
	UserData string // cloud-init: the coordinator's address and peer id
}

// A Size is what the loop places against. Bandwidth is the whole point
// of the type: bytes per second, per direction, that a relay of this
// size can really forward. It is rarely the number a provider prints,
// and converting is the adapter's job so that no caller has to know.
type Size struct {
	ID        string
	Bandwidth int64
	Monthly   int64 // minor units, for choosing between providers
	Regions   []string
}

// A Region is somewhere machines can be created.
type Region struct {
	ID   string
	Name string
}

// A Provisioner is one cloud account.
//
// Create is idempotent on Spec.Key, and it does not wait for the machine
// to be usable: a relay is usable when it registers and heartbeats,
// which is a fact about the relay rather than about the provider, and
// every provider means something different by "active". Destroy is
// idempotent, and already gone is success. List is the truth; whatever
// the coordinator remembers is a cache of it, so reconcile against this
// or a crash at the wrong moment leaks a machine that bills for ever.
type Provisioner interface {
	Create(ctx context.Context, s Spec) (Machine, error)
	Destroy(ctx context.Context, id string) error
	List(ctx context.Context) ([]Machine, error)
	Sizes(ctx context.Context) ([]Size, error)
	Regions(ctx context.Context) ([]Region, error)
}

// The three failures the loop acts on differently. Anything else is
// fatal and the loop must stop: one that retries a malformed request for
// ever is worse than one that gives up and says so.
var (
	ErrNoCapacity = errors.New("provider has no capacity here") // try another region
	ErrQuota      = errors.New("account limit reached")         // stop scaling up
	ErrTransient  = errors.New("retry")
)
