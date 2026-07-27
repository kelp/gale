package provenance

import (
	"fmt"
	"strings"

	"github.com/kelp/gale/internal/lockgraph"
)

// hexDigits is the length of a hex-encoded SHA256.
const hexDigits = 64

// validate reports whether the record is usable in full. The
// individual checks below are examples of one rule, "the record
// validates completely", not a closed list: a field added to Record
// without a check here silently stops being guarded, and an
// unguarded field is exactly how a record that looks complete
// becomes a lie.
func (r Record) validate() error {
	if err := r.validateRequired(); err != nil {
		return err
	}
	if err := r.validateIdentity(); err != nil {
		return err
	}
	if err := r.validateHashes(); err != nil {
		return err
	}
	return r.validateDeps()
}

func (r Record) validateRequired() error {
	required := []struct {
		field string
		value string
	}{
		{"name", r.Name},
		{"version", r.Version},
		{"platform", r.Platform},
		{"sha256", r.SHA256},
		{"method", r.Method},
		{"graph_digest", r.GraphDigest},
	}
	for _, f := range required {
		if f.value == "" {
			return fmt.Errorf("%w: missing %s", ErrInvalid, f.field)
		}
	}
	if r.Method != lockgraph.MethodBinary && r.Method != lockgraph.MethodSource {
		return fmt.Errorf("%w: unknown method %q", ErrInvalid, r.Method)
	}
	return nil
}

func (r Record) validateIdentity() error {
	if err := checkCanonical(r.Name, r.Version); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	// Exactly one separator: strings.Cut splits once, so a bare Cut
	// would accept "darwin/arm64/extra" and silently drop the tail
	// into GOARCH.
	goos, goarch, ok := strings.Cut(r.Platform, "/")
	if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
		return fmt.Errorf(
			"%w: platform %q is not GOOS/GOARCH", ErrInvalid, r.Platform,
		)
	}
	return nil
}

func (r Record) validateHashes() error {
	if !isHex(r.SHA256) {
		return fmt.Errorf(
			"%w: sha256 %q is not %d hex digits", ErrInvalid, r.SHA256, hexDigits,
		)
	}
	if !isDigest(r.GraphDigest) {
		return fmt.Errorf(
			"%w: graph_digest %q is not sha256:<hex>", ErrInvalid, r.GraphDigest,
		)
	}
	if r.ManifestDigest != "" && !isDigest(r.ManifestDigest) {
		return fmt.Errorf(
			"%w: manifest_digest %q is not sha256:<hex>",
			ErrInvalid, r.ManifestDigest,
		)
	}
	return nil
}

// validateDeps proves every dependency entry parses and is
// canonical, which is what makes Record.node total, and enforces
// that a binary record carries no build dependencies.
func (r Record) validateDeps() error {
	for _, list := range [][]string{r.RuntimeDeps, r.BuildDeps} {
		for _, key := range list {
			name, version, ok := strings.Cut(key, "@")
			if !ok {
				return fmt.Errorf(
					"%w: dependency %q is not name@version", ErrInvalid, key,
				)
			}
			if err := checkCanonical(name, version); err != nil {
				return fmt.Errorf("%w: %w", ErrInvalid, err)
			}
		}
	}
	// A prebuilt artifact was not produced from build dependencies
	// on this machine, and what produced it upstream is unknowable
	// here. Recording a recipe's build deps beside a fetched binary
	// would attest something never verified, so the field is empty
	// for the binary method rather than optional.
	if r.Method == lockgraph.MethodBinary && len(r.BuildDeps) > 0 {
		return fmt.Errorf(
			"%w: binary record carries build deps %v", ErrInvalid, r.BuildDeps,
		)
	}
	return nil
}

func isHex(s string) bool {
	if len(s) != hexDigits {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isDigest(s string) bool {
	rest, ok := strings.CutPrefix(s, "sha256:")
	return ok && isHex(rest)
}
