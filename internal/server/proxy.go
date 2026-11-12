package server

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// forward relays a message to cfg.ProxyURL and reports the outcome. A non-empty
// subpath (escaped, with a leading "/") is appended to the destination path.
// It never returns an error to the caller — a failed delivery is a result, not
// a fault.
func (s *Server) forward(cfg store.Config, subpath, method, body string, headers http.Header) forwardResult {
	res, err := s.send(cfg, subpath, method, body, headers)
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

// send relays a message to cfg.ProxyURL plus subpath and returns the response
// of the destination. The caller must close the response body.
func (s *Server) send(cfg store.Config, subpath, method, body string, headers http.Header) (*http.Response, error) {
	if method == "" {
		method = http.MethodPost
	}
	dest, err := destinationURL(cfg.ProxyURL, subpath)
	if err != nil {
		return nil, fmt.Errorf("invalid destination URL: %w", err)
	}
	req, err := http.NewRequest(method, dest, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid destination URL: %w", err)
	}
	copyHeaders(req.Header, headers)
	req.Header.Set("X-Forwarded-By", "webhook-test-endpoint")
	return s.proxyClient.Do(req)
}

// copyHeaders adds all headers of src to dst, except connection-scoped ones.
func copyHeaders(dst, src http.Header) {
	for name, vals := range src {
		if _, skip := hopByHop[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		for _, v := range vals {
			dst.Add(name, v)
		}
	}
}

// respond stores an accepted event and writes the response. Without a proxy,
// the sender gets the configured response. With a proxy, the server records
// the response of the destination on the event. The sender gets that response
// too, unless cfg.ProxyConfiguredResponse is set.
func (s *Server) respond(w http.ResponseWriter, cfg store.Config, e *store.Endpoint, ev store.Event) {
	if !proxyEnabled(cfg) {
		e.Add(ev)
		writeConfigured(w, cfg)
		return
	}
	ev.Proxy = &store.ProxyResponse{URL: logDest(cfg, ev), Pending: true}
	stored := e.Add(ev)
	if cfg.ProxyConfiguredResponse {
		go s.relay(nil, cfg, e, stored)
		writeConfigured(w, cfg)
		return
	}
	s.relay(w, cfg, e, stored)
}

// relay sends a stored event to the destination and records the response on
// the event. When w is not nil, relay also writes the response of the
// destination to w, or a 502 if the destination cannot be reached.
func (s *Server) relay(w http.ResponseWriter, cfg store.Config, e *store.Endpoint, ev store.Event) {
	dest := logDest(cfg, ev)
	start := time.Now()
	pr := store.ProxyResponse{URL: dest}
	defer func() {
		pr.DurationMs = time.Since(start).Milliseconds()
		e.SetProxyResponse(ev.ID, pr)
	}()

	msg := messageFromEvent(ev)
	res, err := s.send(cfg, ev.Subpath, msg.Method, msg.Body, msg.Headers)
	if err != nil {
		pr.Error = err.Error()
		log.Printf("proxy relay to %s failed: %v", dest, err)
		if w != nil {
			http.Error(w, "proxy destination failed: "+err.Error(), http.StatusBadGateway)
		}
		return
	}
	defer res.Body.Close()
	pr.Status = res.StatusCode
	pr.StatusText = res.Status
	pr.Headers = res.Header.Clone()

	body := &cappedBuffer{max: maxBody}
	out := io.Writer(body)
	if w != nil {
		copyHeaders(w.Header(), res.Header)
		w.WriteHeader(res.StatusCode)
		out = io.MultiWriter(w, body)
	}
	_, err = io.Copy(out, res.Body)
	pr.Body = body.buf.String()
	pr.BodyTruncated = body.truncated
	if err != nil {
		pr.Error = "reading response: " + err.Error()
		log.Printf("proxy relay to %s: reading response: %v", dest, err)
		return
	}
	log.Printf("proxy relay to %s: %s", dest, res.Status)
}

// cappedBuffer keeps the first max bytes written to it and discards the rest.
// It never returns an error, so it does not stop an io.MultiWriter.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room < len(p) {
		c.truncated = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// logDest is the destination URL of ev, for log messages.
func logDest(cfg store.Config, ev store.Event) string {
	if dest, err := destinationURL(cfg.ProxyURL, ev.Subpath); err == nil {
		return dest
	}
	return cfg.ProxyURL
}

// destinationURL appends the escaped subpath to the path of base. The query
// string of base is kept.
func destinationURL(base, subpath string) (string, error) {
	if subpath == "" {
		return base, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return u.JoinPath(subpath).String(), nil
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
