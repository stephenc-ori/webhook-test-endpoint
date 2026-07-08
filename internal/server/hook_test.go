package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store, *store.Endpoint) {
	t.Helper()
	st := store.New()
	e := st.Create()
	ts := httptest.NewServer(New(st, nil))
	t.Cleanup(ts.Close)
	return ts, st, e
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(t *testing.T, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestHookDefaultResponse(t *testing.T) {
	ts, _, e := newTestServer(t)
	res := post(t, ts.URL+"/"+e.ID+"/hook", `{"a":1}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	b, _ := io.ReadAll(res.Body)
	if string(b) != `{"status":"success"}` {
		t.Errorf("body = %q", b)
	}
	evs := e.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Method != "POST" || ev.Body != `{"a":1}` || ev.AuthResult != "n/a" || ev.SigResult != "n/a" || ev.Rejected {
		t.Errorf("unexpected event: %+v", ev)
	}
}

func TestHookRecordsAllMethods(t *testing.T) {
	ts, _, e := newTestServer(t)
	res, err := http.Get(ts.URL + "/" + e.ID + "/hook?probe=1")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	evs := e.Events()
	if len(evs) != 1 || evs[0].Method != "GET" {
		t.Fatalf("GET not recorded: %+v", evs)
	}
	if !strings.Contains(evs[0].Path, "probe=1") {
		t.Errorf("query string not captured: %q", evs[0].Path)
	}
}

func TestHookUnknownEndpoint(t *testing.T) {
	ts, _, _ := newTestServer(t)
	res := post(t, ts.URL+"/nonexistent0000/hook", "x", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestValidOldURLSelfCreates(t *testing.T) {
	ts, st, _ := newTestServer(t)
	// A well-formed ID unknown to this (freshly restarted) server revives.
	const id = "abacus-zebra-canyon"
	if st.Get(id) != nil {
		t.Fatal("test precondition: id must not be live")
	}
	res := post(t, ts.URL+"/"+id+"/hook", `{"revived":true}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hook to revivable URL: status = %d, want 200", res.StatusCode)
	}
	e := st.Get(id)
	if e == nil {
		t.Fatal("endpoint was not self-created")
	}
	if evs := e.Events(); len(evs) != 1 || evs[0].Body != `{"revived":true}` {
		t.Errorf("event not recorded on revived endpoint: %+v", evs)
	}

	// The SPA page also revives.
	res2, err := http.Get(ts.URL + "/veal-canal-shrug/")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("SPA on revivable URL: status = %d, want 200", res2.StatusCode)
	}
}

func setConfig(t *testing.T, e *store.Endpoint, mutate func(*store.Config)) {
	t.Helper()
	c := e.Config()
	mutate(&c)
	e.SetConfig(c)
}

func TestBasicAuth(t *testing.T) {
	ts, _, e := newTestServer(t)
	setConfig(t, e, func(c *store.Config) {
		c.AuthMode = "basic"
		c.BasicUser = "u"
		c.BasicPass = "p"
	})
	url := ts.URL + "/" + e.ID + "/hook"

	res := post(t, url, "x", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no creds: status = %d, want 401", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("WWW-Authenticate = %q", res.Header.Get("WWW-Authenticate"))
	}

	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("x"))
	req.SetBasicAuth("u", "wrong")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad pass: status = %d, want 401", res2.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, url, strings.NewReader("x"))
	req.SetBasicAuth("u", "p")
	res3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Errorf("good creds: status = %d, want 200", res3.StatusCode)
	}

	evs := e.Events()
	if len(evs) != 3 { // reject_log records failures too
		t.Fatalf("got %d events, want 3", len(evs))
	}
	if !evs[0].Rejected || evs[0].AuthResult != "failed" {
		t.Errorf("first event should be rejected auth failure: %+v", evs[0])
	}
	if evs[2].Rejected || evs[2].AuthResult != "ok" {
		t.Errorf("third event should be accepted: %+v", evs[2])
	}
}

func TestBearerAuth(t *testing.T) {
	ts, _, e := newTestServer(t)
	setConfig(t, e, func(c *store.Config) {
		c.AuthMode = "bearer"
		c.BearerToken = "tok123"
	})
	url := ts.URL + "/" + e.ID + "/hook"

	if res := post(t, url, "x", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", res.StatusCode)
	}
	if res := post(t, url, "x", map[string]string{"Authorization": "Bearer wrong"}); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", res.StatusCode)
	}
	if res := post(t, url, "x", map[string]string{"Authorization": "Bearer tok123"}); res.StatusCode != http.StatusOK {
		t.Errorf("good token: status = %d, want 200", res.StatusCode)
	}
}

func TestSignatureVerification(t *testing.T) {
	// Known vector from GitHub's webhook docs:
	// secret "It's a Secret to Everybody", payload "Hello, World!"
	const secret = "It's a Secret to Everybody"
	const payload = "Hello, World!"
	const githubSig = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if got := sign(secret, payload); got != githubSig {
		t.Fatalf("sign() = %q, want GitHub docs vector %q", got, githubSig)
	}

	ts, _, e := newTestServer(t)
	setConfig(t, e, func(c *store.Config) {
		c.SigEnabled = true
		c.SigSecret = secret
	})
	url := ts.URL + "/" + e.ID + "/hook"

	if res := post(t, url, payload, map[string]string{"X-Hub-Signature-256": githubSig}); res.StatusCode != http.StatusOK {
		t.Errorf("valid sig: status = %d, want 200", res.StatusCode)
	}
	if res := post(t, url, payload, map[string]string{"X-Hub-Signature-256": "sha256=" + strings.Repeat("0", 64)}); res.StatusCode != http.StatusForbidden {
		t.Errorf("bad sig: status = %d, want 403", res.StatusCode)
	}
	if res := post(t, url, payload, nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("missing sig: status = %d, want 403", res.StatusCode)
	}

	evs := e.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	if evs[0].SigResult != "ok" || evs[1].SigResult != "failed" || evs[2].SigResult != "failed" {
		t.Errorf("sig results: %s %s %s", evs[0].SigResult, evs[1].SigResult, evs[2].SigResult)
	}
}

func TestCustomSignatureHeader(t *testing.T) {
	ts, _, e := newTestServer(t)
	setConfig(t, e, func(c *store.Config) {
		c.SigEnabled = true
		c.SigHeader = "X-My-Signature"
		c.SigSecret = "s3cret"
	})
	url := ts.URL + "/" + e.ID + "/hook"

	if res := post(t, url, "body", map[string]string{"X-My-Signature": sign("s3cret", "body")}); res.StatusCode != http.StatusOK {
		t.Errorf("custom header valid sig: status = %d, want 200", res.StatusCode)
	}
	// Signature in the default header must not count.
	if res := post(t, url, "body", map[string]string{"X-Hub-Signature-256": sign("s3cret", "body")}); res.StatusCode != http.StatusForbidden {
		t.Errorf("sig in wrong header: status = %d, want 403", res.StatusCode)
	}
}

func TestFailureModes(t *testing.T) {
	t.Run("reject_silent", func(t *testing.T) {
		ts, _, e := newTestServer(t)
		setConfig(t, e, func(c *store.Config) {
			c.AuthMode = "bearer"
			c.BearerToken = "t"
			c.FailureMode = "reject_silent"
		})
		res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", res.StatusCode)
		}
		if n := len(e.Events()); n != 0 {
			t.Errorf("silent reject recorded %d events, want 0", n)
		}
	})

	t.Run("accept_mark", func(t *testing.T) {
		ts, _, e := newTestServer(t)
		setConfig(t, e, func(c *store.Config) {
			c.AuthMode = "bearer"
			c.BearerToken = "t"
			c.FailureMode = "accept_mark"
		})
		res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
		if res.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 (accept_mark)", res.StatusCode)
		}
		evs := e.Events()
		if len(evs) != 1 {
			t.Fatalf("got %d events, want 1", len(evs))
		}
		if evs[0].AuthResult != "failed" || evs[0].Rejected {
			t.Errorf("event should be marked failed but not rejected: %+v", evs[0])
		}
	})
}

func TestCustomResponse(t *testing.T) {
	ts, _, e := newTestServer(t)
	setConfig(t, e, func(c *store.Config) {
		c.RespStatus = http.StatusTeapot
		c.RespContentType = "text/plain"
		c.RespBody = "brewing"
	})
	res := post(t, ts.URL+"/"+e.ID+"/hook", "x", nil)
	if res.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/plain" {
		t.Errorf("content-type = %q", ct)
	}
	b, _ := io.ReadAll(res.Body)
	if string(b) != "brewing" {
		t.Errorf("body = %q", b)
	}
}

func TestConfigAPI(t *testing.T) {
	ts, _, e := newTestServer(t)
	base := ts.URL + "/" + e.ID + "/api/config"

	// Round-trip a config update.
	body := `{"authMode":"bearer","bearerToken":"tk","failureMode":"accept_mark","respStatus":202}`
	req, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("PUT status = %d: %s", res.StatusCode, b)
	}

	res2, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var got store.Config
	if err := json.NewDecoder(res2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.AuthMode != "bearer" || got.BearerToken != "tk" || got.RespStatus != 202 {
		t.Errorf("config round-trip mismatch: %+v", got)
	}
	// Defaults applied by validation.
	if got.RespContentType != "application/json" || got.RespBody != "" {
		t.Errorf("defaults not applied: %+v", got)
	}

	// Invalid configs rejected.
	for _, bad := range []string{
		`{"authMode":"bogus"}`,
		`{"authMode":"bearer"}`,     // missing token
		`{"sigEnabled":true}`,       // missing secret
		`{"failureMode":"explode"}`, // bad mode
		`{"respStatus":99}`,         // bad status
		`not json`,
	} {
		req, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(bad))
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

func TestEventsAPI(t *testing.T) {
	ts, _, e := newTestServer(t)
	post(t, ts.URL+"/"+e.ID+"/hook", "one", nil)
	post(t, ts.URL+"/"+e.ID+"/hook", "two", nil)

	res, err := http.Get(ts.URL + "/" + e.ID + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var evs []store.Event
	if err := json.NewDecoder(res.Body).Decode(&evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Body != "one" || evs[1].Body != "two" {
		t.Errorf("unexpected events: %+v", evs)
	}
}

func TestClearEvents(t *testing.T) {
	ts, _, e := newTestServer(t)
	post(t, ts.URL+"/"+e.ID+"/hook", "one", nil)
	post(t, ts.URL+"/"+e.ID+"/hook", "two", nil)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/"+e.ID+"/api/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", res.StatusCode)
	}
	if n := len(e.Events()); n != 0 {
		t.Errorf("%d events after clear, want 0", n)
	}

	// IDs keep counting after a clear so clients merging by ID see no reuse.
	post(t, ts.URL+"/"+e.ID+"/hook", "three", nil)
	evs := e.Events()
	if len(evs) != 1 || evs[0].ID != 3 {
		t.Errorf("post-clear event = %+v, want single event with ID 3", evs)
	}
}

func TestNewRedirect(t *testing.T) {
	ts, st, _ := newTestServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Post(ts.URL+"/new", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	id := strings.Trim(loc, "/")
	if st.Get(id) == nil {
		t.Errorf("redirect target %q is not a live endpoint", loc)
	}
}

func TestSPAAndLanding(t *testing.T) {
	ts, _, e := newTestServer(t)

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(b), "Generate endpoint") {
		t.Errorf("landing page wrong: %d", res.StatusCode)
	}

	res2, err := http.Get(ts.URL + "/" + e.ID + "/")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK || !strings.Contains(string(b2), "app.js") {
		t.Errorf("SPA page wrong: %d", res2.StatusCode)
	}

	res3, err := http.Get(ts.URL + "/doesnotexist000/")
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", res3.StatusCode)
	}
}

func TestCAPEMRoute(t *testing.T) {
	// Without a CA cert the route 404s.
	ts, _, _ := newTestServer(t)
	res, err := http.Get(ts.URL + "/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("no CA: status = %d, want 404", res.StatusCode)
	}

	// With one it is served as a PEM download.
	pem := []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	ts2 := httptest.NewServer(New(store.New(), pem))
	defer ts2.Close()
	res2, err := http.Get(ts2.URL + "/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res2.StatusCode)
	}
	if ct := res2.Header.Get("Content-Type"); ct != "application/x-pem-file" {
		t.Errorf("content-type = %q", ct)
	}
	b, _ := io.ReadAll(res2.Body)
	if string(b) != string(pem) {
		t.Errorf("body = %q, want the CA PEM", b)
	}
}

func TestSSEStreamDeliversEvent(t *testing.T) {
	ts, _, e := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+e.ID+"/api/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc strings.Builder
		for {
			n, err := res.Body.Read(buf)
			acc.Write(buf[:n])
			if strings.Contains(acc.String(), "event: webhook") {
				done <- acc.String()
				return
			}
			if err != nil {
				done <- acc.String()
				return
			}
		}
	}()

	// Give the handler a moment to subscribe, then fire a webhook.
	time.Sleep(100 * time.Millisecond)
	post(t, ts.URL+"/"+e.ID+"/hook", `{"live":true}`, nil)

	select {
	case s := <-done:
		if !strings.Contains(s, "event: webhook") || !strings.Contains(s, "live") {
			t.Errorf("stream output missing event payload: %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
}
