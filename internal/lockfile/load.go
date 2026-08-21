package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// readLockFile reads the lockfile bytes, reporting absence only when
// nothing exists at path.
//
// The discrimination is the point. os.ReadFile reports ErrNotExist both
// for a missing file and for a symlink whose target is missing, and
// only the first means "no lock". Treating the second as absence drops
// the caller into unlocked mode while the lock path is occupied, which
// is the silent downgrade of a security control the design refuses
// everywhere else. os.Lstat answers for the entry itself, so a link is
// seen rather than followed.
//
// Every reader goes through here so the trap cannot return through
// whichever one a later phase happens to call, and so a writer that
// derives a document from a "missing" lock cannot rename over the
// symlink it failed to see.
//
// Absence is established only by ErrNotExist from Lstat. Any other
// Lstat error means the question was not answered, and an unanswered
// question must not read as "nothing locked".
func readLockFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("reading lock file: %w", err)
	}
	_, lerr := os.Lstat(path)
	if lerr == nil {
		return nil, false, fmt.Errorf(
			"%s: %w: the lock path exists but cannot be read, "+
				"which is most likely a symlink to a missing file",
			path, ErrMalformed,
		)
	}
	if !errors.Is(lerr, fs.ErrNotExist) {
		return nil, false, fmt.Errorf("inspecting lock path: %w", lerr)
	}
	return nil, true, nil
}

// Kind classifies which schema a lockfile on disk uses.
type Kind int

const (
	// KindAbsent means there is no lockfile: unlocked mode.
	KindAbsent Kind = iota
	// KindLegacy means the flat pre-enforcement schema, written by
	// every gale shipped before enforcement. Its entries record
	// checksums that were never verified and carry no platform
	// dimension.
	KindLegacy
	// KindV1 means the enforced schema.
	KindV1
	// KindV2 means the fetch schema.
	KindV2
)

// String names the kind, so a failed switch reports which schema it
// was handed rather than an integer.
func (k Kind) String() string {
	switch k {
	case KindAbsent:
		return "absent"
	case KindLegacy:
		return "legacy"
	case KindV1:
		return "v1"
	case KindV2:
		return "v2"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// View is a lockfile read without knowing its schema in advance.
// Exactly one of Legacy and V1 is non-nil, selected by Kind, and both
// are nil when the file is absent. They are pointers rather than
// values so a consumer that switches on the wrong Kind dereferences
// nil instead of reading a zero document it could mistake for an
// empty lock.
type View struct {
	Kind   Kind
	Legacy *LockFile
	V1     *V1
	V2     *V2
}

// Load reads a lockfile of either schema, classifying it by its
// top-level version key.
//
// A file that is present and cannot be modeled is an error and no
// view, never an empty view: an empty view reads as "nothing locked"
// and would let a consumer proceed unlocked past a lock it failed to
// understand, which is the silent downgrade the whole feature exists
// to prevent. Absence is the one case that is genuinely not a lock,
// and it is reported as KindAbsent rather than as an error so
// unlocked mode stays an ordinary path.
//
// Every rejection carries one of this package's sentinels, so the
// exit-code mapping classifies it as lock-unusable (code 4) rather
// than as an ordinary failure.
func Load(path string) (*View, error) {
	data, absent, err := readLockFile(path)
	if err != nil {
		return nil, err
	}
	if absent {
		return &View{Kind: KindAbsent}, nil
	}
	version, err := probeVersion(path, data)
	if err != nil {
		return nil, err
	}
	if version == nil {
		lf, err := decodeLegacy(data)
		if err != nil {
			// The probe already accepted the syntax, so the only way
			// to land here is a value of the wrong type — a present
			// lock that cannot be modeled, not a parse accident.
			return nil, fmt.Errorf("%s: %w: %w", path, ErrMalformed, err)
		}
		return &View{Kind: KindLegacy, Legacy: lf}, nil
	}
	if *version == SchemaV2 {
		lf, err := decodeV2(path, data)
		if err != nil {
			return nil, err
		}
		return &View{Kind: KindV2, V2: lf}, nil
	}
	if err := checkSchemaVersion(path, *version); err != nil {
		return nil, err
	}
	lf, err := decodeV1(path, data)
	if err != nil {
		return nil, err
	}
	return &View{Kind: KindV1, V1: lf}, nil
}
