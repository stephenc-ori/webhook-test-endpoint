package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
)

func enableSubpaths(t *testing.T, e *store.Endpoint) {
	t.Helper()
	setConfig(t, e, func(c *store.Config) { c.SubpathsEnabled = true })
}

func TestSubpathDisabledByDefault(t *testing.T) {
	ts, _, e := newTestServer(t)
	for _, p := range []string{"/hook/", "/hook/a", "/hook/a/b"} {
		res := post(t, ts.URL+"/"+e.ID+p, "x", nil)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, res.StatusCode)
		}
	}
	if n := len(e.Events()); n != 0 {
		t.Errorf("stored %d events, want 0", n)
	}
}

func TestSubpathEnabled(t *testing.T) {
	ts, _, e := newTestServer(t)
	enableSubpaths(t, e)

	cases := []struct{ path, wantSub string }{
		{"/hook", ""},
		{"/hook/", "/"},
		{"/hook/github/push?x=1", "/github/push"},
		{"/hook/a%2Fb", "/a%2Fb"},
	}
	for _, c := range cases {
		res := post(t, ts.URL+"/"+e.ID+c.path, "x", nil)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", c.path, res.StatusCode)
		}
	}
	evs := e.Events()
	if len(evs) != len(cases) {
		t.Fatalf("stored %d events, want %d", len(evs), len(cases))
	}
	for i, c := range cases {
		if evs[i].Subpath != c.wantSub {
			t.Errorf("%s: subpath = %q, want %q", c.path, evs[i].Subpath, c.wantSub)
		}
		if evs[i].Path != "/"+e.ID+c.path {
			t.Errorf("%s: path = %q", c.path, evs[i].Path)
		}
	}
}

func TestSubpathRejectsEncodedSegments(t *testing.T) {
	ts, _, e := newTestServer(t)
	enableSubpaths(t, e)
	for _, p := range []string{"/hook/%2e%2e/x", "/hook/a/%2E", "/h%6Fok/x"} {
		res := post(t, ts.URL+"/"+e.ID+p, "x", nil)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", p, res.StatusCode)
		}
	}
	if n := len(e.Events()); n != 0 {
		t.Errorf("stored %d events, want 0", n)
	}
}

func TestSubpathConfigRoundTrip(t *testing.T) {
	ts, _, e := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/"+e.ID+"/api/config",
		strings.NewReader(`{"subpathsEnabled":true}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d: %s", res.StatusCode, b)
	}
	var c store.Config
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if !c.SubpathsEnabled || !e.Config().SubpathsEnabled {
		t.Errorf("subpathsEnabled not saved: response %v, stored %v", c.SubpathsEnabled, e.Config().SubpathsEnabled)
	}
}

func TestSubpathForwardedToProxy(t *testing.T) {
	ts, _, e := newTestServer(t)
	dst := newDestination(t)
	enableSubpaths(t, e)
	enableProxy(t, e, dst.URL+"/base?k=v")

	// The bare hook goes to the destination URL unchanged.
	post(t, ts.URL+"/"+e.ID+"/hook", "a", nil)
	if got := dst.await(t); got.path != "/base?k=v" {
		t.Errorf("bare hook forwarded to %q, want /base?k=v", got.path)
	}

	// A subpath is appended to the destination path; the query is kept.
	post(t, ts.URL+"/"+e.ID+"/hook/github/push", "b", nil)
	if got := dst.await(t); got.path != "/base/github/push?k=v" {
		t.Errorf("subpath forwarded to %q, want /base/github/push?k=v", got.path)
	}

	// Re-delivery uses the subpath of the stored event.
	id := e.Events()[1].ID
	res := post(t, ts.URL+"/"+e.ID+"/api/events/"+itoa(id)+"/redeliver", "",
		map[string]string{"X-Proxy-Secret": testSecret})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("redeliver: status = %d", res.StatusCode)
	}
	if got := dst.await(t); got.path != "/base/github/push?k=v" {
		t.Errorf("re-delivered to %q, want /base/github/push?k=v", got.path)
	}
}

func TestSubpathInBrunoDownload(t *testing.T) {
	ts, _, e := newTestServer(t)
	enableSubpaths(t, e)
	post(t, ts.URL+"/"+e.ID+"/hook/github/push", "x", nil)
	id := e.Events()[0].ID

	res, err := http.Get(ts.URL + "/" + e.ID + "/api/events/" + itoa(id) + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if want := "/" + e.ID + "/hook/github/push"; !strings.Contains(string(b), want) {
		t.Errorf("download does not name %s: %q", want, b)
	}
}
