// Package tlsconf builds tls.Config values for the three HTTPS modes:
// supplied cert/key, self-signed, and ACME (Let's Encrypt).
package tlsconf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// SelfSigned generates an in-memory certificate hierarchy: a CA and an ECDSA
// P-256 server certificate signed by it, valid for the given hosts (DNS names
// or IP addresses) for one year. It returns the server tls.Config and the CA
// certificate PEM-encoded, so webhook senders can be pointed at the CA to
// test server certificate validation.
func SelfSigned(hosts []string) (*tls.Config, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caTmpl := x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: "webhook-test-endpoint CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTmpl, &caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	leafTmpl := x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: "webhook-test-endpoint"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			leafTmpl.IPAddresses = append(leafTmpl.IPAddresses, ip)
		} else {
			leafTmpl.DNSNames = append(leafTmpl.DNSNames, h)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	cert := tls.Certificate{
		Certificate: [][]byte{leafDER, caDER},
		PrivateKey:  leafKey,
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return &tls.Config{Certificates: []tls.Certificate{cert}}, caPEM, nil
}

func newSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return serial
}

// FromFiles loads a PEM certificate and key pair.
func FromFiles(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

// ACME sets up an autocert manager for the given domains. It returns the
// tls.Config for the HTTPS listener and an HTTP handler that must be served
// on port 80 to answer HTTP-01 challenges (it redirects everything else to
// HTTPS). directoryURL overrides the ACME directory when non-empty (e.g.
// Let's Encrypt staging); email is optional.
func ACME(domains []string, email, cacheDir, directoryURL string) (*tls.Config, http.Handler) {
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domains...),
		Cache:      autocert.DirCache(cacheDir),
		Email:      email,
	}
	if directoryURL != "" {
		m.Client = &acme.Client{DirectoryURL: directoryURL}
	}
	return m.TLSConfig(), m.HTTPHandler(nil)
}
