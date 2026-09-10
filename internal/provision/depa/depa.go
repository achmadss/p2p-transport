// Package depa provisions machines on DEPA Cloud, api.depa.id.
//
// Three facts about that API shape everything here.
//
// It has no user data and no cloud-init, so a machine cannot be told
// anything at boot. A relay is therefore made by cloning one instance an
// operator built by hand — heimdall installed, HEIMDALL_BIFROST set, the
// service enabled — and the coordinator's address lives in that
// instance rather than in a Spec. A clone inherits the source's size and
// location, so Spec.Size and Spec.Region are not honoured; Sizes and
// Regions each return the single value that results.
//
// It has no idempotency key, so the hostname carries one: every machine
// this adapter makes is named prefix+Key, and Create lists before it
// clones.
//
// And it publishes no bandwidth figure anywhere — not per tier, not per
// size, not per location — so what a relay can forward is a measured
// number the caller passes in.
package depa

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/achmadss/p2p-transport/internal/provision"
)

const (
	defaultBase   = "https://api.depa.id/v1"
	defaultPrefix = "heimdall-"

	// perPage is the listing page size. Listing is the reconcile step and
	// runs every tick, so fewer round trips matter more than a small
	// response.
	perPage = 100

	// maxPages stops a listing that never says it has finished. A fleet
	// that has genuinely made ten thousand relays has a different
	// problem than this loop can solve.
	maxPages = 100
)

// Options configures a Client. APIKey, Source and Bandwidth are
// required; the rest have defaults.
type Options struct {
	// APIKey goes in the x-apikey header of every request.
	APIKey string

	// Source is the uuid of the instance to clone: the prepared relay.
	// Spec.Image overrides it per call.
	Source string

	// Bandwidth is bytes per second, per direction, that a relay on one
	// of these machines can really forward. DEPA publishes nothing to
	// derive it from, so it is measured on a real machine and passed in.
	// A relayed byte crosses the machine twice, so it is not the link
	// speed unless that figure is already per direction.
	Bandwidth int64

	// Prefix marks the hostnames this fleet owns. Machines without it are
	// somebody else's and are never listed or destroyed.
	Prefix string

	// BaseURL and HTTP exist for tests and for a proxy.
	BaseURL string
	HTTP    *http.Client
}

// A Client is one DEPA account.
type Client struct {
	key       string
	source    string
	bandwidth int64
	prefix    string
	base      string
	http      *http.Client
}

// New checks the options and returns a client.
func New(o Options) (*Client, error) {
	switch {
	case o.APIKey == "":
		return nil, errors.New("depa: no API key")
	case o.Source == "":
		return nil, errors.New("depa: no source instance to clone")
	case o.Bandwidth <= 0:
		return nil, errors.New("depa: bandwidth must be measured and positive")
	}
	c := &Client{
		key:       o.APIKey,
		source:    o.Source,
		bandwidth: o.Bandwidth,
		prefix:    cmp.Or(o.Prefix, defaultPrefix),
		base:      strings.TrimSuffix(cmp.Or(o.BaseURL, defaultBase), "/"),
		http:      o.HTTP,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	return c, nil
}

var _ provision.Provisioner = (*Client)(nil)

// Create clones the prepared relay into a new machine named after the
// key, and returns the machine that already had that name if one does.
//
// The check is a listing rather than a lock, so two Creates racing with
// the same key inside one API round trip can still both clone. One
// coordinator runs one scaling loop, which is why that is left alone;
// give the loop more than one goroutine and this needs a lock over the
// whole list-then-clone.
func (c *Client) Create(ctx context.Context, s provision.Spec) (provision.Machine, error) {
	if err := checkKey(s.Key); err != nil {
		return provision.Machine{}, err
	}
	// Dropping user data quietly would hand back a relay that never
	// finds its coordinator, and the loop would go on making more of
	// them. Whatever a caller needs to say at boot belongs in the source
	// instance instead.
	if s.UserData != "" {
		return provision.Machine{}, errors.New("depa: no user data: bake it into the source instance")
	}
	name := c.prefix + s.Key
	have, err := c.List(ctx)
	if err != nil {
		return provision.Machine{}, err
	}
	for _, m := range have {
		if m.Key == s.Key {
			return m, nil
		}
	}
	src := cmp.Or(s.Image, c.source)
	var out struct {
		Data struct {
			UUID string `json:"uuid"`
		} `json:"data"`
	}
	body := map[string]string{"hostname": name}
	if err := c.call(ctx, http.MethodPost, "/instance/"+url.PathEscape(src)+"/clone", body, &out); err != nil {
		return provision.Machine{}, err
	}
	if out.Data.UUID == "" {
		return provision.Machine{}, errors.New("depa: clone returned no uuid")
	}
	// Starting, with no address and no creation time. The address arrives
	// in a later listing, and the relay is usable when it registers
	// rather than when DEPA says the machine is up.
	return provision.Machine{ID: out.Data.UUID, Key: s.Key, State: provision.Starting}, nil
}

// Destroy terminates a machine and releases its public IP and disks.
// A machine that is already gone is success.
//
// It refuses to destroy the instance every relay is cloned from. That
// machine is the fleet's only copy of a working relay and nothing
// rebuilds it, so losing it is not an outage that heals — it is an
// afternoon with a fresh Ubuntu and the install notes. The listing's
// prefix is supposed to keep the source out of the fleet's hands
// already; this is the second lock, on the one door that matters.
func (c *Client) Destroy(ctx context.Context, id string) error {
	if id == c.source {
		return fmt.Errorf("depa: refusing to destroy %s: it is the instance every relay is cloned from", id)
	}
	// The _method header is DEPA's own requirement for DELETE, not a
	// workaround for anything here.
	body := map[string]bool{"remove_ip": true, "remove_block_storage": true}
	err := c.call(ctx, http.MethodDelete, "/instance/"+url.PathEscape(id), body, nil)
	var s *statusError
	if errors.As(err, &s) && (s.code == http.StatusNotFound || s.code == http.StatusGone) {
		return nil
	}
	return err
}

// instance is one row of the listing, in the fields this adapter reads.
type instance struct {
	UUID     string `json:"uuid"`
	Hostname string `json:"hostname"`
	IPv4     string `json:"ip_address"`
	IPv6     string `json:"ipv6_address"`
	Status   string `json:"status"`
	Location string `json:"location"`
}

// List returns every machine this fleet made, and nothing else in the
// account.
func (c *Client) List(ctx context.Context) ([]provision.Machine, error) {
	var out []provision.Machine
	for page := 1; page <= maxPages; page++ {
		var r struct {
			Data struct {
				Data []instance `json:"data"`
				Page struct {
					TotalPages int `json:"total_pages"`
				} `json:"page"`
			} `json:"data"`
		}
		q := url.Values{
			"limit": {strconv.Itoa(perPage)},
			"page":  {strconv.Itoa(page)},
		}
		if err := c.call(ctx, http.MethodGet, "/instance?"+q.Encode(), nil, &r); err != nil {
			return nil, err
		}
		for _, in := range r.Data.Data {
			// The whole account is listed and the prefix is applied
			// here. DEPA has a search parameter that would narrow it and
			// it is deliberately not used: a search that quietly matched
			// too little would hide a relay from the reconcile, which
			// would then buy another one and bill for both. Filtering
			// locally cannot fail that way, and it is also what keeps
			// somebody else's machine in the same account from being
			// destroyed as an orphan.
			if !strings.HasPrefix(in.Hostname, c.prefix) {
				continue
			}
			out = append(out, provision.Machine{
				ID:     in.UUID,
				Key:    strings.TrimPrefix(in.Hostname, c.prefix),
				Addrs:  addrs(in),
				Region: in.Location,
				State:  state(in.Status),
			})
		}
		if page >= r.Data.Page.TotalPages || len(r.Data.Data) == 0 {
			break
		}
	}
	return out, nil
}

func addrs(in instance) []string {
	var a []string
	for _, s := range []string{in.IPv4, in.IPv6} {
		if s != "" {
			a = append(a, s)
		}
	}
	return a
}

// state maps DEPA's status to the three the loop acts on.
//
// Stopped and Error are Gone rather than Starting on purpose: both bill
// and neither will ever carry a byte, so the loop should retire the
// placement and destroy the machine. Anything unrecognised is Starting,
// because the safe guess about a status this adapter has not seen is
// that the machine is still on its way up.
func state(s string) provision.State {
	switch strings.ToLower(s) {
	case "running":
		return provision.Running
	case "stopped", "error", "terminated", "deleted":
		return provision.Gone
	default:
		return provision.Starting
	}
}

// detail is the source instance, in the fields Sizes and Regions read.
type detail struct {
	CPU      string  `json:"cpu"`
	Memory   string  `json:"memory"`
	Storage  string  `json:"storage"`
	Location string  `json:"location"`
	Monthly  float64 `json:"estimated_monthly_price"`
}

func (c *Client) detail(ctx context.Context, id string) (detail, error) {
	var r struct {
		Data detail `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, "/instance/"+url.PathEscape(id)+"/detail", nil, &r)
	return r.Data, err
}

// Sizes returns the one size this adapter can make: the source
// instance's, because every relay is a clone of it.
//
// Bandwidth is the measured figure from Options. Monthly is DEPA's own
// estimate in whole rupiah, which has no minor unit in practice.
func (c *Client) Sizes(ctx context.Context) ([]provision.Size, error) {
	d, err := c.detail(ctx, c.source)
	if err != nil {
		return nil, err
	}
	return []provision.Size{{
		ID:        strings.Join([]string{d.CPU, d.Memory, d.Storage}, "/"),
		Bandwidth: c.bandwidth,
		Monthly:   int64(d.Monthly),
		Regions:   []string{d.Location},
	}}, nil
}

// Regions returns the one location this adapter can reach: the source
// instance's, because a clone lands beside what it was cloned from.
//
// The name is the id as well. DEPA's numeric location id is only used
// when building a machine from an OS template, which this adapter never
// does, and the name is the one form both the listing and the detail
// agree on.
func (c *Client) Regions(ctx context.Context) ([]provision.Region, error) {
	d, err := c.detail(ctx, c.source)
	if err != nil {
		return nil, err
	}
	return []provision.Region{{ID: d.Location, Name: d.Location}}, nil
}

// checkKey keeps the key a legal hostname, so that trimming the prefix
// off a hostname gives the key back exactly.
func checkKey(k string) error {
	if k == "" || len(k) > 40 {
		return fmt.Errorf("depa: key %q must be 1 to 40 characters", k)
	}
	for _, r := range k {
		ok := r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("depa: key %q must be lower case letters, digits and hyphens", k)
		}
	}
	return nil
}

// statusError is an HTTP status this adapter could not classify further.
// It wraps one of the package's three errors when the status says which.
type statusError struct {
	code int
	body string
	kind error
}

func (e *statusError) Error() string {
	return fmt.Sprintf("depa: http %d: %s", e.code, e.body)
}

func (e *statusError) Unwrap() error { return e.kind }

// classify turns a status into the distinction the loop acts on.
//
// DEPA documents no error catalogue, so this reads the status alone. Out
// of capacity is not distinguishable from any other refusal here and so
// is never reported; a loop watching for it simply never sees it from
// this provider.
func classify(code int) error {
	switch {
	case code == http.StatusTooManyRequests, code >= 500:
		return provision.ErrTransient
	case code == http.StatusPaymentRequired, code == http.StatusForbidden:
		return provision.ErrQuota
	default:
		return nil
	}
}

// call sends one request and decodes the reply into out, which may be
// nil when the body is not wanted.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-apikey", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodDelete {
		req.Header.Set("_method", "DELETE")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// A refused connection, a timeout, a reset: none of them say the
		// request was wrong, so all of them are worth retrying.
		return fmt.Errorf("%w: %w", provision.ErrTransient, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(msg)), kind: classify(resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
