package admit

import (
	"fmt"
	"strings"

	"github.com/kelp/gale/internal/index"
)

// FormatFragment writes the index artifact table for ver/plat.
// Attestation is omitted. Mode is 0o644 or 0o755.
func FormatFragment(ver, plat string, art index.Artifact) string {
	key := fmt.Sprintf("versions.%q.artifacts.%q", ver, plat)
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", key)
	fmt.Fprintf(&b, "url = %q\n", art.URL)
	fmt.Fprintf(&b, "format = %q\n", art.Format)
	fmt.Fprintf(&b, "sha256 = %q\n", art.SHA256)
	fmt.Fprintf(&b, "tree_digest = %q\n", art.TreeDigest)
	fmt.Fprintf(&b, "hash_source = %q\n", art.HashSource)
	fmt.Fprintf(&b, "strip = %d\n", art.Strip)
	for _, fe := range art.Files {
		fmt.Fprintf(&b, "\n[[%s.files]]\n", key)
		fmt.Fprintf(&b, "src = %q\n", fe.Src)
		fmt.Fprintf(&b, "dest = %q\n", fe.Dest)
		fmt.Fprintf(&b, "mode = %s\n", formatMode(fe.Mode))
	}
	return b.String()
}

func formatMode(mode int) string {
	switch mode {
	case 0o755:
		return "0o755"
	default:
		return "0o644"
	}
}
