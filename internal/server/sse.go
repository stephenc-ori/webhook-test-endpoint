package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ssePadding is written once at stream start. Some proxies (notably the
// Cloudflare edge in front of quick tunnels) hold back small response bodies;
// padding past their buffer makes the first real event arrive immediately.
var ssePadding = ": " + strings.Repeat("padding ", 512) + "\n\n"

// handleStream pushes new events to the SPA as Server-Sent Events.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	e := s.endpoint(w, r)
	if e == nil {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ssePadding)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	// Pings are real events, not comments: the client can't see comments, and
	// it uses message arrival times to detect a silently buffering proxy and
	// fall back to polling.
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, "event: ping\ndata: {}\n\n")
			flusher.Flush()
		case ev := <-events:
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: webhook\ndata: %s\n\n", ev.ID, b)
			flusher.Flush()
		}
	}
}
