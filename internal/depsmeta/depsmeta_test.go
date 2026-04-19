package depsmeta

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Metadata{Deps: []ResolvedDep{
		{Name: "openssl", Version: "3.4.1", Revision: 2},
		{Name: "zstd", Version: "1.5.6", Revision: 1},
	}}
	if err := Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !Has(dir) {
		t.Fatal("Has should be true after Write")
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read = %#v, want %#v", got, want)
	}
}

func TestHasReturnsFalseForMissingFile(t *testing.T) {
	if Has(t.TempDir()) {
		t.Fatal("Has should be false for empty dir")
	}
}

func TestReadMissingReturnsEmpty(t *testing.T) {
	got, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Deps) != 0 {
		t.Fatalf("got %d deps, want 0", len(got.Deps))
	}
}

func TestFromNamedDirsParsesRevision(t *testing.T) {
	dirs := map[string]string{
		"openssl": filepath.Join("/store", "openssl", "3.4.1-2"),
		"zstd":    filepath.Join("/store", "zstd", "1.5.6"),
		"":        filepath.Join("/store", "ignored", "1.0"),
		"empty":   "",
	}
	got := FromNamedDirs(dirs)
	want := []ResolvedDep{
		{Name: "openssl", Version: "3.4.1", Revision: 2},
		{Name: "zstd", Version: "1.5.6", Revision: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromNamedDirs = %#v, want %#v", got, want)
	}
}

func TestFromNamedDirsEmptyInputs(t *testing.T) {
	if got := FromNamedDirs(nil); got != nil {
		t.Fatalf("nil map → %#v, want nil", got)
	}
	if got := FromNamedDirs(map[string]string{}); got != nil {
		t.Fatalf("empty map → %#v, want nil", got)
	}
}
