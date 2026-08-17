package index

import "strings"

const artPath = "versions.1.56.0.artifacts.darwin/arm64"

const goldenURL = "https://github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"

type lintRuleCase struct {
	name string
	raw  string
	path string
	msg  string
}

func replaceURL(next string) string {
	return replaceField(`url = "`+goldenURL+`"`, `url = "`+next+`"`)
}

var lintRuleCases = []lintRuleCase{
	{
		"name fetch",
		replaceField(`name = "just"`, `name = "fetch"`),
		"package.name",
		"reserved",
	},
	{
		"name starts with punctuation",
		replaceField(`name = "just"`, `name = "-just"`),
		"package.name",
		"start",
	},
	{
		"name invalid characters",
		replaceField(`name = "just"`, `name = "just/bin"`),
		"package.name",
		"invalid",
	},
	{
		"latest missing version",
		replaceField(`latest = "1.56.0"`, `latest = "9.9.9"`),
		"package.latest",
		"does not name",
	},
	{
		"version traversal",
		minimalIndex("just", "..", "darwin/arm64"),
		"versions...",
		"path component",
	},
	{
		"platform hyphen",
		replaceField(
			`[versions."1.56.0".artifacts."darwin/arm64"]`,
			`[versions."1.56.0".artifacts."darwin-arm64"]`,
		),
		"versions.1.56.0.artifacts.darwin-arm64",
		"os/arch",
	},
	{
		"format tar.zst",
		replaceField(`format = "tar.gz"`, `format = "tar.zst"`),
		artPath + ".format",
		"not allowed",
	},
	{
		"uppercase sha256",
		replaceField(
			`sha256 = "`+goldenSHA+`"`,
			`sha256 = "`+strings.ToUpper(goldenSHA)+`"`,
		),
		artPath + ".sha256",
		"lowercase",
	},
	{
		"tree_digest missing prefix",
		replaceField(
			`tree_digest = "`+goldenDigest+`"`,
			`tree_digest = "`+goldenSHA+`"`,
		),
		artPath + ".tree_digest",
		"sha256 digest",
	},
	{
		"tree_digest missing",
		replaceField(`tree_digest = "`+goldenDigest+`"`, `tree_digest = ""`),
		artPath + ".tree_digest",
		"required",
	},
	{
		"url port",
		replaceURL("https://github.com:443/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"),
		artPath + ".url",
		"port",
	},
	{
		"url query",
		replaceURL(goldenURL + "?x=1"),
		artPath + ".url",
		"query",
	},
	{
		"url fragment",
		replaceURL(goldenURL + "#x"),
		artPath + ".url",
		"fragment",
	},
	{
		"url userinfo",
		replaceURL("https://user:pass@github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"),
		artPath + ".url",
		"userinfo",
	},
	{
		"url empty userinfo",
		replaceURL("https://@github.com/casey/just/releases/download/1.56.0/just-1.56.0-aarch64-apple-darwin.tar.gz"),
		artPath + ".url",
		"userinfo",
	},
	{
		"url template in host",
		replaceURL("https://{{os}}.github.com/casey/just/releases/download/1.56.0/just.tar.gz"),
		artPath + ".url",
		"path",
	},
	{
		"url unknown template",
		replaceURL("https://github.com/casey/just/releases/download/{{foo}}/just.tar.gz"),
		artPath + ".url",
		"not allowed",
	},
	{
		"absolute src",
		replaceField(`src = "just"`, `src = "/just"`),
		artPath + ".files[0].src",
		"relative",
	},
	{
		"dotdot src",
		replaceField(`src = "just"`, `src = "../just"`),
		artPath + ".files[0].src",
		"relative",
	},
	{
		"mode 0o777",
		replaceField(`mode = 0o755`, `mode = 0o777`),
		artPath + ".files[0].mode",
		"0o644 or 0o755",
	},
	{
		"mode 0",
		replaceField(`mode = 0o755`, `mode = 0`),
		artPath + ".files[0].mode",
		"0o644 or 0o755",
	},
	{
		"attestation false",
		replaceField(`attestation = true`, `attestation = false`),
		artPath + ".attestation",
		"true",
	},
	{
		"reserved dest sidecar",
		replaceField(`dest = "bin/just"`, `dest = ".gale-deps.toml"`),
		artPath + ".files[0].dest",
		"reserved",
	},
}
