package recipe

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDigestHashesConcatenatedParts(t *testing.T) {
	a := []byte("recipe")
	b := []byte("binaries")
	got := Digest(a, b)

	sum := sha256.Sum256(append(append([]byte{}, a...), b...))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("Digest = %q, want %q", got, want)
	}
}

func TestDigestEmptyIsSHA256OfNothing(t *testing.T) {
	sum := sha256.Sum256(nil)
	want := hex.EncodeToString(sum[:])
	if got := Digest(); got != want {
		t.Errorf("Digest() = %q, want %q", got, want)
	}
}

func TestMarkWorkingTreeSetsDigestAndFlag(t *testing.T) {
	r := &Recipe{}
	body := []byte("[package]\nname = \"jq\"\n")
	r.MarkWorkingTree(body)
	if !r.FromWorkingTree {
		t.Error("FromWorkingTree = false, want true")
	}
	if r.Digest != Digest(body) {
		t.Errorf("Digest = %q, want %q", r.Digest, Digest(body))
	}
}
