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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(Options{APIKey: "k", Source: "src", Bandwidth: 50 << 20, BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func row(uuid, host, status string) map[string]any {
	return map[string]any{"uuid": uuid, "hostname": host, "status": status, "ip_address": "203.0.113.7"}
}

func TestCreateClonesOnceForOneKey(t *testing.T) {
	f := &fake{}
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
	// A clone is not given a public address unless it is asked for, and a
	// relay nothing can dial is not a relay.
	if !f.lastPub {
		t.Fatal("the clone did not ask for a public address")
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
