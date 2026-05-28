package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/output"
)

// BUG-4: repo add doesn't persist to config.toml. After
// adding a repo, config.toml should contain the new entry.

func TestRepoAddPersistsToConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	// addRepoToConfig is the extracted persist function.
	err := addRepoToConfig(configPath, "test-repo",
		"https://example.com/recipes")
	if err != nil {
		t.Fatalf("addRepoToConfig error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	cfg, err := config.ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("Repos length = %d, want 1", len(cfg.Repos))
	}
	if cfg.Repos[0].Name != "test-repo" {
		t.Errorf("Repos[0].Name = %q, want %q",
			cfg.Repos[0].Name, "test-repo")
	}
	if cfg.Repos[0].URL != "https://example.com/recipes" {
		t.Errorf("Repos[0].URL = %q, want %q",
			cfg.Repos[0].URL, "https://example.com/recipes")
	}
}

// BUG-5: repo remove doesn't update config.toml. After
// removing a repo, config.toml should no longer contain it.

func TestRepoRemoveUpdatesConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	initial := "[[repos]]\nname = \"test-repo\"\n" +
		"url = \"https://example.com/recipes\"\n"
	if err := os.WriteFile(
		configPath, []byte(initial), 0o644,
	); err != nil {
		t.Fatalf("writing initial config: %v", err)
	}

	err := removeRepoFromConfig(configPath, "test-repo")
	if err != nil {
		t.Fatalf("removeRepoFromConfig error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	cfg, err := config.ParseAppConfig(string(data))
	if err != nil {
		t.Fatalf("parsing config: %v", err)
	}

	if len(cfg.Repos) != 0 {
		t.Errorf("Repos length = %d, want 0", len(cfg.Repos))
	}
}

// --- gale repo update ---
//
// runRepoUpdate refreshes one named tap (or all when name is
// empty). The fetch callback is injectable so tests don't hit
// real git. Errors per-tap are warned but don't abort the loop;
// the command exits non-zero only when *every* refresh failed.

// recordingFetch returns a tapFetcher that records each name
// it is called with and returns the error mapped for that name
// (nil when absent from errs).
func recordingFetch(errs map[string]error) (tapFetcher, *[]string) {
	calls := []string{}
	fn := func(name string) error {
		calls = append(calls, name)
		return errs[name]
	}
	return fn, &calls
}

func writeReposConfig(t *testing.T, galeDir string, entries ...string) {
	t.Helper()
	body := strings.Join(entries, "\n")
	writeAppConfig(t, galeDir, body)
}

func repoEntry(name, url string) string {
	return "[[repos]]\nname = \"" + name + "\"\nurl = \"" + url + "\"\n"
}

func TestRunRepoUpdateSingleTap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "mytap", map[string]string{
		"jq": jqRecipe("1.0.0"),
	})
	writeReposConfig(t, galeDir, repoEntry("mytap", "https://x/m.git"))

	buf := &bytes.Buffer{}
	out := output.New(buf, false)
	fetch, calls := recordingFetch(nil)

	if err := runRepoUpdate(out, "mytap", fetch); err != nil {
		t.Fatalf("runRepoUpdate: %v", err)
	}
	if got := *calls; len(got) != 1 || got[0] != "mytap" {
		t.Errorf("fetch calls = %v, want [mytap]", got)
	}
	if !strings.Contains(buf.String(), "Refreshed 1 tap") {
		t.Errorf("output missing success summary: %q", buf.String())
	}
}

func TestRunRepoUpdateAllTaps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "a", map[string]string{"jq": jqRecipe("1.0.0")})
	setupTapCache(t, galeDir, "b", map[string]string{"jq": jqRecipe("2.0.0")})
	writeReposConfig(t, galeDir,
		repoEntry("a", "https://x/a.git"),
		repoEntry("b", "https://x/b.git"))

	buf := &bytes.Buffer{}
	out := output.New(buf, false)
	fetch, calls := recordingFetch(nil)

	if err := runRepoUpdate(out, "", fetch); err != nil {
		t.Fatalf("runRepoUpdate: %v", err)
	}
	got := *calls
	if len(got) != 2 {
		t.Fatalf("fetch calls = %v, want 2 calls", got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen["a"] || !seen["b"] {
		t.Errorf("fetch calls = %v, want both a and b", got)
	}
}

func TestRunRepoUpdateUnknownTap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	writeReposConfig(t, galeDir, repoEntry("a", "https://x/a.git"))

	buf := &bytes.Buffer{}
	out := output.New(buf, false)
	fetch, calls := recordingFetch(nil)

	err := runRepoUpdate(out, "ghost", fetch)
	if err == nil {
		t.Fatal("expected error for unknown tap")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error = %q, want mention of ghost", err)
	}
	if len(*calls) != 0 {
		t.Errorf("fetch should not be called for unknown tap, got %v", *calls)
	}
}

func TestRunRepoUpdatePartialFailureSucceedsOverall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "a", map[string]string{"jq": jqRecipe("1.0.0")})
	setupTapCache(t, galeDir, "b", map[string]string{"jq": jqRecipe("2.0.0")})
	writeReposConfig(t, galeDir,
		repoEntry("a", "https://x/a.git"),
		repoEntry("b", "https://x/b.git"))

	buf := &bytes.Buffer{}
	out := output.New(buf, false)
	fetch, calls := recordingFetch(map[string]error{
		"a": errors.New("network blip"),
	})

	if err := runRepoUpdate(out, "", fetch); err != nil {
		t.Fatalf("runRepoUpdate: %v", err)
	}
	if len(*calls) != 2 {
		t.Errorf("both taps should be attempted, calls = %v", *calls)
	}
	s := buf.String()
	if !strings.Contains(s, "network blip") {
		t.Errorf("expected warn line for failing tap, got: %q", s)
	}
	if !strings.Contains(s, "1 failed") {
		t.Errorf("expected '1 failed' in success summary, got: %q", s)
	}
}

func TestRunRepoUpdateAllFailReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	galeDir := filepath.Join(home, ".gale")
	setupTapCache(t, galeDir, "a", map[string]string{"jq": jqRecipe("1.0.0")})
	writeReposConfig(t, galeDir, repoEntry("a", "https://x/a.git"))

	buf := &bytes.Buffer{}
	out := output.New(buf, false)
	fetch, _ := recordingFetch(map[string]error{
		"a": errors.New("boom"),
	})

	err := runRepoUpdate(out, "", fetch)
	if err == nil {
		t.Fatal("expected error when every refresh fails")
	}
}

func TestRunRepoUpdateNoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	buf := &bytes.Buffer{}
	out := output.New(buf, false)
	fetch, calls := recordingFetch(nil)

	if err := runRepoUpdate(out, "", fetch); err != nil {
		t.Fatalf("runRepoUpdate with no config: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("no taps configured, fetch should not be called, got %v", *calls)
	}
	if !strings.Contains(buf.String(), "No repositories") {
		t.Errorf("expected 'No repositories' message, got: %q", buf.String())
	}
}
