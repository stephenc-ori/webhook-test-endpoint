// Command webhook-test-endpoint serves a webhook testing backend with an SPA
// front end. It listens on plain HTTP by default; HTTPS is available with a
// supplied cert/key pair, a generated self-signed cert, or via Let's Encrypt
// (ACME).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stephenc-ori/webhook-test-endpoint/internal/server"
	"github.com/stephenc-ori/webhook-test-endpoint/internal/store"
	"github.com/stephenc-ori/webhook-test-endpoint/internal/tlsconf"
)

// version is overridden at release build time via
// -ldflags "-X main.version=v1.2.3".
var version = "dev"

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

type options struct {
	addr            string
	addrSet         bool
	tlsCert         string
	tlsKey          string
	tlsSelfSigned   bool
	tlsHosts        string
	acmeDomains     stringList
	acmeEmail       string
	acmeCache       string
	acmeDirectory   string
	acmeHTTPAddr    string
	proxySecretFile string
	showVersion     bool
}

func parseFlags(args []string) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("webhook-test-endpoint", flag.ContinueOnError)
	fs.StringVar(&o.addr, "addr", ":8080", "listen address (defaults to :443 in ACME mode)")
	fs.StringVar(&o.tlsCert, "tls-cert", "", "path to PEM certificate; enables HTTPS (requires -tls-key)")
	fs.StringVar(&o.tlsKey, "tls-key", "", "path to PEM private key (requires -tls-cert)")
	fs.BoolVar(&o.tlsSelfSigned, "tls-self-signed", false, "enable HTTPS with a self-signed certificate generated at startup")
	fs.StringVar(&o.tlsHosts, "tls-hosts", "localhost,127.0.0.1", "comma-separated SANs for the self-signed certificate")
	fs.Var(&o.acmeDomains, "acme-domain", "domain to obtain a Let's Encrypt certificate for; repeatable or comma-separated (enables ACME mode)")
	fs.StringVar(&o.acmeEmail, "acme-email", "", "contact email for the ACME account (expiry notices)")
	fs.StringVar(&o.acmeCache, "acme-cache", defaultACMECache(), "directory for cached ACME certificates")
	fs.StringVar(&o.acmeDirectory, "acme-directory-url", "", "ACME directory URL override (e.g. Let's Encrypt staging); default is Let's Encrypt production")
	fs.StringVar(&o.acmeHTTPAddr, "acme-http-addr", ":80", "plain-HTTP listen address for ACME HTTP-01 challenges and HTTPS redirect")
	fs.StringVar(&o.proxySecretFile, "proxy-secret-file", "", "file holding the secret that unlocks the pass-through proxy feature; overridden by the WEBHOOK_PROXY_SECRET env var, generated and logged at startup if neither is given")
	fs.BoolVar(&o.showVersion, "version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			o.addrSet = true
		}
	})

	modes := 0
	if o.tlsCert != "" || o.tlsKey != "" {
		if o.tlsCert == "" || o.tlsKey == "" {
			return nil, fmt.Errorf("-tls-cert and -tls-key must be used together")
		}
		modes++
	}
	if o.tlsSelfSigned {
		modes++
	}
	if len(o.acmeDomains) > 0 {
		modes++
	}
	if modes > 1 {
		return nil, fmt.Errorf("choose one of: -tls-cert/-tls-key, -tls-self-signed, -acme-domain")
	}
	if len(o.acmeDomains) > 0 && !o.addrSet {
		o.addr = ":443"
	}
	return o, nil
}

// resolveProxySecret decides the secret that unlocks the pass-through proxy
// feature: the WEBHOOK_PROXY_SECRET env var wins; otherwise the contents of
// secretFile if given; otherwise a fresh random secret logged to the console.
// generated reports whether the returned secret was freshly generated.
func resolveProxySecret(secretFile string) (secret string, generated bool, err error) {
	if s := strings.TrimSpace(os.Getenv("WEBHOOK_PROXY_SECRET")); s != "" {
		return s, false, nil
	}
	if secretFile != "" {
		b, err := os.ReadFile(secretFile)
		if err != nil {
			return "", false, fmt.Errorf("reading -proxy-secret-file: %w", err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", false, fmt.Errorf("-proxy-secret-file %q is empty", secretFile)
		}
		return s, false, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", false, fmt.Errorf("generating proxy secret: %w", err)
	}
	return hex.EncodeToString(b), true, nil
}

func defaultACMECache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "acme-cache"
	}
	return filepath.Join(dir, "webhook-test-endpoint", "acme")
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if opts.showVersion {
		fmt.Println("webhook-test-endpoint", version)
		return
	}

	proxySecret, generated, err := resolveProxySecret(opts.proxySecretFile)
	if err != nil {
		log.Fatal(err)
	}
	if generated {
		log.Printf("pass-through proxy secret (generated): %s", proxySecret)
	}

	st := store.New()
	st.StartJanitor(24*time.Hour, time.Hour, make(chan struct{}))
	srv := &http.Server{
		Addr:              opts.addr,
		ReadHeaderTimeout: 10 * time.Second,
	}
	switch {
	case opts.tlsCert != "":
		cfg, err := tlsconf.FromFiles(opts.tlsCert, opts.tlsKey)
		if err != nil {
			log.Fatalf("loading TLS cert: %v", err)
		}
		srv.TLSConfig = cfg
		srv.Handler = server.New(st, nil, proxySecret)
		log.Printf("listening on https://%s (supplied certificate)", opts.addr)
		log.Fatal(srv.ListenAndServeTLS("", ""))

	case opts.tlsSelfSigned:
		var hosts stringList
		_ = hosts.Set(opts.tlsHosts) // never returns an error
		cfg, pemBytes, err := tlsconf.SelfSigned(hosts)
		if err != nil {
			log.Fatalf("generating self-signed cert: %v", err)
		}
		srv.TLSConfig = cfg
		// The server offers the CA certificate at /ca.pem so senders can
		// test server certificate validation.
		srv.Handler = server.New(st, pemBytes, proxySecret)
		log.Printf("listening on https://%s (self-signed, hosts: %s; CA cert at /ca.pem)", opts.addr, hosts.String())
		log.Fatal(srv.ListenAndServeTLS("", ""))

	case len(opts.acmeDomains) > 0:
		cfg, challengeHandler := tlsconf.ACME(opts.acmeDomains, opts.acmeEmail, opts.acmeCache, opts.acmeDirectory)
		srv.TLSConfig = cfg
		srv.Handler = server.New(st, nil, proxySecret)
		go func() {
			httpSrv := &http.Server{
				Addr:              opts.acmeHTTPAddr,
				Handler:           challengeHandler,
				ReadHeaderTimeout: 10 * time.Second,
			}
			log.Printf("ACME challenge/redirect listener on http://%s", opts.acmeHTTPAddr)
			log.Fatal(httpSrv.ListenAndServe())
		}()
		log.Printf("listening on https://%s (Let's Encrypt, domains: %s)", opts.addr, opts.acmeDomains.String())
		log.Fatal(srv.ListenAndServeTLS("", ""))

	default:
		srv.Handler = server.New(st, nil, proxySecret)
		log.Printf("listening on http://%s", opts.addr)
		log.Fatal(srv.ListenAndServe())
	}
}
