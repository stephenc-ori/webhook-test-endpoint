package store

import (
	"regexp"
	"testing"
	"time"
)

func TestRingBufferKeepsLast50(t *testing.T) {
	s := New()
	e := s.Create()
	for i := 0; i < MaxEvents+20; i++ {
		e.Add(Event{Method: "POST"})
	}
	got := e.Events()
	if len(got) != MaxEvents {
		t.Fatalf("got %d events, want %d", len(got), MaxEvents)
	}
	// IDs are monotonic starting at 1; after 70 adds the oldest kept is 21.
	if got[0].ID != 21 {
		t.Errorf("oldest event ID = %d, want 21", got[0].ID)
	}
	if got[len(got)-1].ID != int64(MaxEvents+20) {
		t.Errorf("newest event ID = %d, want %d", got[len(got)-1].ID, MaxEvents+20)
	}
}

func TestSubscribeReceivesEvents(t *testing.T) {
	s := New()
	e := s.Create()
	ch, cancel := e.Subscribe()
	defer cancel()

	added := e.Add(Event{Method: "POST"})
	select {
	case got := <-ch:
		if got.ID != added.ID {
			t.Errorf("subscriber got ID %d, want %d", got.ID, added.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	s := New()
	e := s.Create()
	ch, cancel := e.Subscribe()
	cancel()
	e.Add(Event{Method: "POST"})
	select {
	case ev := <-ch:
		t.Errorf("unexpected event %d after unsubscribe", ev.ID)
	default:
	}
}

func TestGetUnknownID(t *testing.T) {
	s := New()
	if s.Get("nope") != nil {
		t.Error("Get of unknown ID should return nil")
	}
}

func TestIDGeneration(t *testing.T) {
	s := New()
	a, b := s.Create(), s.Create()
	if !regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+$`).MatchString(a.ID) {
		t.Errorf("ID %q is not three hyphen-separated lowercase words", a.ID)
	}
	if a.ID == b.ID {
		t.Error("two created endpoints share an ID")
	}
	if s.Get(a.ID) != a {
		t.Error("Get did not return created endpoint")
	}
}

func TestWordList(t *testing.T) {
	if len(words) < 7000 {
		t.Fatalf("word list has %d entries, want a large dictionary", len(words))
	}
	for _, w := range words {
		if !regexp.MustCompile(`^[a-z]+$`).MatchString(w) {
			t.Errorf("word %q is not plain lowercase", w)
		}
	}
}

func TestValidID(t *testing.T) {
	s := New()
	real := s.Create().ID
	for id, want := range map[string]bool{
		real:                    true,
		"abacus-zebra":          false, // two words
		"abacus-zebra-nOtWoRd":  false, // not in dictionary
		"abacus-zebra-zebra-ox": false, // four parts
		"":                      false,
		"abacus--zebra":         false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestGetOrCreateRevives(t *testing.T) {
	s := New()
	// A valid ID from "before the restart" is revived with default config.
	old := s.Create()
	id := old.ID
	s2 := New() // fresh store simulates a restart
	e := s2.GetOrCreate(id)
	if e == nil {
		t.Fatal("valid old ID was not revived")
	}
	if e.ID != id {
		t.Errorf("revived ID = %q, want %q", e.ID, id)
	}
	if s2.GetOrCreate(id) != e {
		t.Error("second GetOrCreate should return the same endpoint")
	}
	if s2.GetOrCreate("not-a-valid-id!") != nil {
		t.Error("invalid ID should not be created")
	}
}

func TestExpire(t *testing.T) {
	s := New()
	e := s.Create()
	e.mu.Lock()
	e.lastActive = time.Now().Add(-48 * time.Hour)
	e.mu.Unlock()
	s.expire(24 * time.Hour)
	if s.Get(e.ID) != nil {
		t.Error("idle endpoint should have been expired")
	}

	// An endpoint with a live subscriber is not expired.
	e2 := s.Create()
	_, cancel := e2.Subscribe()
	defer cancel()
	e2.mu.Lock()
	e2.lastActive = time.Now().Add(-48 * time.Hour)
	e2.mu.Unlock()
	s.expire(24 * time.Hour)
	if s.Get(e2.ID) == nil {
		t.Error("endpoint with live subscriber should not be expired")
	}
}
