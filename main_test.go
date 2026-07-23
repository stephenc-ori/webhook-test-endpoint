package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFlags(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		o, err := parseFlags(nil)
		if err != nil {
			t.Fatal(err)
		}
		if o.addr != ":8080" {
			t.Errorf("addr = %q, want :8080", o.addr)
		}
	})

	t.Run("acme defaults addr to 443", func(t *testing.T) {
		o, err := parseFlags([]string{"-acme-domain", "example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if o.addr != ":443" {
			t.Errorf("addr = %q, want :443", o.addr)
		}
	})

	t.Run("acme respects explicit addr", func(t *testing.T) {
		o, err := parseFlags([]string{"-acme-domain", "example.com", "-addr", ":8443"})
		if err != nil {
			t.Fatal(err)
		}
		if o.addr != ":8443" {
			t.Errorf("addr = %q, want :8443", o.addr)
		}
	})

	t.Run("acme domains accumulate", func(t *testing.T) {
		o, err := parseFlags([]string{"-acme-domain", "a.com,b.com", "-acme-domain", "c.com"})
		if err != nil {
			t.Fatal(err)
		}
		if len(o.acmeDomains) != 3 {
			t.Errorf("domains = %v, want 3", o.acmeDomains)
		}
	})

	t.Run("cert without key rejected", func(t *testing.T) {
		if _, err := parseFlags([]string{"-tls-cert", "c.pem"}); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("conflicting modes rejected", func(t *testing.T) {
		for _, args := range [][]string{
			{"-tls-self-signed", "-acme-domain", "a.com"},
			{"-tls-cert", "c", "-tls-key", "k", "-tls-self-signed"},
			{"-tls-cert", "c", "-tls-key", "k", "-acme-domain", "a.com"},
		} {
			if _, err := parseFlags(args); err == nil {
				t.Errorf("args %v: expected error", args)
			}
		}
	})
}

func TestResolveProxySecret(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv("WEBHOOK_PROXY_SECRET", "from-env")
		s, gen, err := resolveProxySecret("")
		if err != nil || s != "from-env" || gen {
			t.Errorf("got (%q, %v, %v), want (from-env, false, nil)", s, gen, err)
		}
	})

	t.Run("file when no env", func(t *testing.T) {
		t.Setenv("WEBHOOK_PROXY_SECRET", "")
		f := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(f, []byte("  from-file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, gen, err := resolveProxySecret(f)
		if err != nil || s != "from-file" || gen {
			t.Errorf("got (%q, %v, %v), want (from-file, false, nil)", s, gen, err)
		}
	})

	t.Run("empty file rejected", func(t *testing.T) {
		t.Setenv("WEBHOOK_PROXY_SECRET", "")
		f := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(f, []byte("   \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolveProxySecret(f); err == nil {
			t.Error("expected error for empty secret file")
		}
	})

	t.Run("generated when neither given", func(t *testing.T) {
		t.Setenv("WEBHOOK_PROXY_SECRET", "")
		s, gen, err := resolveProxySecret("")
		if err != nil || !gen || len(s) != 48 { // 24 random bytes → 48 hex chars
			t.Errorf("got (%q len %d, gen %v, err %v), want a generated 48-char secret", s, len(s), gen, err)
		}
	})
}
