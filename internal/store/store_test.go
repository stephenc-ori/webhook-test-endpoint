package store

import (
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
	if len(a.ID) != IDLength {
		t.Errorf("ID length = %d, want %d", len(a.ID), IDLength)
	}
	if a.ID == b.ID {
		t.Error("two created endpoints share an ID")
	}
	if s.Get(a.ID) != a {
		t.Error("Get did not return created endpoint")
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
