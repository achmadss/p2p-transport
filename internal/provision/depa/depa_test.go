package depa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/achmadss/p2p-transport/internal/provision"
)

// fake is a DEPA that answers from a script. Every handler records what
// it was called with, because most of what matters here is which call
// was made rather than what came back.
type fake struct {
	t        *testing.T
	rows     []map[string]any
	clones   int
	deleted  []string
	status   int    // when non-zero, every request fails with this
	lastHost string // hostname of the last clone
	lastPub  bool   // whether the last clone asked for a public address

	// The pool of public addresses. pool is what the provider has to
	// give, ips is what the account holds, and moved records every
	// attachment as "ip->instance".
	pool    []string
	ips     []IP
	moved   []string
	dropped []string
	blocked []string // what the client is told never to use
	loc     int      // location the reserve call was given
}

func (f *fake) serve(t *testing.T) *Client {
	t.Helper()
	f.t = t
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter) bool {
		if f.status == 0 {
			return false
		}
		http.Error(w, `{"message":"no"}`, f.status)
		return true
	}
	mux.HandleFunc("GET /v1/instance", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		// Narrowing the listing on the server is what the local prefix
		// check exists to avoid depending on. Asking for it back would
		// put the risk straight back.
		if r.URL.Query().Has("search") {
			f.t.Error("the listing asked the server to filter")
		}
		// One row per page, so the paging is actually exercised.
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		var rows []map[string]any
		if page <= len(f.rows) {
			rows = f.rows[page-1 : page]
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"data": rows,
			"page": map[string]any{"total_pages": len(f.rows)},
		}})
	})
	mux.HandleFunc("POST /v1/instance/{id}/clone", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		var body struct {
			Hostname string `json:"hostname"`
			PublicIP bool   `json:"use_public_ip"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.clones++
		f.lastHost = body.Hostname
		f.lastPub = body.PublicIP
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"uuid": "new-uuid"}})
	})
	mux.HandleFunc("DELETE /v1/instance/{id}", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		f.deleted = append(f.deleted, r.PathValue("id"))
		w.Write([]byte(`{"message":"terminating"}`))
	})
	mux.HandleFunc("GET /v1/instance/{id}/detail", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"cpu": "2 CPU", "memory": "2048 MB", "storage": "60 GB",
			"location": "Jakarta 1", "estimated_monthly_price": 545599.18,
		}})
	})
	mux.HandleFunc("GET /v1/network/public/reserved", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		rows := []map[string]any{}
		for _, ip := range f.ips {
			rows = append(rows, map[string]any{
				"id": ip.ID, "ip_address": ip.Address,
				"instance": map[string]any{"id": ip.Instance},
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"data": rows,
			"page": map[string]any{"total_pages": 1},
		}})
	})
	mux.HandleFunc("POST /v1/network/public/create", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		var body struct {
			Location int `json:"location_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.loc = body.Location
		// DEPA hands out the first free address in its pool, and an
		// address released goes back into that pool — so the same one
		// comes round again immediately. Anything that releases a bad
		// draw before drawing again gets it straight back, which is the
		// whole reason this fake models the pool rather than a queue.
		addr := ""
		for _, a := range f.pool {
			taken := false
			for _, ip := range f.ips {
				taken = taken || ip.Address == a
			}
			if !taken {
				addr = a
				break
			}
		}
		if addr == "" {
			f.t.Error("the pool of addresses ran out")
			http.Error(w, `{"message":"no addresses"}`, http.StatusConflict)
			return
		}
		f.ips = append(f.ips, IP{ID: "ip-" + addr, Address: addr})
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ip_address": addr}})
	})
	mux.HandleFunc("PATCH /v1/network/public/{id}/change-instance", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		var body struct {
			Instance string `json:"instance_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.moved = append(f.moved, r.PathValue("id")+"->"+body.Instance)
		for i := range f.ips {
			if f.ips[i].ID == r.PathValue("id") {
				f.ips[i].Instance = body.Instance
			}
		}
		w.Write([]byte(`{"data":{}}`))
	})
	mux.HandleFunc("DELETE /v1/network/public/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		if fail(w) {
			return
		}
		f.dropped = append(f.dropped, r.PathValue("id"))
		kept := f.ips[:0]
		for _, ip := range f.ips {
			if ip.ID != r.PathValue("id") {
				kept = append(kept, ip)
			}
		}
		f.ips = kept
		w.Write([]byte(`{"data":{}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		APIKey: "k", Source: "src", Bandwidth: 50 << 20, Location: 1,
		Blacklist: f.blocked, BaseURL: srv.URL + "/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func row(uuid, host, status string) map[string]any {
	return map[string]any{"uuid": uuid, "hostname": host, "status": status, "ip_address": "203.0.113.7"}
}

// bare is a machine that exists and has no public address, which is what
// a clone is until this adapter gives it one.
func bare(uuid, host, status string) map[string]any {
	return map[string]any{"uuid": uuid, "hostname": host, "status": status}
}

func TestCreateClonesOnceForOneKey(t *testing.T) {
	f := &fake{pool: []string{"203.0.113.7"}}
	c := f.serve(t)

	m, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "new-uuid" || m.State != provision.Starting {
		t.Fatalf("got %+v", m)
	}
	if f.lastHost != "heimdall-relay-1" {
		t.Fatalf("hostname %q does not carry the key", f.lastHost)
	}
	// The address must not come from the clone: DEPA would draw one from
	// a pool holding addresses nothing can reach, and would not say
	// which. The adapter draws it separately and attaches it.
	if f.lastPub {
		t.Fatal("the clone took whatever address DEPA felt like handing out")
	}
	if len(f.moved) != 1 || f.moved[0] != "ip-203.0.113.7->new-uuid" {
		t.Fatalf("attached %v, want the drawn address on the new machine", f.moved)
	}

	// The machine now exists, which is what a retry after a timeout would
	// find. Asking again must not buy a second one.
	f.rows = []map[string]any{row("new-uuid", "heimdall-relay-1", "Running")}
	again, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"})
	if err != nil {
		t.Fatal(err)
	}
	if f.clones != 1 {
		t.Fatalf("cloned %d times for one key", f.clones)
	}
	if again.ID != "new-uuid" || again.State != provision.Running {
		t.Fatalf("got %+v", again)
	}
}

func TestCreateRefusesWhatItCannotDeliver(t *testing.T) {
	c := (&fake{}).serve(t)
	for _, s := range []provision.Spec{
		{Key: "relay-1", UserData: "#cloud-config"},
		{Key: "Relay_1"},
		{Key: ""},
	} {
		if _, err := c.Create(context.Background(), s); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}

func TestListPagesAndKeepsOnlyThisFleet(t *testing.T) {
	f := &fake{rows: []map[string]any{
		row("a", "heimdall-one", "Running"),
		row("b", "someone-elses-box", "Running"),
		row("c", "heimdall-two", "Pending"),
		row("d", "heimdall-three", "Stopped"),
	}}
	c := f.serve(t)

	got, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []provision.Machine{
		{ID: "a", Key: "one", State: provision.Running},
		{ID: "c", Key: "two", State: provision.Starting},
		{ID: "d", Key: "three", State: provision.Gone},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d machines, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].ID != w.ID || got[i].Key != w.Key || got[i].State != w.State {
			t.Fatalf("machine %d: got %+v, want %+v", i, got[i], w)
		}
	}
}

func TestDestroyOfSomethingAlreadyGoneIsSuccess(t *testing.T) {
	f := &fake{}
	c := f.serve(t)
	if err := c.Destroy(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	f.status = http.StatusNotFound
	if err := c.Destroy(context.Background(), "a"); err != nil {
		t.Fatalf("already gone should be success, got %v", err)
	}
}

func TestTheLoopCanTellRetryFromStop(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{
		{http.StatusTooManyRequests, provision.ErrTransient},
		{http.StatusBadGateway, provision.ErrTransient},
		{http.StatusPaymentRequired, provision.ErrQuota},
		{http.StatusForbidden, provision.ErrQuota},
	} {
		f := &fake{status: tc.code}
		c := f.serve(t)
		_, err := c.List(context.Background())
		if !errors.Is(err, tc.want) {
			t.Fatalf("http %d gave %v, want %v", tc.code, err, tc.want)
		}
	}
	// A malformed request is neither, so the loop stops instead of
	// hammering the provider with the same bad call.
	f := &fake{status: http.StatusBadRequest}
	c := f.serve(t)
	_, err := c.List(context.Background())
	if err == nil || errors.Is(err, provision.ErrTransient) || errors.Is(err, provision.ErrQuota) {
		t.Fatalf("http 400 gave %v, want a fatal error", err)
	}
}

func TestSizeAndRegionComeFromTheSourceInstance(t *testing.T) {
	c := (&fake{}).serve(t)

	sizes, err := c.Sizes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 {
		t.Fatalf("got %d sizes, want the one the source has", len(sizes))
	}
	s := sizes[0]
	if s.ID != "2 CPU/2048 MB/60 GB" || s.Bandwidth != 50<<20 || s.Monthly != 545599 {
		t.Fatalf("got %+v", s)
	}
	if len(s.Regions) != 1 || s.Regions[0] != "Jakarta 1" {
		t.Fatalf("got regions %v", s.Regions)
	}

	regions, err := c.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 1 || regions[0].ID != "Jakarta 1" {
		t.Fatalf("got %+v", regions)
	}
}

func TestNewInsistsOnAMeasuredBandwidth(t *testing.T) {
	for _, o := range []Options{
		{Source: "src", Bandwidth: 1},
		{APIKey: "k", Bandwidth: 1},
		{APIKey: "k", Source: "src"},
	} {
		if _, err := New(o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}

// Nothing rebuilds the instance relays are cloned from. Whatever names a
// machine to destroy — a reconcile that miscounts, an operator with the
// wrong uuid on the clipboard — must not be able to name that one.
func TestTheCloneSourceCannotBeDestroyed(t *testing.T) {
	f := &fake{}
	c := f.serve(t)
	if err := c.Destroy(context.Background(), "src"); err == nil {
		t.Fatal("the clone source was destroyed")
	}
	if len(f.deleted) != 0 {
		t.Fatalf("the delete was sent anyway: %v", f.deleted)
	}
	if err := c.Destroy(context.Background(), "some-other-machine"); err != nil {
		t.Fatal(err)
	}
}

// Some addresses in this account answer from nowhere. A relay wearing
// one is a machine that bills and is never dialled, so a drawn address
// on the list is kept out of the way until a usable one turns up and
// only then released — kept, because releasing it puts it back at the
// front of the provider's pool and the next draw returns the same one;
// released in the end, because a reservation costs money whether or not
// anything holds it.
func TestABlacklistedAddressIsHeldUntilAGoodOneIsDrawn(t *testing.T) {
	f := &fake{
		blocked: []string{"103.253.244.13", "103.253.244.14"},
		pool:    []string{"103.253.244.13", "103.253.244.14", "203.0.113.9"},
	}
	c := f.serve(t)

	if _, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.moved) != 1 || f.moved[0] != "ip-203.0.113.9->new-uuid" {
		t.Fatalf("attached %v, want only the address that is not blacklisted", f.moved)
	}
	want := []string{"ip-103.253.244.13", "ip-103.253.244.14"}
	if len(f.dropped) != 2 || f.dropped[0] != want[0] || f.dropped[1] != want[1] {
		t.Fatalf("released %v, want both blacklisted addresses %v", f.dropped, want)
	}
	if f.loc != 1 {
		t.Fatalf("reserved in location %d, want the one the client was given", f.loc)
	}
}

// A blacklist wide enough to swallow the whole pool is a mistake to
// report, not one to keep paying for: each draw reserves a real address.
func TestADrawThatKeepsComingUpBlacklistedStops(t *testing.T) {
	var pool, banned []string
	for i := 0; i < maxIPTries+5; i++ {
		a := "198.51.100." + strconv.Itoa(i)
		pool = append(pool, a)
		banned = append(banned, a)
	}
	f := &fake{blocked: banned, pool: pool}
	c := f.serve(t)

	_, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"})
	if err == nil {
		t.Fatal("a blacklist covering every address still produced a machine")
	}
	if len(f.dropped) != maxIPTries {
		t.Fatalf("released %d addresses, want all %d it reserved", len(f.dropped), maxIPTries)
	}
}

// An address already reserved and attached to nothing is paid for
// already. Drawing a second one while it sits idle pays twice for one
// address.
func TestAnIdleAddressIsUsedBeforeANewOneIsBought(t *testing.T) {
	f := &fake{
		ips:  []IP{{ID: "ip-old", Address: "203.0.113.50"}},
		pool: nil, // reserving anything at all would find the pool empty and fail
	}
	c := f.serve(t)

	if _, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.moved) != 1 || f.moved[0] != "ip-old->new-uuid" {
		t.Fatalf("attached %v, want the address the account already had", f.moved)
	}
}

// An address that is on the list is no more usable for sitting in the
// account already, so an idle one still has to be skipped.
func TestAnIdleAddressOnTheListIsSkipped(t *testing.T) {
	f := &fake{
		ips:     []IP{{ID: "ip-bad", Address: "103.253.244.13"}},
		blocked: []string{"103.253.244.13"},
		pool:    []string{"203.0.113.9"},
	}
	c := f.serve(t)

	if _, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.moved) != 1 || f.moved[0] != "ip-203.0.113.9->new-uuid" {
		t.Fatalf("attached %v, want a fresh address rather than the blacklisted idle one", f.moved)
	}
}

// Making the machine and giving it an address are two calls, so a Create
// can die between them and leave a machine nothing can reach. The retry
// finds it by name and must finish the job rather than report a healthy
// machine or buy a second one.
func TestAMachineLeftWithoutAnAddressGetsOneOnTheRetry(t *testing.T) {
	f := &fake{
		rows: []map[string]any{bare("half-made", "heimdall-relay-1", "Running")},
		pool: []string{"203.0.113.9"},
	}
	c := f.serve(t)

	m, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "half-made" {
		t.Fatalf("got %s, want the machine that was already there", m.ID)
	}
	if f.clones != 0 {
		t.Fatalf("cloned %d times, want none: the machine existed", f.clones)
	}
	if len(f.moved) != 1 || f.moved[0] != "ip-203.0.113.9->half-made" {
		t.Fatalf("attached %v, want an address on the machine that had none", f.moved)
	}
}

// A machine that already answers must be left alone. Drawing it a second
// address would pay for one nothing uses and move the machine off the
// address the fleet is already dialling.
func TestAMachineThatAlreadyAnswersIsNotGivenAnother(t *testing.T) {
	f := &fake{rows: []map[string]any{row("done", "heimdall-relay-1", "Running")}}
	c := f.serve(t)

	if _, err := c.Create(context.Background(), provision.Spec{Key: "relay-1"}); err != nil {
		t.Fatal(err)
	}
	if len(f.moved) != 0 {
		t.Fatalf("moved %v, want nothing touched", f.moved)
	}
}
