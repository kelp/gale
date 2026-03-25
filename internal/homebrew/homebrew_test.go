package homebrew

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const jqResponse = `{
  "name": "jq",
  "full_name": "jq",
  "desc": "Lightweight and flexible command-line JSON processor",
  "license": "MIT",
  "homepage": "https://jqlang.github.io/jq/",
  "versions": {
    "stable": "1.7.1",
    "head": null
  },
  "urls": {
    "stable": {
      "url": "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
    }
  },
  "dependencies": ["oniguruma"],
  "build_dependencies": ["autoconf", "automake", "libtool"]
}`

func TestFetchFormulaName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Name != "jq" {
		t.Errorf("Name = %q, want %q", f.Name, "jq")
	}
}

func TestFetchFormulaVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Version != "1.7.1" {
		t.Errorf("Version = %q, want %q", f.Version, "1.7.1")
	}
}

func TestFetchFormulaDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Description != "Lightweight and flexible command-line JSON processor" {
		t.Errorf("Description = %q", f.Description)
	}
}

func TestFetchFormulaLicense(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.License != "MIT" {
		t.Errorf("License = %q, want %q", f.License, "MIT")
	}
}

func TestFetchFormulaHomepage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Homepage != "https://jqlang.github.io/jq/" {
		t.Errorf("Homepage = %q", f.Homepage)
	}
}

func TestFetchFormulaSourceURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.SourceURL != "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz" {
		t.Errorf("SourceURL = %q", f.SourceURL)
	}
}

func TestFetchFormulaDependencies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	f, err := FetchFormula("jq", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.RuntimeDeps) != 1 || f.RuntimeDeps[0] != "oniguruma" {
		t.Errorf("RuntimeDeps = %v, want [oniguruma]", f.RuntimeDeps)
	}
	if len(f.BuildDeps) != 3 {
		t.Errorf("BuildDeps length = %d, want 3", len(f.BuildDeps))
	}
}

func TestFetchFormulaRequestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(jqResponse))
		}))
	defer srv.Close()

	FetchFormula("jq", srv.URL)
	if gotPath != "/api/formula/jq.json" {
		t.Errorf("request path = %q, want %q", gotPath, "/api/formula/jq.json")
	}
}

func TestFetchFormula404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
	defer srv.Close()

	_, err := FetchFormula("nonexistent", srv.URL)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestToRecipeTOML(t *testing.T) {
	f := &Formula{
		Name:        "jq",
		Version:     "1.7.1",
		Description: "Lightweight and flexible command-line JSON processor",
		License:     "MIT",
		Homepage:    "https://jqlang.github.io/jq/",
		SourceURL:   "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz",
		RuntimeDeps: []string{"oniguruma"},
		BuildDeps:   []string{"autoconf", "automake", "libtool"},
	}

	toml := f.ToRecipeTOML()

	if toml == "" {
		t.Fatal("expected non-empty TOML output")
	}

	// Verify key fields appear in the output.
	checks := []string{
		`name = "jq"`,
		`version = "1.7.1"`,
		`description = "Lightweight and flexible command-line JSON processor"`,
		`license = "MIT"`,
		`homepage = "https://jqlang.github.io/jq/"`,
		`runtime = ["oniguruma"]`,
	}
	for _, want := range checks {
		found := false
		for _, line := range splitLines(toml) {
			if trimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("TOML missing %q", want)
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}
