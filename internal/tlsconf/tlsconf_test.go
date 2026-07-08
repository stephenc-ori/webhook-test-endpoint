package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfSigned(t *testing.T) {
	cfg, caPEM, err := SelfSigned([]string{"localhost", "127.0.0.1", "example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("got %d certificate chains, want 1", len(cfg.Certificates))
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("cert not valid for localhost: %v", err)
	}
	if err := leaf.VerifyHostname("example.test"); err != nil {
		t.Errorf("cert not valid for example.test: %v", err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("cert not valid for 127.0.0.1: %v", err)
	}
	if err := leaf.VerifyHostname("other.example"); err == nil {
		t.Error("cert should not be valid for other.example")
	}

	// The published CA PEM must verify the served chain, like a webhook
	// sender configured with it would.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("CA PEM did not parse: %q", caPEM)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
		t.Errorf("leaf does not verify against published CA: %v", err)
	}
	if strings.Count(string(caPEM), "BEGIN CERTIFICATE") != 1 {
		t.Errorf("CA PEM should contain exactly one certificate:\n%s", caPEM)
	}
}

func TestSelfSignedEndToEnd(t *testing.T) {
	cfg, caPEM, err := SelfSigned([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	ts.TLS = cfg
	ts.StartTLS()
	defer ts.Close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
	res, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("TLS request with CA-only trust failed: %v", err)
	}
	res.Body.Close()

	// Without the CA the same request must fail validation.
	plain := &http.Client{}
	if res, err := plain.Get(ts.URL); err == nil {
		res.Body.Close()
		t.Error("request without CA trust should fail certificate validation")
	}
}

func TestACMEWiring(t *testing.T) {
	cfg, handler := ACME([]string{"example.com"}, "a@b.c", t.TempDir(), "https://acme-staging-v02.api.letsencrypt.org/directory")
	if cfg == nil || cfg.GetCertificate == nil {
		t.Error("ACME tls.Config missing GetCertificate")
	}
	if handler == nil {
		t.Error("ACME challenge handler is nil")
	}
	// autocert advertises the TLS-ALPN-01 protocol.
	found := false
	for _, p := range cfg.NextProtos {
		if p == "acme-tls/1" {
			found = true
		}
	}
	if !found {
		t.Errorf("NextProtos missing acme-tls/1: %v", cfg.NextProtos)
	}
}

func TestFromFilesMissing(t *testing.T) {
	if _, err := FromFiles("nope.crt", "nope.key"); err == nil {
		t.Error("expected error for missing files")
	}
}
