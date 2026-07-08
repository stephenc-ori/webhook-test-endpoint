// Package server implements the HTTP API, webhook receiver and SPA hosting.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
	"github.com/stephenc-ori/webhook-test-endpoint/web"
)

// Server routes requests to the landing page, per-endpoint SPA and API.
type Server struct {
	store *store.Store
	mux   *http.ServeMux
	caPEM []byte // CA certificate offered for download; empty unless self-signed TLS
}

// New builds a Server around the given store. caPEM, when non-nil, is served
// at /ca.pem so webhook senders can validate a self-signed server certificate.
func New(st *store.Store, caPEM []byte) *Server {
	s := &Server{store: st, mux: http.NewServeMux(), caPEM: caPEM}

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
	return nil
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
