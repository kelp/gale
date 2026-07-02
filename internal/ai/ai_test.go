package ai

import (
	"errors"
	"testing"
)

func TestNewClientReturnsNonNil(t *testing.T) {
	c := NewClient("sk-test-key")
	if c == nil {
		t.Fatal("expected non-nil Client")
	}
}

func TestNewClientEmptyKeyReturnsNonNil(t *testing.T) {
	c := NewClient("")
	if c == nil {
		t.Fatal("expected non-nil Client even with empty key")
	}
}

func TestRunAgentWithoutAPIKeyReturnsErrNotConfigured(t *testing.T) {
	c := NewClient("")
	_, err := c.RunAgent("system", "user", nil, 5)
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, want ErrNotConfigured", err)
	}
}
