// Package store holds endpoints and their received events in memory.
package store

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	// MaxEvents is the number of events retained per endpoint.
	MaxEvents = 50
	// IDLength is the length of generated endpoint IDs.
	IDLength = 16
	// idAlphabet is unambiguous lowercase base36.
	idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
)

// Event is a single received webhook request.
type Event struct {
	ID            int64       `json:"id"`
	ReceivedAt    time.Time   `json:"receivedAt"`
	Method        string      `json:"method"`
	Path          string      `json:"path"`
	Headers       http.Header `json:"headers"`
	Body          string      `json:"body"`
	BodyTruncated bool        `json:"bodyTruncated"`
	RemoteAddr    string      `json:"remoteAddr"`
	AuthResult    string      `json:"authResult"` // "n/a" | "ok" | "failed"
	SigResult     string      `json:"sigResult"`  // "n/a" | "ok" | "failed"
	Rejected      bool        `json:"rejected"`
}

// Config controls how an endpoint authenticates and responds.
type Config struct {
	AuthMode    string `json:"authMode"` // "none" | "basic" | "bearer"
	BasicUser   string `json:"basicUser"`
	BasicPass   string `json:"basicPass"`
	BearerToken string `json:"bearerToken"`

	SigEnabled bool   `json:"sigEnabled"`
	SigHeader  string `json:"sigHeader"`
	SigSecret  string `json:"sigSecret"`

	// "reject_log" | "reject_silent" | "accept_mark"
	FailureMode string `json:"failureMode"`

	RespStatus      int    `json:"respStatus"`
	RespContentType string `json:"respContentType"`
	RespBody        string `json:"respBody"`
}

// DefaultConfig returns the config a fresh endpoint starts with.
func DefaultConfig() Config {
	return Config{
		AuthMode:        "none",
		SigHeader:       "X-Hub-Signature-256",
		FailureMode:     "reject_log",
		RespStatus:      http.StatusOK,
		RespContentType: "application/json",
		RespBody:        `{"status":"success"}`,
	}
}

// Endpoint is a single webhook destination with its config and recent events.
type Endpoint struct {
	ID string

	mu         sync.Mutex
	config     Config
	events     []Event // newest last, len <= MaxEvents
	nextID     int64
	lastActive time.Time
	subs       map[chan Event]struct{}
}

// Config returns a copy of the endpoint's config.
func (e *Endpoint) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config
}

// SetConfig replaces the endpoint's config.
func (e *Endpoint) SetConfig(c Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config = c
	e.lastActive = time.Now()
}

// Events returns a copy of the retained events, oldest first.
func (e *Endpoint) Events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, len(e.events))
	copy(out, e.events)
	return out
}

// Add assigns the event an ID, appends it to the ring buffer and notifies
// subscribers. It returns the stored event.
func (e *Endpoint) Add(ev Event) Event {
	e.mu.Lock()
	e.nextID++
	ev.ID = e.nextID
	e.events = append(e.events, ev)
	if len(e.events) > MaxEvents {
		e.events = e.events[len(e.events)-MaxEvents:]
	}
	e.lastActive = time.Now()
	subs := make([]chan Event, 0, len(e.subs))
	for ch := range e.subs {
		subs = append(subs, ch)
	}
	e.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the hook
		}
	}
	return ev
}

// Touch records activity so the janitor does not expire the endpoint.
func (e *Endpoint) Touch() {
	e.mu.Lock()
	e.lastActive = time.Now()
	e.mu.Unlock()
}

// Subscribe registers a listener for new events. Call the returned func to
// unsubscribe.
func (e *Endpoint) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
	}
}

// Store is the registry of live endpoints.
type Store struct {
	mu        sync.RWMutex
	endpoints map[string]*Endpoint
}

// New creates an empty Store.
func New() *Store {
	return &Store{endpoints: make(map[string]*Endpoint)}
}

// Create makes a new endpoint with a random ID and default config.
func (s *Store) Create() *Endpoint {
	e := &Endpoint{
		ID:         newID(),
		config:     DefaultConfig(),
		lastActive: time.Now(),
		subs:       make(map[chan Event]struct{}),
	}
	s.mu.Lock()
	s.endpoints[e.ID] = e
	s.mu.Unlock()
	return e
}

// Get returns the endpoint with the given ID, or nil.
func (s *Store) Get(id string) *Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endpoints[id]
}

// StartJanitor expires endpoints idle for longer than ttl, checking every
// interval, until stop is closed.
func (s *Store) StartJanitor(ttl, interval time.Duration, stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.expire(ttl)
			}
		}
	}()
}

func (s *Store) expire(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.endpoints {
		e.mu.Lock()
		idle := e.lastActive.Before(cutoff) && len(e.subs) == 0
		e.mu.Unlock()
		if idle {
			delete(s.endpoints, id)
		}
	}
}

func newID() string {
	b := make([]byte, IDLength)
	max := big.NewInt(int64(len(idAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand failure is unrecoverable
		}
		b[i] = idAlphabet[n.Int64()]
	}
	return string(b)
}
