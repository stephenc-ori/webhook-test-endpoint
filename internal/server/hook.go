package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
)

// maxBody caps how much of a webhook payload is read and retained.
const maxBody = 1 << 20 // 1 MiB

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	s.receive(w, r, e, e.Config(), "")
}

// handleHookSubpath receives requests to /{id}/hook/{subpath...}. The endpoint
// accepts them only when its config enables subpaths; otherwise the request
// gets a 404 and is not recorded.
func (s *Server) handleHookSubpath(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	cfg := e.Config()
	if !cfg.SubpathsEnabled {
		http.NotFound(w, r)
		return
	}
	// Use the escaped path so that encoded characters (e.g. %2F) survive
	// the relay to the proxy destination.
	// A sender can encode the ID or "hook" (e.g. h%6Fok); ServeMux still routes
	// it here, but the escaped prefix does not match. Reject it.
	sub, ok := strings.CutPrefix(r.URL.EscapedPath(), "/"+e.ID+"/hook")
	if !ok || !strings.HasPrefix(sub, "/") || hasDotSegment(sub) {
		http.Error(w, "invalid subpath", http.StatusBadRequest)
		return
	}
	s.receive(w, r, e, cfg, sub)
}

// hasDotSegment reports whether the escaped path holds a "." or ".." segment
// after unescaping. ServeMux cleans literal dot segments, but encoded ones
// (%2e%2e) get through and would escape the proxy destination path.
func hasDotSegment(escaped string) bool {
	for _, seg := range strings.Split(escaped, "/") {
		if u, err := url.PathUnescape(seg); err != nil || u == "." || u == ".." {
			return true
		}
	}
	return false
}

// receive records a webhook request and writes the response: the configured
// one, or the response of the proxy destination (see respond).
// subpath is the escaped path after /{id}/hook, or "" for the bare hook.
func (s *Server) receive(w http.ResponseWriter, r *http.Request, e *store.Endpoint, cfg store.Config, subpath string) {

	body, truncated := readBody(w, r)

	ev := store.Event{
		ReceivedAt:    time.Now(),
		Method:        r.Method,
		Path:          r.URL.RequestURI(),
		Subpath:       subpath,
		Headers:       r.Header.Clone(),
		Body:          string(body),
		BodyTruncated: truncated,
		RemoteAddr:    r.RemoteAddr,
		AuthResult:    "n/a",
		SigResult:     "n/a",
	}

	authOK := true
	if cfg.AuthMode != "none" {
		authOK = checkAuth(r, cfg)
		ev.AuthResult = resultString(authOK)
	}
	sigOK := true
	if cfg.SigEnabled {
		// Signature covers the raw body; a truncated read cannot verify.
		sigOK = !truncated && checkSignature(r, cfg, body)
		ev.SigResult = resultString(sigOK)
	}

	if authOK && sigOK {
		s.respond(w, cfg, e, ev)
		return
	}

	// Rejected events are stored (unless silent) but never relayed.
	switch cfg.FailureMode {
	case "accept_mark":
		s.respond(w, cfg, e, ev)
		return
	case "reject_log":
		ev.Rejected = true
		e.Add(ev)
	}
	if !authOK {
		switch cfg.AuthMode {
		case "basic":
			w.Header().Set("WWW-Authenticate", `Basic realm="webhook"`)
		case "bearer":
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.Error(w, "signature verification failed", http.StatusForbidden)
}

// readBody reads up to maxBody bytes and reports whether the payload was
// larger than that.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, maxBody)
	body, err := io.ReadAll(limited)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return body, true
		}
		// Partial read on a broken connection: keep what we got.
		return body, false
	}
	return body, false
}

func checkAuth(r *http.Request, cfg store.Config) bool {
	switch cfg.AuthMode {
	case "basic":
		user, pass, ok := r.BasicAuth()
		if !ok {
			return false
		}
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.BasicUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.BasicPass)) == 1
		return userOK && passOK
	case "bearer":
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(cfg.BearerToken)) == 1
	}
	return true
}

// checkSignature verifies a GitHub-style HMAC-SHA256 payload signature:
// the header value must be "sha256=" + hex(HMAC-SHA256(secret, body)).
func checkSignature(r *http.Request, cfg store.Config, body []byte) bool {
	sig := r.Header.Get(cfg.SigHeader)
	const prefix = "sha256="
	if !strings.HasPrefix(sig, prefix) {
		return false
	}
	got, err := hex.DecodeString(sig[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(cfg.SigSecret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func writeConfigured(w http.ResponseWriter, cfg store.Config) {
	w.Header().Set("Content-Type", cfg.RespContentType)
	w.WriteHeader(cfg.RespStatus)
	io.WriteString(w, cfg.RespBody)
}

func resultString(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}
