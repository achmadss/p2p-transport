//go:build live

// Live tests. They talk to the real api.depa.id, and the ones that
// create anything spend real money, so they are behind a build tag and
// behind two environment variables rather than in the normal suite.
//
// Read-only, free:
//
//	DEPA_API_KEY=... DEPA_SOURCE=<instance uuid> \
//	  go test -tags live ./internal/provision/depa/ -v -count=1
//
// The whole round trip, which clones a machine and destroys it again.
// It costs a few minutes of one instance, and it is the only way to find
// out whether Create and Destroy work at all:
//
//	DEPA_API_KEY=... DEPA_SOURCE=<instance uuid> DEPA_LIVE_CLONE=1 \
//	  go test -tags live ./internal/provision/depa/ -v -count=1 -timeout 20m
package depa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/provision"
)

// bandwidth is a placeholder here. Nothing in these tests places against
// it; New only insists that one was given.
const bandwidth = 50 << 20

func live(t *testing.T) *Client {
	t.Helper()
	key, src := os.Getenv("DEPA_API_KEY"), os.Getenv("DEPA_SOURCE")
	if key == "" || src == "" {
		t.Skip("set DEPA_API_KEY and DEPA_SOURCE to run the live tests")
	}
	c, err := New(Options{APIKey: key, Source: src, Bandwidth: bandwidth})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestLiveReadOnly is free. It proves the three calls that only look:
// the account lists, and the source instance answers with the fields
// Sizes and Regions read out of it.
func TestLiveReadOnly(t *testing.T) {
	c := live(t)
	ctx := context.Background()

	got, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("machines this fleet owns: %d", len(got))
	for _, m := range got {
		t.Logf("  %s key=%q state=%s addrs=%v", m.ID, m.Key, m.State, m.Addrs)
	}

	sizes, err := c.Sizes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 {
		t.Fatalf("got %d sizes, want the one the source has", len(sizes))
	}
	s := sizes[0]
	t.Logf("size: %q bandwidth=%d monthly=%d regions=%v", s.ID, s.Bandwidth, s.Monthly, s.Regions)
	switch {
	case s.ID == "//":
		t.Fatal("the source's hardware fields are all empty: the detail call changed shape")
	case s.Monthly <= 0:
		t.Fatal("no monthly price: estimated_monthly_price is not being read")
	case len(s.Regions) != 1 || s.Regions[0] == "":
		t.Fatalf("no region on the size: %v", s.Regions)
	}

	regions, err := c.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 1 || regions[0].ID == "" {
		t.Fatalf("got %+v, want the one location the source is in", regions)
	}
	t.Logf("region: %q", regions[0].ID)
}

// TestLiveBadKeyIsFatal proves the classifier on the one failure that
// costs nothing to cause. A rejected key must not look retryable, or a
// loop with a typo in its configuration retries for ever.
func TestLiveBadKeyIsFatal(t *testing.T) {
	live(t) // for the skip
	c, err := New(Options{APIKey: "not-a-key", Source: os.Getenv("DEPA_SOURCE"), Bandwidth: bandwidth})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.List(context.Background())
	if err == nil {
		t.Fatal("a made-up API key was accepted")
	}
	t.Logf("a bad key gives: %v", err)
	if errors.Is(err, provision.ErrTransient) {
		t.Fatal("a bad key looks retryable, so a misconfigured loop would retry for ever")
	}
}

// TestLiveCloneAndDestroy is the whole round trip and the only test that
// spends anything: it clones the source, checks that asking again does
// not clone a second one, and destroys it.
//
// The destroy runs from a cleanup that retries, and fails the test
// loudly with the uuid if it never succeeds. A leaked machine bills
// until somebody notices, so a silent cleanup failure is the worst
// outcome this file can have.
func TestLiveCloneAndDestroy(t *testing.T) {
	c := live(t)
	if os.Getenv("DEPA_LIVE_CLONE") == "" {
		t.Skip("set DEPA_LIVE_CLONE=1 to clone a real machine and destroy it again")
	}
	ctx := context.Background()
	key := "smoke-" + strconv.FormatInt(time.Now().Unix(), 10)

	m, err := c.Create(ctx, provision.Spec{Key: key})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	t.Logf("cloned %s as %q", m.ID, key)

	t.Cleanup(func() {
		if err := destroyHard(c, m.ID); err != nil {
			t.Errorf("LEAKED MACHINE %s (%s): %v — destroy it by hand", m.ID, key, err)
			return
		}
		t.Logf("destroyed %s", m.ID)
	})

	if m.State != provision.Starting {
		t.Errorf("a fresh clone is %s, want starting: Create is waiting for something", m.State)
	}

	// The point of the key. A create that timed out would be retried, and
	// a retry must find the machine rather than buy a second one.
	again, err := c.Create(ctx, provision.Spec{Key: key})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if again.ID != m.ID {
		t.Fatalf("asking twice for %q made two machines: %s and %s — DESTROY BOTH", key, m.ID, again.ID)
	}

	found, err := find(c, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listed as %s, state %s, addrs %v", found.ID, found.State, found.Addrs)
	if found.ID != m.ID {
		t.Fatalf("the listing names %s, the clone was %s", found.ID, m.ID)
	}
}

// find waits for a machine to appear in the listing. A clone is not
// instant, and the listing is the only place the fleet's own view of it
// comes from.
func find(c *Client, key string) (provision.Machine, error) {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		got, err := c.List(context.Background())
		if err != nil {
			return provision.Machine{}, err
		}
		for _, m := range got {
			if m.Key == key {
				return m, nil
			}
		}
		if time.Now().After(deadline) {
			return provision.Machine{}, fmt.Errorf("%q never appeared in the listing", key)
		}
		time.Sleep(10 * time.Second)
	}
}

// destroyHard keeps asking until the machine is gone from the listing.
// A provider busy creating a machine will not delete it, so the first
// call can fail for a reason that stops being true.
func destroyHard(c *Client, id string) error {
	deadline := time.Now().Add(10 * time.Minute)
	var last error
	for {
		if err := c.Destroy(context.Background(), id); err != nil {
			last = err
		} else {
			got, err := c.List(context.Background())
			if err != nil {
				return err
			}
			gone := true
			for _, m := range got {
				if m.ID == id {
					gone = false
					break
				}
			}
			if gone {
				return nil
			}
			last = fmt.Errorf("destroy accepted but %s is still listed", id)
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(15 * time.Second)
	}
}
