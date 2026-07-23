package server

import (
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
)

// hopByHop headers are connection-scoped and must not be forwarded to the
// destination; the outgoing client manages its own.
var hopByHop = map[string]struct{}{
	"Connection":        {},
	"Proxy-Connection":  {},
	"Keep-Alive":        {},
	"Te":                {},
	"Trailer":           {},
	"Transfer-Encoding": {},
	"Upgrade":           {},
	"Content-Length":    {}, // recomputed from the body we send
	"Host":              {}, // set by the destination URL
	"Accept-Encoding":   {}, // let the client negotiate its own
}

// forwardResult reports what the destination did with a relayed message.
type forwardResult struct {
	OK         bool   `json:"ok"`
	Status     int    `json:"status,omitempty"`
	StatusText string `json:"statusText,omitempty"`
	Error      string `json:"error,omitempty"`
}

// proxyEnabled reports whether cfg is configured to relay to a destination.
func proxyEnabled(cfg store.Config) bool {
	return cfg.ProxyEnabled && strings.TrimSpace(cfg.ProxyURL) != ""
}

// forward relays a message to cfg.ProxyURL and reports the outcome. It never
// returns an error to the caller — a failed delivery is a result, not a fault.
func (s *Server) forward(cfg store.Config, method, body string, headers http.Header) forwardResult {
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, cfg.ProxyURL, strings.NewReader(body))
	if err != nil {
		return forwardResult{Error: "invalid destination URL: " + err.Error()}
	}
	for name, vals := range headers {
		if _, skip := hopByHop[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		for _, v := range vals {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("X-Forwarded-By", "webhook-test-endpoint")

	res, err := s.proxyClient.Do(req)
	if err != nil {
		return forwardResult{Error: err.Error()}
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return forwardResult{
		OK:         res.StatusCode >= 200 && res.StatusCode < 400,
		Status:     res.StatusCode,
		StatusText: res.Status,
	}
}

// autoForward relays a freshly stored event to the destination in the
// background when the endpoint is configured to proxy. Rejected events are
// never forwarded. Failures are logged, not surfaced to the sender.
func (s *Server) autoForward(cfg store.Config, ev store.Event) {
	if ev.Rejected || !proxyEnabled(cfg) {
		return
	}
	dest := cfg.ProxyURL
	msg := messageFromEvent(ev)
	go func() {
		res := s.forward(cfg, msg.Method, msg.Body, msg.Headers)
		if res.Error != "" {
			log.Printf("proxy forward to %s failed: %s", dest, res.Error)
			return
		}
		log.Printf("proxy forward to %s: %s", dest, res.StatusText)
	}()
}

// checkProxySecret constant-time compares the X-Proxy-Secret header against the
// server secret. An empty server secret (never the case in normal operation,
// since one is generated at startup) denies everything.
func (s *Server) checkProxySecret(r *http.Request) bool {
	if s.proxySecret == "" {
		return false
	}
	got := r.Header.Get("X-Proxy-Secret")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.proxySecret)) == 1
}

// requireProxySecret writes a 403 and returns false when the secret is absent
// or wrong.
func (s *Server) requireProxySecret(w http.ResponseWriter, r *http.Request) bool {
	if s.checkProxySecret(r) {
		return true
	}
	http.Error(w, "proxy secret required or incorrect", http.StatusForbidden)
	return false
}

// messageFromEvent adapts a stored event into a forwardable message.
func messageFromEvent(ev store.Event) message {
	return message{
		Method:  ev.Method,
		Path:    ev.Path,
		Headers: ev.Headers,
		Body:    ev.Body,
	}
}

// withoutHopHeaders returns a copy of m whose connection-scoped headers (Host,
// Content-Length, etc.) are removed — used when exporting a message so the file
// carries only meaningful, reusable headers.
func withoutHopHeaders(m message) message {
	clean := make(http.Header, len(m.Headers))
	for name, vals := range m.Headers {
		if _, skip := hopByHop[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		clean[name] = vals
	}
	m.Headers = clean
	return m
}

// eventName is a stable, human-readable label for a downloaded event file.
func eventName(id string, ev store.Event) string {
	return fmt.Sprintf("%s-request-%d", id, ev.ID)
}
