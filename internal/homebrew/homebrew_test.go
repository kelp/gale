package homebrew

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const jqFormula = `class Jq < Formula
  desc "Lightweight and flexible command-line JSON processor"
  homepage "https://jqlang.github.io/jq/"
  url "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
  sha256 "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703873deeee580c2"
  license "MIT"

  bottle do
    sha256 cellar: :any, arm64_sonoma: "abc123"
  end

  head do
    url "https://github.com/jqlang/jq.git", branch: "master"
    depends_on "autoconf" => :build
    depends_on "automake" => :build
    depends_on "libtool" => :build
  end

  depends_on "oniguruma"

  def install
    system "./configure", *std_configure_args,
                          "--disable-silent-rules",
                          "--disable-maintainer-mode"
    system "make", "install"
  end

  test do
    assert_equal "2\n", pipe_output("#{bin}/jq .bar", '{"foo":1}')
  end
end
`

const ripgrepFormula = `class Ripgrep < Formula
  desc "Search tool like grep and The Silver Searcher"
  homepage "https://github.com/BurntSushi/ripgrep"
  url "https://github.com/BurntSushi/ripgrep/archive/refs/tags/15.1.0.tar.gz"
  sha256 "046fa01a216793b8bd2750f9d68d4ad43986eb9c0d6122600f993906012972e8"
  license "Unlicense"

  depends_on "asciidoctor" => :build
  depends_on "pkgconf" => :build
  depends_on "rust" => :build
  depends_on "pcre2"

  def install
    system "cargo", "install", *std_cargo_args
  end

  test do
    system bin/"rg", "Hello"
  end
end
`

const fdFormula = `class Fd < Formula
  desc "Simple, fast and user-friendly alternative to find"
  homepage "https://github.com/sharkdp/fd"
  url "https://github.com/sharkdp/fd/archive/refs/tags/v10.4.2.tar.gz"
  sha256 "3a7e027af8c8e91c196ac259c703d78cd55c364706ddafbc66d02c326e57a456"
  license any_of: ["Apache-2.0", "MIT"]

  depends_on "rust" => :build

  def install
    system "cargo", "install", *std_cargo_args
    generate_completions_from_executable(bin/"fd", "--gen-completions")
    man1.install "doc/fd.1"
  end

  test do
    touch "test_file"
    assert_equal "test_file", shell_output("#{bin}/fd test").chomp
  end
end
`

// --- Parse metadata ---

func TestParseFormulaName(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	if f.Name != "jq" {
		t.Errorf("Name = %q, want %q", f.Name, "jq")
	}
}

func TestParseFormulaDescription(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	want := "Lightweight and flexible command-line JSON processor"
	if f.Description != want {
		t.Errorf("Description = %q", f.Description)
	}
}

func TestParseFormulaHomepage(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	if f.Homepage != "https://jqlang.github.io/jq/" {
		t.Errorf("Homepage = %q", f.Homepage)
	}
}

func TestParseFormulaLicense(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	if f.License != "MIT" {
		t.Errorf("License = %q, want %q", f.License, "MIT")
	}
}

func TestParseFormulaLicenseAnyOf(t *testing.T) {
	f, _ := ParseFormula("fd", fdFormula)
	if f.License != "Apache-2.0" {
		t.Errorf("License = %q, want %q", f.License, "Apache-2.0")
	}
}

func TestParseFormulaSourceURL(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	want := "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-1.7.1.tar.gz"
	if f.SourceURL != want {
		t.Errorf("SourceURL = %q", f.SourceURL)
	}
}

func TestParseFormulaSHA256(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	want := "478c9ca129fd2e3443fe27314b455e211e0d8c60bc8ff7df703873deeee580c2"
	if f.SHA256 != want {
		t.Errorf("SHA256 = %q", f.SHA256)
	}
}

func TestParseFormulaVersion(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	if f.Version != "1.7.1" {
		t.Errorf("Version = %q, want %q", f.Version, "1.7.1")
	}
}

func TestParseFormulaVersionFromTag(t *testing.T) {
	f, _ := ParseFormula("fd", fdFormula)
	if f.Version != "10.4.2" {
		t.Errorf("Version = %q, want %q", f.Version, "10.4.2")
	}
}

// --- Parse dependencies ---

func TestParseFormulaRuntimeDeps(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	if len(f.RuntimeDeps) != 1 || f.RuntimeDeps[0] != "oniguruma" {
		t.Errorf("RuntimeDeps = %v, want [oniguruma]", f.RuntimeDeps)
	}
}

func TestParseFormulaBuildDeps(t *testing.T) {
	f, _ := ParseFormula("ripgrep", ripgrepFormula)
	if len(f.BuildDeps) != 3 {
		t.Fatalf("BuildDeps length = %d, want 3", len(f.BuildDeps))
	}
}

func TestParseFormulaIgnoresHeadDeps(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	// autoconf/automake/libtool are in the head block — should not appear.
	if len(f.BuildDeps) != 0 {
		t.Errorf("BuildDeps = %v, want empty (head deps excluded)",
			f.BuildDeps)
	}
}

// --- Parse build steps ---

func TestParseFormulaBuildStepsAutotools(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	if len(f.BuildSteps) < 2 {
		t.Fatalf("BuildSteps length = %d, want >= 2", len(f.BuildSteps))
	}
	// First step should be configure.
	if f.BuildSteps[0] == "" {
		t.Error("first build step is empty")
	}
}

func TestParseFormulaBuildStepsCargo(t *testing.T) {
	f, _ := ParseFormula("ripgrep", ripgrepFormula)
	if len(f.BuildSteps) != 1 {
		t.Fatalf("BuildSteps length = %d, want 1", len(f.BuildSteps))
	}
	if f.BuildSteps[0] == "" {
		t.Error("build step is empty")
	}
}

func TestParseFormulaConfigureUsesPrefix(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	found := false
	for _, step := range f.BuildSteps {
		if contains(step, "${PREFIX}") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no build step contains ${PREFIX}: %v", f.BuildSteps)
	}
}

func TestParseFormulaCargoUsesPrefix(t *testing.T) {
	f, _ := ParseFormula("ripgrep", ripgrepFormula)
	found := false
	for _, step := range f.BuildSteps {
		if contains(step, "${PREFIX}") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no build step contains ${PREFIX}: %v", f.BuildSteps)
	}
}

// --- Fetch from server ---

func TestFetchFormulaRequestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Write([]byte(jqFormula))
		}))
	defer srv.Close()

	FetchFormula("jq", srv.URL)
	if gotPath != "/j/jq.rb" {
		t.Errorf("path = %q, want %q", gotPath, "/j/jq.rb")
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

// --- TOML generation ---

func TestToRecipeTOMLContainsPackageFields(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	toml := f.ToRecipeTOML()

	checks := []string{
		`name = "jq"`,
		`version = "1.7.1"`,
		`license = "MIT"`,
	}
	for _, want := range checks {
		if !contains(toml, want) {
			t.Errorf("TOML missing %q", want)
		}
	}
}

func TestToRecipeTOMLContainsSHA256(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	toml := f.ToRecipeTOML()
	if !contains(toml, "478c9ca") {
		t.Error("TOML missing SHA256")
	}
}

func TestToRecipeTOMLContainsAttribution(t *testing.T) {
	f, _ := ParseFormula("jq", jqFormula)
	toml := f.ToRecipeTOML()
	if !contains(toml, "Homebrew") {
		t.Error("TOML missing Homebrew attribution")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		len(s) >= len(substr) &&
		containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
