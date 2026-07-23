package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
)

// destination is a stand-in for the downstream webhook listener. Received
// requests are delivered on a channel so tests can assert on them.
type destination struct {
	*httptest.Server
	got    chan *receivedReq
	status int
}

type receivedReq struct {
	method string
	body   string
	header http.Header
}

func newDestination(t *testing.T) *destination {
	t.Helper()
	d := &destination{got: make(chan *receivedReq, 8), status: http.StatusOK}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d.got <- &receivedReq{method: r.Method, body: string(b), header: r.Header.Clone()}
		w.WriteHeader(d.status)
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *destination) await(t *testing.T) *receivedReq {
	t.Helper()
	select {
	case r := <-d.got:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded request")
		return nil
	}
}

func (d *destination) awaitNone(t *testing.T) {
	t.Helper()
	select {
	case r := <-d.got:
		t.Fatalf("unexpected forward: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
}

func enableProxy(t *testing.T, e *store.Endpoint, url string) {
	t.Helper()
	setConfig(t, e, func(c *store.Config) {
		c.ProxyEnabled = true
		c.ProxyURL = url
	})
}

func TestAutoForwardOnReceive(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := newDestination(t)
	enableProxy(t, e, dst.URL)

	post(t, ts.URL+"/"+e.ID+"/hook", `{"live":1}`, map[string]string{"Content-Type": "application/json"})
	got := dst.await(t)
	if got.method != "POST" || got.body != `{"live":1}` {
		t.Errorf("forwarded request = %+v", got)
	}
	if got.header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type not forwarded: %q", got.header.Get("Content-Type"))
	}
	if got.header.Get("X-Forwarded-By") != "webhook-test-endpoint" {
		t.Errorf("marker header missing")
	}
}

func TestAutoForwardSkipsRejected(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := newDestination(t)
	setConfig(t, e, func(c *store.Config) {
		c.AuthMode = "bearer"
		c.BearerToken = "tok"
		c.ProxyEnabled = true
		c.ProxyURL = dst.URL
	})

	// reject_log stores the event but marks it rejected — must not forward.
	post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	dst.awaitNone(t)

	// accept_mark stores it un-rejected — must forward.
	setConfig(t, e, func(c *store.Config) { c.FailureMode = "accept_mark" })
	post(t, ts.URL+"/"+e.ID+"/hook", "y", nil)
	if got := dst.await(t); got.body != "y" {
		t.Errorf("accept_mark forward = %+v", got)
	}
}

func TestConfigProxyRequiresSecret(t *testing.T) {
	ts, _, e := newTestServer(t)
	base := ts.URL + "/" + e.ID + "/api/config"
	body := `{"proxyEnabled":true,"proxyURL":"https://example.com/hook"}`

	// No secret → 403.
	req, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("no secret: status = %d, want 403", res.StatusCode)
	}
	if e.Config().ProxyEnabled {
		t.Error("proxy enabled without secret")
	}

	// Correct secret → saved.
	req2, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Proxy-Secret", testSecret)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("with secret: status = %d, want 200", res2.StatusCode)
	}
	if !e.Config().ProxyEnabled {
		t.Error("proxy not enabled with correct secret")
	}
}

func TestConfigProxyValidatesURL(t *testing.T) {
	ts, _, e := newTestServer(t)
	base := ts.URL + "/" + e.ID + "/api/config"
	for _, bad := range []string{
		`{"proxyEnabled":true,"proxyURL":""}`,
		`{"proxyEnabled":true,"proxyURL":"not a url"}`,
		`{"proxyEnabled":true,"proxyURL":"ftp://example.com"}`,
	} {
		req, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(bad))
		req.Header.Set("X-Proxy-Secret", testSecret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("PUT %q: status = %d, want 400", bad, res.StatusCode)
		}
	}
}

func TestRedeliver(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := newDestination(t)
	enableProxy(t, e, dst.URL)

	// Store an event (auto-forward fires once and is drained).
	post(t, ts.URL+"/"+e.ID+"/hook", `{"n":1}`, map[string]string{"Content-Type": "application/json"})
	dst.await(t)
	id := e.Events()[0].ID

	url := ts.URL + "/" + e.ID + "/api/events/" + itoa(id) + "/redeliver"

	// Without the secret → 403, no forward.
	if res := post(t, url, "", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("no secret: status = %d, want 403", res.StatusCode)
	}
	dst.awaitNone(t)

	// With the secret → forwards and reports the destination status.
	res := post(t, url, "", map[string]string{"X-Proxy-Secret": testSecret})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("redeliver: status = %d, want 200", res.StatusCode)
	}
	var fr forwardResult
	if err := json.NewDecoder(res.Body).Decode(&fr); err != nil {
		t.Fatal(err)
	}
	if !fr.OK || fr.Status != http.StatusOK {
		t.Errorf("forwardResult = %+v", fr)
	}
	if got := dst.await(t); got.body != `{"n":1}` {
		t.Errorf("re-delivered body = %q", got.body)
	}
}

func TestRedeliverNeedsProxyEnabled(t *testing.T) {
	ts, _, e := newTestServer(t)
	post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	id := e.Events()[0].ID
	url := ts.URL + "/" + e.ID + "/api/events/" + itoa(id) + "/redeliver"
	res := post(t, url, "", map[string]string{"X-Proxy-Secret": testSecret})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409 when proxy disabled", res.StatusCode)
	}
}

func TestDeliverUpload(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := newDestination(t)
	enableProxy(t, e, dst.URL)
	url := ts.URL + "/" + e.ID + "/api/deliver"

	bru := "post {\n  url: https://ignored/\n  body: json\n}\n\nheaders {\n  Content-Type: application/json\n}\n\nbody:json {\n  {\"from\":\"upload\"}\n}\n"

	// Requires the secret.
	if res := post(t, url, bru, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("no secret: status = %d, want 403", res.StatusCode)
	}

	res := post(t, url, bru, map[string]string{"X-Proxy-Secret": testSecret, "Content-Type": "text/plain"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("deliver: status = %d, want 200", res.StatusCode)
	}
	got := dst.await(t)
	if got.method != "POST" || got.body != `{"from":"upload"}` {
		t.Errorf("delivered = %+v", got)
	}
	if got.header.Get("Content-Type") != "application/json" {
		t.Errorf("content-type not carried from .bru: %q", got.header.Get("Content-Type"))
	}
}

func TestDeliverRawBody(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := newDestination(t)
	enableProxy(t, e, dst.URL)
	url := ts.URL + "/" + e.ID + "/api/deliver"

	res := post(t, url, `{"raw":true}`, map[string]string{"X-Proxy-Secret": testSecret, "Content-Type": "application/json"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	got := dst.await(t)
	if got.body != `{"raw":true}` || got.header.Get("Content-Type") != "application/json" {
		t.Errorf("raw delivery = %+v", got)
	}
}

func TestDownloadEventBruno(t *testing.T) {
	ts, _, e := newTestServer(t)
	post(t, ts.URL+"/"+e.ID+"/hook", `{"d":1}`, map[string]string{"Content-Type": "application/json"})
	id := e.Events()[0].ID

	res, err := http.Get(ts.URL + "/" + e.ID + "/api/events/" + itoa(id) + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, ".bru") {
		t.Errorf("content-disposition = %q", cd)
	}
	b, _ := io.ReadAll(res.Body)
	doc := string(b)
	if !strings.Contains(doc, "post {") || !strings.Contains(doc, `{"d":1}`) {
		t.Errorf("download not a .bru with body: %q", doc)
	}
	// And it round-trips.
	if _, err := decodeBruno(doc); err != nil {
		t.Errorf("downloaded .bru does not parse: %v", err)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
