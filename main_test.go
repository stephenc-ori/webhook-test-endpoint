package main

import "testing"

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
