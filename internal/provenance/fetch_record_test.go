package provenance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestWriteFetchPersistsFieldsAndStaysUnreadable(t *testing.T) {
	dir := t.TempDir()
	r := FetchRecord{
		Name:       "just",
		Version:    "1.56.0",
		SHA256:     shaJQ,
		TreeDigest: digestM,
		Method:     MethodFetch,
	}
	if err := WriteFetch(dir, r); err != nil {
		t.Fatalf("WriteFetch: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var got FetchRecord
	if err := toml.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != r {
		t.Errorf("sidecar = %+v, want %+v", got, r)
	}

	_, err = ReadUnverified(dir)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("ReadUnverified error = %v, want ErrInvalid", err)
	}

	gotFetch, err := ReadFetch(dir)
	if err != nil {
		t.Fatalf("ReadFetch: %v", err)
	}
	if gotFetch != r {
		t.Errorf("ReadFetch = %+v, want %+v", gotFetch, r)
	}
}

func TestWriteFetchRefusesIncompleteRecord(t *testing.T) {
	dir := t.TempDir()
	base := FetchRecord{
		Name:       "just",
		Version:    "1.56.0",
		SHA256:     shaJQ,
		TreeDigest: digestM,
		Method:     MethodFetch,
	}
	cases := []struct {
		name string
		mut  func(*FetchRecord)
	}{
		{"empty name", func(r *FetchRecord) { r.Name = "" }},
		{"empty version", func(r *FetchRecord) { r.Version = "" }},
		{"bad sha", func(r *FetchRecord) { r.SHA256 = "aa" }},
		{"bad digest", func(r *FetchRecord) { r.TreeDigest = shaJQ }},
		{"wrong method", func(r *FetchRecord) { r.Method = "binary" }},
		{"path name", func(r *FetchRecord) { r.Name = "foo/bar" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			if err := WriteFetch(dir, r); err == nil {
				t.Fatal("WriteFetch succeeded, want error")
			}
			if _, err := os.Stat(filepath.Join(dir, File)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("sidecar exists after refuse: %v", err)
			}
		})
	}
}

func TestWriteFetchDoesNotLeaveDigestVisibleTemp(t *testing.T) {
	dir := t.TempDir()
	r := FetchRecord{
		Name:       "just",
		Version:    "1.56.0",
		SHA256:     shaJQ,
		TreeDigest: digestM,
		Method:     MethodFetch,
	}
	if err := WriteFetch(dir, r); err != nil {
		t.Fatalf("WriteFetch: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".gale-tmp") {
			t.Errorf("digest-visible temp left behind: %s", e.Name())
		}
	}
}
