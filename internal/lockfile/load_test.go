package lockfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadClassifies pins the whole classification table in one
// place. Every consumer switches on Kind, so what matters is that a
// file lands in exactly one class and that no unmodelable file is
// ever classified at all.
func TestLoadClassifies(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		absent   bool
		wantKind Kind
		wantErr  error
	}{
		{
			name:     "absent file",
			absent:   true,
			wantKind: KindAbsent,
		},
		{
			name:     "legacy flat schema",
			content:  validLockTOML,
			wantKind: KindLegacy,
		},
		{
			name:     "v1 schema",
			content:  v1Fixture,
			wantKind: KindV1,
		},
		{
			name:    "unknown schema version",
			content: "version = 99\n",
			wantErr: ErrUnknownVersion,
		},
		{
			name:    "unparseable",
			content: "version = = 1\n",
			wantErr: ErrMalformed,
		},
		{
			name:    "v1 without the guard",
			content: "version = 1\n",
			wantErr: ErrDowngradeGuard,
		},
		{
			name:    "v1 with an unmodeled field",
			content: v1Fixture + "\nsurprise = true\n",
			wantErr: ErrUnknownField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gale.lock")
			if !tt.absent {
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}

			v, err := Load(path)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Load error = %v, want %v", err, tt.wantErr)
				}
				if v != nil {
					t.Errorf("Load returned a view alongside %v", tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if v.Kind != tt.wantKind {
				t.Fatalf("Kind = %v, want %v", v.Kind, tt.wantKind)
			}
		})
	}
}

// TestLoadPopulatesOnlyTheMatchingSchema checks the discriminated
// union holds: a consumer that switches on Kind and reaches for the
// other schema's field must find nil rather than a zero value it
// could mistake for an empty lock.
func TestLoadPopulatesOnlyTheMatchingSchema(t *testing.T) {
	legacy, err := Load(writeTemp(t, validLockTOML))
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if legacy.Legacy == nil || len(legacy.Legacy.Packages) != 2 {
		t.Errorf("legacy view Legacy = %+v, want 2 packages", legacy.Legacy)
	}
	if legacy.V1 != nil {
		t.Errorf("legacy view carries V1 = %+v", legacy.V1)
	}

	v1, err := Load(writeTemp(t, v1Fixture))
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if v1.Legacy != nil {
		t.Errorf("v1 view carries Legacy = %+v", v1.Legacy)
	}
	if v1.V1 == nil {
		t.Fatal("v1 view has no V1")
	}
	// Load must go through the same guard-stripping ReadV1 does, or
	// the guard would reach plan construction as a package node.
	if _, ok := v1.V1.Packages[guardKey]; ok {
		t.Errorf("Load left the guard entry in Packages")
	}
	if len(v1.V1.Packages) != 1 {
		t.Errorf("got %d package nodes, want 1", len(v1.V1.Packages))
	}

	absent, err := Load(filepath.Join(t.TempDir(), "gale.lock"))
	if err != nil {
		t.Fatalf("Load absent: %v", err)
	}
	if absent.Legacy != nil || absent.V1 != nil {
		t.Errorf("absent view carries a document: %+v", absent)
	}
}

// TestLoadDanglingSymlinkIsNotAbsent closes a fail-open path. A
// lockfile that is a symlink to a missing target makes os.ReadFile
// report ErrNotExist, exactly as a missing file does, so a naive
// absence check reports "no lock" and drops the caller into unlocked
// mode while the lock path is occupied.
//
// The design refuses to treat an unreadable lock as an absent one,
// because that is a silent downgrade of a security control. Absence
// means nothing exists at the path, and nothing else.
func TestLoadDanglingSymlinkIsNotAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.Symlink(filepath.Join(dir, "gone.lock"), path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	v, err := Load(path)
	if err == nil {
		t.Fatalf("Load succeeded with Kind = %v, want an error", v.Kind)
	}
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed so it maps to lock-unusable", err)
	}
	if v != nil {
		t.Errorf("Load returned a view alongside an error: %+v", v)
	}
}

// TestReadV1DanglingSymlinkIsNotMissing pins the same discrimination
// on the other entry point. ReadV1's contract is that a missing file
// wraps fs.ErrNotExist so callers can choose unlocked mode; an
// occupied path must not qualify, or the trap returns through whichever
// reader a later phase happens to call.
func TestReadV1DanglingSymlinkIsNotMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gale.lock")
	if err := os.Symlink(filepath.Join(dir, "gone.lock"), path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ReadV1(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, which callers read as 'no lock'", err)
	}
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

// TestLoadUnreadableFileIsNotAbsent covers readLockFile's third
// case, beside the missing file and the dangling symlink: a lock that
// exists and cannot be read at all. Load must fail rather than report
// absence, for the same reason — an unanswered question must never
// read as "nothing locked".
//
// Formerly asserted through the legacy Read, which was culled with
// gh#197; readLockFile is shared by both surviving entry points, so
// the path is unchanged.
func TestLoadUnreadableFileIsNotAbsent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("mode-0 files are readable as root")
	}
	path := filepath.Join(t.TempDir(), "gale.lock")
	if err := os.WriteFile(path,
		[]byte("[packages.jq]\nversion = \"1.7.1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	v, err := Load(path)
	if err == nil {
		t.Fatalf("Load succeeded with Kind = %v, want an error", v.Kind)
	}
	if v != nil {
		t.Errorf("Load returned a view alongside an error: %+v", v)
	}
}
