package index

import (
	"fmt"
	"strings"
	"testing"
)

func mustParse(t *testing.T, raw string) *File {
	t.Helper()
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func requireClean(t *testing.T, issues []Issue) {
	t.Helper()
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
}

func requireIssue(t *testing.T, issues []Issue, path, msg string) {
	t.Helper()
	for _, iss := range issues {
		if iss.Path == path && strings.Contains(iss.Message, msg) {
			return
		}
	}
	t.Fatalf("issues = %+v, want path %q containing %q", issues, path, msg)
}

func replaceField(old, next string) string {
	return strings.Replace(goldenTOML(), old, next, 1)
}

func TestLintGoldenIsClean(t *testing.T) {
	requireClean(t, Lint(mustParse(t, goldenTOML())))
}

func TestLintRules(t *testing.T) {
	for _, tt := range lintRuleCases {
		t.Run(tt.name, func(t *testing.T) {
			requireIssue(t, Lint(mustParse(t, tt.raw)), tt.path, tt.msg)
		})
	}
}

func TestLintFooDotDotBarSrcOK(t *testing.T) {
	raw := replaceField(`src = "just"`, `src = "foo..bar"`)
	requireClean(t, Lint(mustParse(t, raw)))
}

func TestLintAllowedTemplatesInPath(t *testing.T) {
	raw := replaceURL("https://github.com/casey/just/releases/download/{{version}}/just-{{version}}-{{arch}}-{{os}}.tar.gz")
	requireClean(t, Lint(mustParse(t, raw)))
}

func TestLintDuplicateDest(t *testing.T) {
	raw := goldenTOML() + extraFile("just.1", "bin/just", 0o644)
	requireIssue(t, Lint(mustParse(t, raw)),
		"versions.1.56.0.artifacts.darwin/arm64.files[1].dest",
		"unique")
}

func TestLintOverlappingDest(t *testing.T) {
	raw := goldenTOML() + extraFile("extra", "bin/just/extra", 0o644)
	requireIssue(t, Lint(mustParse(t, raw)),
		"versions.1.56.0.artifacts.darwin/arm64.files[1].dest",
		"overlap")
}

func TestLintArbitraryTreeDigestNeedsNoFiles(t *testing.T) {
	// Presence and spelling only. A well-formed digest is
	// accepted with nothing on disk.
	raw := replaceField(
		`tree_digest = "`+goldenDigest+`"`,
		`tree_digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"`,
	)
	requireClean(t, Lint(mustParse(t, raw)))
}

func TestLintIssuesAreSorted(t *testing.T) {
	raw := goldenTOML() + `
[versions."1.56.0".artifacts."linux/amd64"]
url = "http://evil.example/x"
format = "nope"
sha256 = "zz"
tree_digest = "zz"
hash_source = "nope"

[[versions."1.56.0".artifacts."linux/amd64".files]]
src = "just"
dest = "bin/just"
mode = 0o755
`
	issues := Lint(mustParse(t, raw))
	if len(issues) < 2 {
		t.Fatalf("issues = %+v, want at least two", issues)
	}
	for i := 1; i < len(issues); i++ {
		prev, next := issues[i-1], issues[i]
		if prev.Path > next.Path ||
			(prev.Path == next.Path && prev.Message > next.Message) {
			t.Fatalf("unsorted: %+v then %+v", prev, next)
		}
	}
}

func TestLintDiffAppendVersionClean(t *testing.T) {
	old := mustParse(t, goldenTOML())
	newer := mustParse(t, goldenTOML()+extraArtifact(
		"1.57.0", "darwin/arm64",
		"https://github.com/casey/just/releases/download/1.57.0/just.tar.gz",
	))
	newer.Package.Latest = "1.57.0"
	requireClean(t, LintDiff(old, newer))
}

func TestLintDiffChangeSHA256(t *testing.T) {
	old := mustParse(t, goldenTOML())
	newer := mustParse(t, replaceField(
		`sha256 = "`+goldenSHA+`"`,
		`sha256 = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"`,
	))
	requireIssue(t, LintDiff(old, newer),
		"versions.1.56.0.artifacts.darwin/arm64.sha256",
		"immutable")
}

func TestLintDiffDropVersion(t *testing.T) {
	old := mustParse(t, goldenTOML()+extraArtifact(
		"1.57.0", "darwin/arm64",
		"https://github.com/casey/just/releases/download/1.57.0/just.tar.gz",
	))
	newer := mustParse(t, goldenTOML())
	requireIssue(t, LintDiff(old, newer), "versions.1.57.0", "removed")
}

func TestLintDiffAddPlatformToOldVersion(t *testing.T) {
	old := mustParse(t, goldenTOML())
	newer := mustParse(t, goldenTOML()+extraArtifact(
		"1.56.0", "linux/amd64",
		"https://github.com/casey/just/releases/download/1.56.0/just-linux.tar.gz",
	))
	requireIssue(t, LintDiff(old, newer),
		"versions.1.56.0.artifacts.linux/amd64",
		"added")
}

func TestLintDiffDropAttestation(t *testing.T) {
	old := mustParse(t, goldenTOML())
	newer := mustParse(t, replaceField("attestation = true\n", ""))
	requireIssue(t, LintDiff(old, newer),
		"versions.1.56.0.artifacts.darwin/arm64.attestation",
		"removed")
}

func TestLintDiffRenamePackage(t *testing.T) {
	old := mustParse(t, goldenTOML())
	newer := mustParse(t, replaceField(`name = "just"`, `name = "justfile"`))
	requireIssue(t, LintDiff(old, newer), "package.name", "immutable")
}

func TestLintDiffReorderFiles(t *testing.T) {
	two := goldenTOML() + extraFile("just.1", "share/man/man1/just.1", 0o644)
	old := mustParse(t, two)
	newer := mustParse(t, two)
	putFiles(newer, []FileEntry{
		{Src: "just.1", Dest: "share/man/man1/just.1", Mode: 0o644},
		{Src: "just", Dest: "bin/just", Mode: 0o755},
	})
	requireIssue(t, LintDiff(old, newer),
		"versions.1.56.0.artifacts.darwin/arm64.files",
		"immutable")
}

func TestLintDiffNilOld(t *testing.T) {
	requireIssue(t, LintDiff(nil, mustParse(t, goldenTOML())), "", "required")
}

func TestLintDiffNilNew(t *testing.T) {
	requireIssue(t, LintDiff(mustParse(t, goldenTOML()), nil), "", "required")
}

func extraArtifact(ver, plat, url string) string {
	return fmt.Sprintf(`
[versions.%q.artifacts.%q]
url = %q
format = "tar.gz"
sha256 = %q
tree_digest = %q
hash_source = "computed"

[[versions.%q.artifacts.%q.files]]
src = "just"
dest = "bin/just"
mode = 0o755
`, ver, plat, url, goldenSHA, goldenDigest, ver, plat)
}

func extraFile(src, dest string, mode int) string {
	return fmt.Sprintf(`
[[versions."1.56.0".artifacts."darwin/arm64".files]]
src = %q
dest = %q
mode = 0o%o
`, src, dest, mode)
}

func putFiles(f *File, files []FileEntry) {
	ver := f.Versions["1.56.0"]
	art := ver.Artifacts["darwin/arm64"]
	art.Files = files
	ver.Artifacts["darwin/arm64"] = art
	f.Versions["1.56.0"] = ver
}

func minimalIndex(name, ver, plat string) string {
	return fmt.Sprintf(`[package]
name = %q
latest = %q

[versions.%q.artifacts.%q]
url = "https://github.com/casey/just/releases/download/1.0.0/just.tar.gz"
format = "tar.gz"
sha256 = %q
tree_digest = %q
hash_source = "computed"

[[versions.%q.artifacts.%q.files]]
src = "just"
dest = "bin/just"
mode = 0o755
`, name, ver, ver, plat, goldenSHA, goldenDigest, ver, plat)
}
