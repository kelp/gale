package support

import (
	"strings"
	"testing"
)

func TestSubstituteDataPayloadSHA(t *testing.T) {
	p := &Payloads{
		Map: map[string]*Payload{
			"hello": {TarballPath: "/fake/hello.tar.gz", SHA256: "abc123"},
		},
	}
	env := map[string]string{
		"GHCR_URL":          "http://localhost:1234",
		"HELLO_PAYLOAD_SHA": "abc123",
	}
	getenv := func(k string) string { return env[k] }
	input := []byte("url=__GHCR_URL__ sha=__HELLO_PAYLOAD_SHA__")
	got := substituteData(getenv, input, p)
	want := "url=http://localhost:1234 sha=abc123"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstituteDataEmptyPayloads(t *testing.T) {
	p := &Payloads{Map: make(map[string]*Payload)}
	env := map[string]string{
		"GHCR_URL": "http://localhost:5678",
	}
	getenv := func(k string) string { return env[k] }
	input := []byte("url=__GHCR_URL__ sha=__HELLO_PAYLOAD_SHA__")
	got := substituteData(getenv, input, p)
	want := "url=http://localhost:5678 sha=__HELLO_PAYLOAD_SHA__"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFixtureSHA256IsStable(t *testing.T) {
	a := FixtureSHA256("hello", "1.0")
	b := FixtureSHA256("hello", "1.0")
	if a != b {
		t.Fatal("FixtureSHA256 must be deterministic")
	}
	if len(a) != 64 {
		t.Errorf("sha256 len = %d, want 64", len(a))
	}
	if FixtureSHA256("hello", "1.1") == a {
		t.Error("different versions must not share a digest")
	}
}

func TestSubstituteDataMultiplePayloads(t *testing.T) {
	p := &Payloads{
		Map: map[string]*Payload{
			"hello": {SHA256: "aaa"},
			"world": {SHA256: "bbb"},
		},
	}
	env := map[string]string{
		"GHCR_URL":          "http://host",
		"HELLO_PAYLOAD_SHA": "aaa",
		"WORLD_PAYLOAD_SHA": "bbb",
	}
	getenv := func(k string) string { return env[k] }
	input := []byte("__HELLO_PAYLOAD_SHA__ __WORLD_PAYLOAD_SHA__")
	got := string(substituteData(getenv, input, p))
	if !strings.Contains(got, "aaa") || !strings.Contains(got, "bbb") {
		t.Errorf("expected both SHAs substituted, got %q", got)
	}
}
