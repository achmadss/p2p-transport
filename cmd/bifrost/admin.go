package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/achmadss/p2p-transport/internal/config"
)

// A subject is one person's allowance and the machines that share it.
//
// Min is the rate the fleet promises: it is what placement and scaling
// count, and it is never sold twice. Max is the rate a machine may
// reach when the relay is quiet, and it costs nothing to promise
// because nobody is busy all the time. Neither is a machine's limit —
// the machines of one subject share the pair between them.
//
// There is no name, no account and no owner here on purpose. The
// application knows who this is; the fleet only needs the numbers.
type subject struct {
	Min     int64    `json:"min"`     // bytes per second, guaranteed
	Max     int64    `json:"max"`     // bytes per second, ceiling
	Devices []string `json:"devices"` // machine ids
}

// store holds every subject, on disk as one JSON file.
//
// ponytail: a file and a mutex. It is rewritten whole on every change,
// which is fine for the hundreds of subjects one coordinator places and
// wrong for the hundred thousand it will not. SQLite when that day comes.
type store struct {
	path     string
	mu       sync.Mutex
	subjects map[string]*subject
}

const storeFile = "subjects.json"

func openStore(dir string) (*store, error) {
	d, err := config.Dir(dir)
	if err != nil {
		return nil, err
	}
	s := &store{path: filepath.Join(d, storeFile), subjects: map[string]*subject{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(b, &s.subjects); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", s.path, err)
	}
	return s, nil
}

// save writes the whole file. The caller holds the lock.
func (s *store) save() error {
	b, err := json.MarshalIndent(s.subjects, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(b, '\n'), 0o600)
}

// subject returns one subject by id.
func (s *store) subject(id string) (string, subject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.subjects[id]
	if !ok {
		return "", subject{}, false
	}
	return id, *sub, true
}

// find returns the subject a machine belongs to.
//
// ponytail: a scan of every subject. A map from machine to subject when
// the scan shows up in a profile, not before.
func (s *store) find(peer string) (string, subject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sub := range s.subjects {
		for _, d := range sub.Devices {
			if d == peer {
				return id, *sub, true
			}
		}
	}
	return "", subject{}, false
}

func (s *store) set(id string, min, max int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subjects[id]
	if sub == nil {
		sub = &subject{}
		s.subjects[id] = sub
	}
	sub.Min, sub.Max = min, max
	return s.save()
}

func (s *store) setDevices(id string, peers []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subjects[id]
	if sub == nil {
		return errNoSubject
	}
	sub.Devices = peers
	return s.save()
}

func (s *store) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subjects[id]; !ok {
		return errNoSubject
	}
	delete(s.subjects, id)
	return s.save()
}

var errNoSubject = errors.New("no such subject")

// Defaults for a subject the application creates without saying what it
// is allowed. A small floor and a generous ceiling on purpose: the
// floor is what the fleet has to buy machines for, and the ceiling
// costs nothing because nobody transfers all day.
const (
	defaultSubjectMin = 2 << 20  // 2 MB/s guaranteed
	defaultSubjectMax = 50 << 20 // 50 MB/s when the relay is quiet
)

// admin is the application's way in: plain HTTP and JSON, because the
// thing calling it is a web backend rather than a libp2p host.
//
// It carries no authentication and must not be exposed. Bind it to
// loopback, or to a private interface behind whatever already guards
// the application's own admin traffic.
// changed is told when a subject's ceiling moves, so the relays
// carrying it can be sent the new one. Nil in a test that only cares
// about the store.
func admin(s *store, defMin, defMax int64, changed func(subject string, max int64)) http.Handler {
	mux := http.NewServeMux()
	if changed == nil {
		changed = func(string, int64) {}
	}

	mux.HandleFunc("PUT /v1/subjects/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Pointers so an omitted rate takes the default and an explicit
		// zero floor still means zero: best effort, nothing reserved.
		var body struct {
			Min *int64 `json:"min"`
			Max *int64 `json:"max"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "body is not valid JSON", http.StatusBadRequest)
			return
		}
		min, max := defMin, defMax
		if body.Min != nil {
			min = *body.Min
		}
		if body.Max != nil {
			max = *body.Max
		}
		// A ceiling below the floor is a promise that cannot be kept,
		// and a negative rate is a typo. Refuse both here rather than
		// letting placement reason about them later.
		if min < 0 || max <= 0 || min > max {
			http.Error(w, "need 0 <= min <= max, and max above zero, in bytes per second", http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		if err := s.set(id, min, max); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Pushed after the write, so a relay never enforces a rate that
		// a restart would not bring back.
		changed(id, max)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("PUT /v1/subjects/{id}/devices", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PeerIDs []string `json:"peer_ids"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "body is not valid JSON", http.StatusBadRequest)
			return
		}
		switch err := s.setDevices(r.PathValue("id"), body.PeerIDs); {
		case errors.Is(err, errNoSubject):
			http.Error(w, err.Error(), http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	mux.HandleFunc("DELETE /v1/subjects/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch err := s.remove(r.PathValue("id")); {
		case errors.Is(err, errNoSubject):
			http.Error(w, err.Error(), http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	return mux
}
