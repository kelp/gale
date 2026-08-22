package recipe

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest is SHA-256 of the concatenation of the byte slices
// that produced a recipe.
func Digest(parts ...[]byte) string {
	var n int
	for _, p := range parts {
		n += len(p)
	}
	buf := make([]byte, 0, n)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// MarkWorkingTree records that r was loaded from a working-tree
// file and fingerprints the bytes that produced it.
func (r *Recipe) MarkWorkingTree(parts ...[]byte) {
	r.FromWorkingTree = true
	r.Digest = Digest(parts...)
}
