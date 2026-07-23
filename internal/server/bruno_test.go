package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestBrunoRoundTrip(t *testing.T) {
	m := message{
		Method:  "POST",
		Headers: http.Header{"Content-Type": {"application/json"}, "X-Custom": {"a", "b"}},
		Body:    "{\n  \"hello\": \"world\"\n}",
	}
	doc := encodeBruno("my-request", "https://example.com/hook", m)

	got, err := decodeBruno(doc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Method != "POST" {
		t.Errorf("method = %q, want POST", got.Method)
	}
	if got.Headers.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", got.Headers.Get("Content-Type"))
	}
	if vs := got.Headers["X-Custom"]; len(vs) != 2 || vs[0] != "a" || vs[1] != "b" {
		t.Errorf("X-Custom = %v, want [a b]", vs)
	}
	if got.Body != m.Body {
		t.Errorf("body round-trip mismatch:\n got %q\nwant %q", got.Body, m.Body)
	}
}

func TestBrunoDecodeHandWritten(t *testing.T) {
	// JSON braces inside the body must not terminate the block early.
	doc := `meta {
  name: hand
}

post {
  url: https://example.com/hook
  body: json
}

headers {
  Content-Type: application/json
  Authorization: Bearer tok
}

body:json {
  {"nested": {"a": 1}, "arr": [1,2,3]}
}
`
	m, err := decodeBruno(doc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Method != "POST" {
		t.Errorf("method = %q, want POST", m.Method)
	}
	if m.Headers.Get("Authorization") != "Bearer tok" {
		t.Errorf("authorization = %q", m.Headers.Get("Authorization"))
	}
	if !strings.Contains(m.Body, `{"nested": {"a": 1}, "arr": [1,2,3]}`) {
		t.Errorf("body lost nested braces: %q", m.Body)
	}
}

func TestBrunoDecodeRejectsNonBruno(t *testing.T) {
	if _, err := decodeBruno(`{"just":"json"}`); err == nil {
		t.Error("expected error decoding plain JSON as .bru")
	}
}
