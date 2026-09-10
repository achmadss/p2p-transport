package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func put(t *testing.T, h http.Handler, path, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("PUT", path, strings.NewReader(body)))
	return w.Code
}

// An application that says nothing about rates gets the defaults, so
// the common call is the short one.
func TestSubjectDefaults(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := admin(st, testMeter(t), 7, 70, nil)

	if got := put(t, h, "/v1/subjects/alice", `{}`); got != http.StatusNoContent {
		t.Fatalf("PUT with no rates = %d, want 204", got)
	}
	if got := st.subjects["alice"]; got.Min != 7 || got.Max != 70 {
		t.Fatalf("got min %d max %d, want the defaults 7 and 70", got.Min, got.Max)
	}

	// An explicit zero floor is best effort, not "use the default".
	if got := put(t, h, "/v1/subjects/bob", `{"min":0,"max":100}`); got != http.StatusNoContent {
		t.Fatalf("PUT with a zero floor = %d, want 204", got)
	}
	if got := st.subjects["bob"]; got.Min != 0 || got.Max != 100 {
		t.Fatalf("got min %d max %d, want 0 and 100", got.Min, got.Max)
	}
}

// A ceiling under the floor is a promise that cannot be kept.
func TestSubjectRatesRefused(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := admin(st, testMeter(t), 7, 70, nil)

	for _, body := range []string{`{"min":100,"max":10}`, `{"min":-1}`, `{"max":0}`, `not json`} {
		if got := put(t, h, "/v1/subjects/x", body); got != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400", body, got)
		}
	}
	if len(st.subjects) != 0 {
		t.Fatal("a refused call still wrote a subject")
	}
}

// Subjects survive a restart, because the application is not going to
// send them again.
func TestSubjectsPersist(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := put(t, admin(st, testMeter(t), 7, 70, nil), "/v1/subjects/alice", `{"min":5,"max":50}`); got != http.StatusNoContent {
		t.Fatalf("PUT = %d, want 204", got)
	}
	if err := st.setDevices("alice", []string{"machine-1"}); err != nil {
		t.Fatal(err)
	}

	again, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, sub, ok := again.find("machine-1")
	if !ok || id != "alice" || sub.Min != 5 || sub.Max != 50 {
		t.Fatalf("after a restart: %q %+v %v", id, sub, ok)
	}
}
