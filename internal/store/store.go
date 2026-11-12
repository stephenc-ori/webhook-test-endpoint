// Package store holds endpoints and their received events in memory.
package store

import (
	"crypto/rand"
	_ "embed"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxEvents is the number of events retained per endpoint.
const MaxEvents = 50

// IDWords is the number of dictionary words in a generated endpoint ID.
const IDWords = 3

// words is the EFF large wordlist (https://www.eff.org/dice, CC BY 3.0),
// minus its four hyphenated entries: 7772 lowercase words. Three words give
// ~38.8 bits of entropy.
//
//go:embed words.txt
var wordsFile string

var words = strings.Split(strings.TrimSpace(wordsFile), "\n")

var wordSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}()

// ValidID reports whether id has the shape of a generated endpoint ID:
// exactly IDWords dictionary words joined with hyphens.
func ValidID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) != IDWords {
		return false
	}
	for _, p := range parts {
		if _, ok := wordSet[p]; !ok {
			return false
		}
	}
	return true
}

// Event is a single received webhook request.
type Event struct {
	ID            int64       `json:"id"`
	ReceivedAt    time.Time   `json:"receivedAt"`
	Method        string      `json:"method"`
	Path          string      `json:"path"`
	Subpath       string      `json:"subpath,omitempty"` // escaped path after /{id}/hook, e.g. "/github/push"
	Headers       http.Header `json:"headers"`
	Body          string      `json:"body"`
	BodyTruncated bool        `json:"bodyTruncated"`
	RemoteAddr    string      `json:"remoteAddr"`
	AuthResult    string      `json:"authResult"` // "n/a" | "ok" | "failed"
	SigResult     string      `json:"sigResult"`  // "n/a" | "ok" | "failed"
	Rejected      bool        `json:"rejected"`

	// Proxy is what the pass-through proxy destination returned. It is nil
	// when the server did not relay the event.
	Proxy *ProxyResponse `json:"proxy,omitempty"`
}

// ProxyResponse records the relay of an event to the proxy destination.
type ProxyResponse struct {
	URL           string      `json:"url"`
	Pending       bool        `json:"pending,omitempty"` // relay still in progress
	Status        int         `json:"status,omitempty"`
	StatusText    string      `json:"statusText,omitempty"`
	Headers       http.Header `json:"headers,omitempty"`
	Body          string      `json:"body,omitempty"`
	BodyTruncated bool        `json:"bodyTruncated,omitempty"`
	Error         string      `json:"error,omitempty"`
	DurationMs    int64       `json:"durationMs"`
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

	// SubpathsEnabled makes /{id}/hook/{subpath...} accept requests too.
	// When false, a request to a subpath gets a 404 and is not recorded.
	SubpathsEnabled bool `json:"subpathsEnabled"`

	// Pass-through proxy: when enabled, every stored, non-rejected event is
	// relayed to ProxyURL, and retained events can be manually re-delivered.
	ProxyEnabled bool   `json:"proxyEnabled"`
	ProxyURL     string `json:"proxyURL"`
	// ProxyConfiguredResponse makes the endpoint send the configured response
	// (RespStatus etc.) and relay in the background. When false (the default),
	// the endpoint waits for the destination and sends its response instead.
	ProxyConfiguredResponse bool `json:"proxyConfiguredResponse"`
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

// Event returns the retained event with the given ID and true, or a zero
// Event and false if it is no longer retained.
func (e *Endpoint) Event(id int64) (Event, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.ID == id {
			return ev, true
		}
	}
	return Event{}, false
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
	subs := e.subscribersLocked()
	e.mu.Unlock()

	notify(subs, ev)
	return ev
}

// SetProxyResponse records the proxy response of a retained event and sends
// the updated event to subscribers. It does nothing if the event is no longer
// retained.
func (e *Endpoint) SetProxyResponse(id int64, p ProxyResponse) {
	e.mu.Lock()
	var updated *Event
	for i := range e.events {
		if e.events[i].ID == id {
			e.events[i].Proxy = &p
			updated = &e.events[i]
			break
		}
	}
	if updated == nil {
		e.mu.Unlock()
		return
	}
	ev := *updated
	subs := e.subscribersLocked()
	e.mu.Unlock()

	notify(subs, ev)
}

// subscribersLocked returns a snapshot of the subscribers. The caller must
// hold e.mu.
func (e *Endpoint) subscribersLocked() []chan Event {
	subs := make([]chan Event, 0, len(e.subs))
	for ch := range e.subs {
		subs = append(subs, ch)
	}
	return subs
}

// notify sends ev to each subscriber without blocking.
func notify(subs []chan Event, ev Event) {
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the hook
		}
	}
}

// ClearEvents discards all retained events. The ID counter keeps counting so
// clients that merge by event ID never see a reused ID.
func (e *Endpoint) ClearEvents() {
	e.mu.Lock()
	e.events = nil
	e.lastActive = time.Now()
	e.mu.Unlock()
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
		config:     DefaultConfig(),
		lastActive: time.Now(),
		subs:       make(map[chan Event]struct{}),
	}
	s.mu.Lock()
	for {
		e.ID = newID()
		if _, taken := s.endpoints[e.ID]; !taken {
			break
		}
	}
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

// GetOrCreate returns the endpoint with the given ID, reviving it with
// default config if id is a validly shaped ID that is not currently live —
// so URLs survive a server restart. Invalid IDs return nil.
func (s *Store) GetOrCreate(id string) *Endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.endpoints[id]; e != nil {
		return e
	}
	if !ValidID(id) {
		return nil
	}
	e := &Endpoint{
		ID:         id,
		config:     DefaultConfig(),
		lastActive: time.Now(),
		subs:       make(map[chan Event]struct{}),
	}
	s.endpoints[id] = e
	return e
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

// newID returns IDWords random dictionary words joined with hyphens,
// e.g. "abacus-zebra-canyon".
func newID() string {
	picked := make([]string, IDWords)
	max := big.NewInt(int64(len(words)))
	for i := range picked {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand failure is unrecoverable
		}
		picked[i] = words[n.Int64()]
	}
	return strings.Join(picked, "-")
}
