package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
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
	cfg := e.Config()

	body, truncated := readBody(w, r)

	ev := store.Event{
		ReceivedAt:    time.Now(),
		Method:        r.Method,
		Path:          r.URL.RequestURI(),
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
		e.Add(ev)
		writeConfigured(w, cfg)
		return
	}

	reject := cfg.FailureMode == "reject_log" || cfg.FailureMode == "reject_silent"
	ev.Rejected = reject
	if cfg.FailureMode != "reject_silent" {
		e.Add(ev)
	}
	if !reject {
		writeConfigured(w, cfg)
		return
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
