// Package server implements the HTTP API, webhook receiver and SPA hosting.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
	"github.com/stephenc-ori/webhook-test-endpoint/web"
)

// Server routes requests to the landing page, per-endpoint SPA and API.
type Server struct {
	store       *store.Store
	mux         *http.ServeMux
	caPEM       []byte // CA certificate offered for download; empty unless self-signed TLS
	proxySecret string // gates the pass-through proxy feature (see checkProxySecret)
	proxyClient *http.Client
}

// New builds a Server around the given store. caPEM, when non-nil, is served
// at /ca.pem so webhook senders can validate a self-signed server certificate.
// proxySecret gates the pass-through proxy feature: relaying to a destination
// requires it in the X-Proxy-Secret header.
func New(st *store.Store, caPEM []byte, proxySecret string) *Server {
	s := &Server{
		store:       st,
		mux:         http.NewServeMux(),
		caPEM:       caPEM,
		proxySecret: proxySecret,
		proxyClient: &http.Client{Timeout: 15 * time.Second},
	}

	s.mux.HandleFunc("GET /{$}", s.handleLanding)
	s.mux.HandleFunc("POST /new", s.handleNew)
	// Assets live at fixed top-level paths: wildcard patterns like
	// "GET /static/{file}" are ambiguous against "/{id}/hook" in ServeMux.
	s.mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, "app.js", "text/javascript; charset=utf-8")
	})
	s.mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		serveEmbedded(w, "style.css", "text/css; charset=utf-8")
	})
	s.mux.HandleFunc("GET /ca.pem", s.handleCAPEM)
	s.mux.HandleFunc("GET /{id}/{$}", s.handleApp)
	s.mux.HandleFunc("/{id}/hook", s.handleHook)
	s.mux.HandleFunc("GET /{id}/api/events", s.handleEvents)
	s.mux.HandleFunc("DELETE /{id}/api/events", s.handleClearEvents)
	s.mux.HandleFunc("GET /{id}/api/events/{eid}/download", s.handleDownloadEvent)
	s.mux.HandleFunc("POST /{id}/api/events/{eid}/redeliver", s.handleRedeliver)
	s.mux.HandleFunc("POST /{id}/api/deliver", s.handleDeliver)
	s.mux.HandleFunc("GET /{id}/api/config", s.handleGetConfig)
	s.mux.HandleFunc("PUT /{id}/api/config", s.handlePutConfig)
	s.mux.HandleFunc("GET /{id}/api/stream", s.handleStream)

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	serveEmbedded(w, "index.html", "text/html; charset=utf-8")
}

func (s *Server) handleCAPEM(w http.ResponseWriter, r *http.Request) {
	if len(s.caPEM) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="webhook-test-endpoint-ca.pem"`)
	w.Write(s.caPEM)
}

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request) {
	e := s.store.Create()
	http.Redirect(w, r, "/"+e.ID+"/", http.StatusSeeOther)
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	if s.endpoint(w, r) == nil {
		return
	}
	serveEmbedded(w, "app.html", "text/html; charset=utf-8")
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	writeJSON(w, e.Events())
}

func (s *Server) handleClearEvents(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	e.ClearEvents()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	writeJSON(w, e.Config())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	var c store.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&c); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateConfig(&c); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Enabling the pass-through proxy turns the endpoint into an outbound HTTP
	// client, so it is gated by the server secret.
	if c.ProxyEnabled && !s.requireProxySecret(w, r) {
		return
	}
	e.SetConfig(c)
	writeJSON(w, c)
}

// endpoint resolves the {id} path value, writing a 404 page and returning nil
// if it does not exist. A validly shaped ID that is not live (e.g. from
// before a server restart) is transparently re-created.
func (s *Server) endpoint(w http.ResponseWriter, r *http.Request) *store.Endpoint {
	e := s.store.GetOrCreate(r.PathValue("id"))
	if e == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Not found</title>`+
			`<body style="font-family:system-ui;padding:3rem"><h1>Endpoint not found</h1>`+
			`<p>This endpoint does not exist or has expired.</p>`+
			`<p><a href="/">Generate a new one</a></p>`)
		return nil
	}
	return e
}

func validateConfig(c *store.Config) error {
	switch c.AuthMode {
	case "", "none":
		c.AuthMode = "none"
	case "basic":
		if c.BasicUser == "" && c.BasicPass == "" {
			return fmt.Errorf("basic auth requires a username or password")
		}
	case "bearer":
		if c.BearerToken == "" {
			return fmt.Errorf("bearer auth requires a token")
		}
	default:
		return fmt.Errorf("invalid authMode %q", c.AuthMode)
	}
	switch c.FailureMode {
	case "":
		c.FailureMode = "reject_log"
	case "reject_log", "reject_silent", "accept_mark":
	default:
		return fmt.Errorf("invalid failureMode %q", c.FailureMode)
	}
	if c.SigEnabled {
		if c.SigSecret == "" {
			return fmt.Errorf("signature verification requires a secret")
		}
		if strings.TrimSpace(c.SigHeader) == "" {
			c.SigHeader = "X-Hub-Signature-256"
		}
	}
	if c.RespStatus == 0 {
		c.RespStatus = http.StatusOK
	}
	if c.RespStatus < 100 || c.RespStatus > 599 {
		return fmt.Errorf("invalid respStatus %d", c.RespStatus)
	}
	if c.RespContentType == "" {
		c.RespContentType = "application/json"
	}
	if c.ProxyEnabled {
		u, err := url.Parse(strings.TrimSpace(c.ProxyURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("proxy requires an absolute http(s) destination URL")
		}
		c.ProxyURL = u.String()
	}
	return nil
}

// handleDownloadEvent serves a retained event as a Bruno (.bru) file so it can
// be inspected, edited or re-imported into an API client.
func (s *Server) handleDownloadEvent(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	ev, ok := s.event(w, r, e)
	if !ok {
		return
	}
	hookURL := "https://" + r.Host + "/" + e.ID + "/hook"
	if r.TLS == nil {
		hookURL = "http://" + r.Host + "/" + e.ID + "/hook"
	}
	name := eventName(e.ID, ev)
	doc := encodeBruno(name, hookURL, withoutHopHeaders(messageFromEvent(ev)))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name+".bru"))
	io.WriteString(w, doc)
}

// handleRedeliver relays a single retained event to the configured proxy
// destination and reports the destination's response.
func (s *Server) handleRedeliver(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	if !s.requireProxySecret(w, r) {
		return
	}
	cfg := e.Config()
	if !proxyEnabled(cfg) {
		http.Error(w, "pass-through proxy is not enabled for this endpoint", http.StatusConflict)
		return
	}
	ev, ok := s.event(w, r, e)
	if !ok {
		return
	}
	res := s.forward(cfg, ev.Method, ev.Body, ev.Headers)
	e.Touch()
	writeJSON(w, res)
}

// handleDeliver accepts an uploaded message (a Bruno .bru document, or a raw
// body when Content-Type is not a .bru) and relays it to the configured proxy
// destination.
func (s *Server) handleDeliver(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	if !s.requireProxySecret(w, r) {
		return
	}
	cfg := e.Config()
	if !proxyEnabled(cfg) {
		http.Error(w, "pass-through proxy is not enabled for this endpoint", http.StatusConflict)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "reading upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	msg, err := decodeBruno(string(raw))
	if err != nil {
		// Not a .bru document: treat the upload as a raw body, carrying its
		// own Content-Type through to the destination.
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		msg = message{Method: http.MethodPost, Headers: http.Header{"Content-Type": {ct}}, Body: string(raw)}
	}
	res := s.forward(cfg, msg.Method, msg.Body, msg.Headers)
	e.Touch()
	writeJSON(w, res)
}

// event resolves the {eid} path value against the endpoint, writing a 404 and
// returning ok=false if it is not (or no longer) retained.
func (s *Server) event(w http.ResponseWriter, r *http.Request, e *store.Endpoint) (store.Event, bool) {
	id, err := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return store.Event{}, false
	}
	ev, ok := e.Event(id)
	if !ok {
		http.Error(w, "event not found or no longer retained", http.StatusNotFound)
		return store.Event{}, false
	}
	return ev, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}

func serveEmbedded(w http.ResponseWriter, name, contentType string) {
	b, err := web.Files.ReadFile(name)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(b)
}
