package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
)

// newReplyingDestination is a destination that answers with a fixed status,
// header and body, and records the request path.
func newReplyingDestination(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	paths := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Dest", "yes")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "from destination")
	}))
	t.Cleanup(srv.Close)
	return srv, paths
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProxyRelaysDestinationResponseByDefault(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst, paths := newReplyingDestination(t)
	setConfig(t, e, func(c *store.Config) { c.SubpathsEnabled = true })
	enableProxy(t, e, dst.URL)

	res := post(t, ts.URL+"/"+e.ID+"/hook/a/b", "x", nil)
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", res.StatusCode)
	}
	if got := res.Header.Get("X-Dest"); got != "yes" {
		t.Errorf("X-Dest = %q, want yes", got)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("content-type = %q, want text/plain", got)
	}
	if got := readAll(t, res); got != "from destination" {
		t.Errorf("body = %q", got)
	}
	if got := <-paths; got != "/a/b" {
		t.Errorf("destination path = %q, want /a/b", got)
	}
	if n := len(e.Events()); n != 1 {
		t.Errorf("stored %d events, want 1", n)
	}
	// The relay is synchronous: no second, background delivery.
	select {
	case p := <-paths:
		t.Errorf("unexpected second delivery to %q", p)
	default:
	}
}

func TestProxyConfiguredResponse(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst, paths := newReplyingDestination(t)
	enableProxy(t, e, dst.URL)
	setConfig(t, e, func(c *store.Config) { c.ProxyConfiguredResponse = true })

	res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if got := readAll(t, res); got != `{"status":"success"}` {
		t.Errorf("body = %q, want configured body", got)
	}
	<-paths // the background delivery still happens
}

func TestProxyRelayUnreachable(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := httptest.NewServer(http.NotFoundHandler())
	url := dst.URL
	dst.Close()
	enableProxy(t, e, url)

	res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", res.StatusCode)
	}
	if n := len(e.Events()); n != 1 {
		t.Errorf("stored %d events, want 1", n)
	}
}

func TestProxyRelayFailureModes(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst, paths := newReplyingDestination(t)
	enableProxy(t, e, dst.URL)
	setConfig(t, e, func(c *store.Config) {
		c.AuthMode = "bearer"
		c.BearerToken = "tok"
	})

	// reject_log: the sender gets 401, nothing is relayed.
	if res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("reject_log: status = %d, want 401", res.StatusCode)
	}
	select {
	case p := <-paths:
		t.Errorf("rejected request relayed to %q", p)
	default:
	}

	// accept_mark: relayed, and the sender gets the destination response.
	setConfig(t, e, func(c *store.Config) { c.FailureMode = "accept_mark" })
	res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("accept_mark: status = %d, want 202", res.StatusCode)
	}
	<-paths
}

// awaitProxy waits until the only stored event has a finished proxy response.
func awaitProxy(t *testing.T, e *store.Endpoint) *store.ProxyResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evs := e.Events(); len(evs) == 1 && evs[0].Proxy != nil && !evs[0].Proxy.Pending {
			return evs[0].Proxy
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the proxy response")
	return nil
}

func TestProxyResponseRecorded(t *testing.T) {
	for _, configured := range []bool{false, true} {
		ts, _, e := newTestServer(t)
		dst, paths := newReplyingDestination(t)
		setConfig(t, e, func(c *store.Config) { c.SubpathsEnabled = true })
		enableProxy(t, e, dst.URL)
		setConfig(t, e, func(c *store.Config) { c.ProxyConfiguredResponse = configured })

		post(t, ts.URL+"/"+e.ID+"/hook/x", "in", nil)
		<-paths
		pr := awaitProxy(t, e)
		if pr.URL != dst.URL+"/x" {
			t.Errorf("configured=%v: url = %q", configured, pr.URL)
		}
		if pr.Status != http.StatusAccepted || pr.Body != "from destination" || pr.Headers.Get("X-Dest") != "yes" {
			t.Errorf("configured=%v: proxy response = %+v", configured, pr)
		}
		if pr.Error != "" {
			t.Errorf("configured=%v: error = %q", configured, pr.Error)
		}
	}
}

func TestProxyResponseRecordsError(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := httptest.NewServer(http.NotFoundHandler())
	url := dst.URL
	dst.Close()
	enableProxy(t, e, url)

	post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	if pr := awaitProxy(t, e); pr.Error == "" || pr.Status != 0 {
		t.Errorf("proxy response = %+v, want an error and no status", pr)
	}
}

func TestProxyResponseNotRecordedWithoutProxy(t *testing.T) {
	ts, _, e := newTestServer(t)
	post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	if p := e.Events()[0].Proxy; p != nil {
		t.Errorf("proxy = %+v, want nil", p)
	}
}

func TestCappedBuffer(t *testing.T) {
	c := &cappedBuffer{max: 4}
	for _, s := range []string{"ab", "cdef", "g"} {
		if n, err := c.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if c.buf.String() != "abcd" || !c.truncated {
		t.Errorf("buf = %q, truncated = %v", c.buf.String(), c.truncated)
	}
}
