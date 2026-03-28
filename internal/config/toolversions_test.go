package config

import (
	"testing"
)

func TestParseToolVersions(t *testing.T) {
	input := `nodejs 20.11.0
ruby 3.3.0
# this is a comment
python 3.12.1

golang 1.21.6
`
	pkgs, err := ParseToolVersions(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 4 {
		t.Fatalf("got %d packages, want 4", len(pkgs))
	}
	if pkgs["node"] != "20.11.0" {
		t.Errorf("node = %q, want 20.11.0",
			pkgs["node"])
	}
	if pkgs["go"] != "1.21.6" {
		t.Errorf("go = %q, want 1.21.6",
			pkgs["go"])
	}
}

func TestParseToolVersionsEmpty(t *testing.T) {
	pkgs, err := ParseToolVersions("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("got %d, want 0", len(pkgs))
	}
}

func TestParseToolVersionsMultipleVersions(t *testing.T) {
	// asdf allows multiple versions; we take the first.
	input := "nodejs 20.11.0 18.19.0\n"
	pkgs, err := ParseToolVersions(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkgs["node"] != "20.11.0" {
		t.Errorf("node = %q, want 20.11.0",
			pkgs["node"])
	}
}

func TestParseToolVersionsNameMapping(t *testing.T) {
	// "golang" in .tool-versions should map to "go" in gale.
	input := "golang 1.21.6\n"
	pkgs, err := ParseToolVersions(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := pkgs["go"]; !ok {
		t.Error("expected 'golang' mapped to 'go'")
	}
}
