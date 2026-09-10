package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/achmadss/p2p-transport/internal/wire"
	"github.com/libp2p/go-libp2p/core/peer"
)

// usageOf reads the endpoint the application will read.
func usageOf(t *testing.T, h http.Handler, id string) (int, counter) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/subjects/"+id+"/usage", nil))
	var c counter
	json.NewDecoder(w.Body).Decode(&c)
	return w.Code, c
}

// The relays already say what each subject moved, once a period,
// because the allowance loop cannot divide a rate without it. Metering
// is those numbers added up, and it must add up across the relays: a
// subject on two of them is billed for both.
func TestUsageAddsUpEveryRelay(t *testing.T) {
	f := testFleet(t)
	machine(t, f, "alice", 1<<20)
	a, b := peer.ID("a"), peer.ID("b")
	carry(f, "alice", a, 10<<20)
	carry(f, "alice", b, 10<<20)

	f.report(a, wire.Demand{Period: time.Second, Reports: []wire.Report{{Subject: "alice", Used: 300}}})
	f.report(b, wire.Demand{Period: time.Second, Reports: []wire.Report{{Subject: "alice", Used: 700}}})

	if got := f.meter.read("alice").Bytes; got != 1000 {
		t.Fatalf("two relays moved 300 and 700, meter says %d, want 1000", got)
	}
}

// A relay reports a subject the coordinator has no share for whenever a
// placement expires mid-period. The bytes still crossed the relay, so
// they are still counted; dropping them would make the last period of
// every transfer free.
func TestUsageCountsASubjectWithNoShare(t *testing.T) {
	f := testFleet(t)
	f.report(peer.ID("a"), wire.Demand{Period: time.Second, Reports: []wire.Report{{Subject: "ghost", Used: 42}}})
	if got := f.meter.read("ghost").Bytes; got != 42 {
		t.Fatalf("meter says %d for a subject with no share, want 42", got)
	}
}

// The counter is what someone bills from, so it has to outlive the
// coordinator that counted it.
func TestUsageSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	m, err := openMeter(dir)
	if err != nil {
		t.Fatal(err)
	}
	m.add("alice", 5000)
	started := m.read("alice").Since
	if err := m.flush(); err != nil {
		t.Fatal(err)
	}

	again, err := openMeter(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := again.read("alice")
	if got.Bytes != 5000 || !got.Since.Equal(started) {
		t.Fatalf("after a restart: %d bytes since %s, want 5000 since %s", got.Bytes, got.Since, started)
	}
}

// The endpoint answers for a subject that exists and has moved nothing,
// refuses one that does not, and forgets a subject that is deleted.
func TestUsageEndpoint(t *testing.T) {
	f := testFleet(t)
	m := f.meter
	h := admin(f, 7, 70)

	if code, _ := usageOf(t, h, "alice"); code != http.StatusNotFound {
		t.Fatalf("usage of a subject nobody created = %d, want 404", code)
	}
	if got := put(t, h, "/v1/subjects/alice", `{}`); got != http.StatusNoContent {
		t.Fatalf("PUT = %d, want 204", got)
	}
	code, quiet := usageOf(t, h, "alice")
	if code != http.StatusOK || quiet.Bytes != 0 || quiet.Since.IsZero() {
		t.Fatalf("a subject that moved nothing = %d %+v, want 200 and zero bytes since a real time", code, quiet)
	}

	// Since must not move between two reads, or an application
	// subtracting one reading from the next would read every quiet
	// moment as a counter that had been reset.
	if _, twice := usageOf(t, h, "alice"); !twice.Since.Equal(quiet.Since) {
		t.Fatalf("since moved between two reads: %s then %s", quiet.Since, twice.Since)
	}

	m.add("alice", 1234)
	if _, got := usageOf(t, h, "alice"); got.Bytes != 1234 {
		t.Fatalf("usage = %d, want 1234", got.Bytes)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/v1/subjects/alice", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", w.Code)
	}
	if got := m.read("alice").Bytes; got != 0 {
		t.Fatalf("a deleted subject kept %d bytes of usage", got)
	}
}
