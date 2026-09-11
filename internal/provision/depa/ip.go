package depa

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// maxIPTries bounds the draw for an address that is not blacklisted.
// Each attempt reserves a real address and bills for it until it is
// dropped again, so a blacklist wide enough to exhaust this is a
// blacklist that wants looking at rather than retrying.
const maxIPTries = 10

// An IP is one reserved public address in the account.
type IP struct {
	ID       string // uuid, which is what the change and delete calls take
	Address  string
	Instance string // uuid of the machine holding it, empty when free
}

// IPs returns every public address reserved in the account, attached or
// not.
func (c *Client) IPs(ctx context.Context) ([]IP, error) {
	var out []IP
	for page := 1; page <= maxPages; page++ {
		var r struct {
			Data struct {
				Data []struct {
					ID       string `json:"id"`
					Address  string `json:"ip_address"`
					Instance struct {
						ID string `json:"id"`
					} `json:"instance"`
				} `json:"data"`
				Page struct {
					TotalPages int `json:"total_pages"`
				} `json:"page"`
			} `json:"data"`
		}
		q := url.Values{"limit": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
		if err := c.call(ctx, http.MethodGet, "/network/public/reserved?"+q.Encode(), nil, &r); err != nil {
			return nil, err
		}
		for _, in := range r.Data.Data {
			out = append(out, IP{ID: in.ID, Address: in.Address, Instance: in.Instance.ID})
		}
		if page >= r.Data.Page.TotalPages || len(r.Data.Data) == 0 {
			break
		}
	}
	return out, nil
}

// NewIP reserves a public address in this client's location.
//
// DEPA's create returns the address and not its uuid, and every other
// call here takes the uuid, so the reservation is read back out of the
// listing. That is also the only check that the address really exists.
func (c *Client) NewIP(ctx context.Context) (IP, error) {
	var r struct {
		Data struct {
			Address string `json:"ip_address"`
		} `json:"data"`
	}
	body := map[string]any{"location_id": c.location}
	if err := c.call(ctx, http.MethodPost, "/network/public/create", body, &r); err != nil {
		return IP{}, err
	}
	if r.Data.Address == "" {
		return IP{}, fmt.Errorf("depa: reserving an address returned no address")
	}
	have, err := c.IPs(ctx)
	if err != nil {
		return IP{}, err
	}
	for _, ip := range have {
		if ip.Address == r.Data.Address {
			return ip, nil
		}
	}
	return IP{}, fmt.Errorf("depa: reserved %s but it is not in the account's addresses", r.Data.Address)
}

// MoveIP attaches a reserved address to a machine, taking it off
// whatever machine held it.
func (c *Client) MoveIP(ctx context.Context, ip, instance string) error {
	body := map[string]any{"instance_id": instance}
	return c.call(ctx, http.MethodPatch, "/network/public/"+url.PathEscape(ip)+"/change-instance", body, nil)
}

// DropIP releases a reserved address. An address that is already gone is
// success, because the only reason to drop one is to stop paying for it.
func (c *Client) DropIP(ctx context.Context, ip string) error {
	err := c.call(ctx, http.MethodDelete, "/network/public/"+url.PathEscape(ip)+"/delete", nil, nil)
	return ignoreGone(err)
}

// Banned reports whether an address is one the fleet refuses to use.
func (c *Client) Banned(addr string) bool { return c.blacklist[addr] }

// freeIP finds an address the fleet may use, reserving one if the
// account has none spare.
//
// An address already reserved and attached to nothing is used first:
// DEPA bills for a reservation whether or not a machine holds it, so
// making a second one while the first sits idle pays twice for one
// address.
func (c *Client) freeIP(ctx context.Context) (IP, error) {
	have, err := c.IPs(ctx)
	if err != nil {
		return IP{}, err
	}
	for _, ip := range have {
		if ip.Instance == "" && !c.Banned(ip.Address) {
			return ip, nil
		}
	}
	for try := 0; try < maxIPTries; try++ {
		ip, err := c.NewIP(ctx)
		if err != nil {
			return IP{}, err
		}
		if !c.Banned(ip.Address) {
			return ip, nil
		}
		// It is on the list, so nothing will ever reach a machine wearing
		// it. Dropping it now is what stops the draw from billing for a
		// pile of addresses the fleet cannot use.
		if err := c.DropIP(ctx, ip.ID); err != nil {
			return IP{}, fmt.Errorf("depa: drew blacklisted %s and could not release it: %w", ip.Address, err)
		}
	}
	return IP{}, fmt.Errorf("depa: %d addresses in a row were all blacklisted", maxIPTries)
}

// give puts a usable public address on a machine.
func (c *Client) give(ctx context.Context, instance string) error {
	ip, err := c.freeIP(ctx)
	if err != nil {
		return err
	}
	return c.MoveIP(ctx, ip.ID, instance)
}
