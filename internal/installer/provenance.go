package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"

	"github.com/kelp/gale/internal/build"
	"github.com/kelp/gale/internal/depsmeta"
	"github.com/kelp/gale/internal/lockgraph"
	"github.com/kelp/gale/internal/provenance"
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

// commitArtifact is what an install knows about the bytes it is about
// to commit, minus the runtime edges that only the staged directory
// can name.
type commitArtifact struct {
	Name           string
	Version        string // canonical version-revision
	Method         string // lockgraph.MethodBinary or MethodSource
	SHA256         string
	ManifestDigest string
	// BuildDeps is populated for a source artifact only. A prebuilt
	// binary was not produced from build dependencies on this machine,
	// and provenance attests only what was verified here.
	BuildDeps []depsmeta.ResolvedDep
	// ClosureUnusable means the caller already knows this artifact
	// cannot be attested — a declared build dependency that did not
	// resolve. Carried on the artifact rather than returned as an
	// error because it is not one: the install proceeds, unprovenanced.
	ClosureUnusable bool
}

// recordProvenance writes a provenance record into stagingDir for the
// artifact about to be committed from it. It runs before the commit
// rename so the canonical directory never exists without its record.
//
// Runtime edges come from the staged .gale-deps.toml rather than from
// whatever the caller resolved: an archive built by `gale build` ships
// its own file and the installer keeps it, so the record must describe
// the closure the committed directory declares, not the one the
// installer would have guessed.
//
// A closure that cannot be attested leaves the artifact committed with
// no record at all. That is the design's all-or-nothing rule, not a
// failure: on a machine upgraded into provenance every dependency
// predates it, and failing those installs would make the feature
// unshippable. Convergence is bottom-up instead — a leaf earns a record
// on its next install and its dependents on theirs.
func recordProvenance(storeRoot, stagingDir string, a commitArtifact) error {
	if err := scrubStagedProvenance(stagingDir); err != nil {
		return err
	}
	if a.ClosureUnusable {
		return nil
	}
	edges, ok := stagedEdges(stagingDir, a.BuildDeps)
	if !ok {
		return nil
	}
	rec, err := provenance.New(storeRoot, lockgraph.Node{
		Name:           a.Name,
		Version:        a.Version,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Method:         a.Method,
		SHA256:         a.SHA256,
		ManifestDigest: a.ManifestDigest,
		Edges:          edges,
	})
	if err != nil {
		if errors.Is(err, lockgraph.ErrMissingDep) {
			return nil
		}
		return fmt.Errorf("build provenance for %s: %w", a.Name, err)
	}
	return provenance.Write(stagingDir, rec)
}

// scrubStagedProvenance deletes any provenance file the staged payload
// brought with it, before anything reads or writes at that path.
//
// An archive can ship .gale-provenance.toml. Left in place it survives
// every path that declines to write a record and is committed as though
// gale produced it, and the whole point of the file is that its presence
// means gale verified these bytes. A planted file is a forged record.
//
// It also protects the write. The extractor deliberately allows an
// absolute symlink to be created as-is, on the stated grounds that no
// later write follows one: its own writes use O_NOFOLLOW. provenance.Write
// uses os.WriteFile, which does not, so a symlink planted at this name
// would be followed out of the store. os.Remove unlinks the entry itself
// rather than following it, which closes that before it opens.
//
// Failing to remove it fails the install: committing a directory whose
// provenance gale does not control is worse than not installing.
func scrubStagedProvenance(stagingDir string) error {
	path := filepath.Join(stagingDir, provenance.File)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove staged provenance file: %w", err)
	}
	return nil
}

// stagedDepsPresent reports whether the staged payload already carries
// a .gale-deps.toml, deciding by the directory entry itself rather than
// by what it points at.
//
// It exists because depsmeta.Has uses os.Stat, which follows a symlink.
// An archive shipping this path as a *dangling* absolute symlink is
// reported absent, and depsmeta.Write's os.WriteFile then follows the
// link and creates the target outside the store. strictDeps cannot
// prevent that: synthesis runs first. Lstat closes it — any entry at
// all counts as present and is preserved, symlink included, and
// strictDeps then marks that closure unusable so the package commits
// without provenance instead of attesting something read from outside
// its own directory.
//
// An inspection failure other than absence fails the install: writing
// on top of an entry gale could not identify is the case this exists to
// prevent.
func stagedDepsPresent(dir string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(dir, depsmeta.File)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect staged deps metadata: %w", err)
	}
	return true, nil
}

// stagedEdges builds the artifact's edge list from the closure the
// staged directory declares plus the build dependencies the caller
// supplied. ok is false when that closure is unusable, which under the
// all-or-nothing rule means the artifact commits with no record.
func stagedEdges(
	stagingDir string, buildDeps []depsmeta.ResolvedDep,
) ([]lockgraph.Edge, bool) {
	deps, ok := strictDeps(stagingDir)
	if !ok {
		return nil, false
	}
	edges := append(
		depEdges(lockgraph.KindRuntime, deps),
		depEdges(lockgraph.KindBuild, buildDeps)...,
	)
	for _, e := range edges {
		// Screened here rather than left to provenance.New, which
		// reports a noncanonical identity as a caller error — right for
		// the artifact's own identity, wrong for a dependency's.
		if provenance.CheckIdentity(e.Name, e.Version) != nil {
			return nil, false
		}
	}
	return edges, true
}

// strictDeps reads the staged .gale-deps.toml as attestation input.
// ok is false when the file cannot be trusted to say what the package
// was built against; the caller then commits with no record, never a
// failed install.
//
// depsmeta.Read is deliberately not used. Its leniency is right for
// the farm and gc, which tolerate a partial answer, and wrong here: a
// record is a claim about a specific closure, so anything it cannot
// read exactly it must not describe at all. Concretely this rejects
// what depsmeta.Read accepts — a non-regular file (an archive may ship
// this path as a symlink, and os.ReadFile would follow it out of the
// store), unknown fields, an entry with no name or no version (an empty
// version canonicalizes to the plausible-looking "-1"), and a negative
// revision (only the absent-field zero means the pre-revision default).
func strictDeps(stagingDir string) ([]depsmeta.ResolvedDep, bool) {
	path := filepath.Join(stagingDir, depsmeta.File)
	fi, err := os.Lstat(path)
	if err != nil {
		// Absent is a zero-dep package, which is a complete answer.
		// Every install path writes this file before reaching here, so
		// in practice absence means an archive that shipped none and a
		// recipe that declares none.
		return nil, errors.Is(err, os.ErrNotExist)
	}
	if !fi.Mode().IsRegular() {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var md depsmeta.Metadata
	meta, err := toml.Decode(string(data), &md)
	if err != nil || len(meta.Undecoded()) > 0 {
		return nil, false
	}
	for _, d := range md.Deps {
		if d.Name == "" || d.Version == "" || d.Revision < 0 {
			return nil, false
		}
	}
	return md.Deps, true
}

// depEdges converts recorded dependencies into graph edges of one
// kind. A zero revision predates the field and means the bare
// directory, which is revision 1 by store convention — the same default
// recipe.Package.Full and store.SplitRevision apply. Negative revisions
// never reach here; strictDeps rejects them rather than inventing an
// identity for them.
func depEdges(kind lockgraph.Kind, deps []depsmeta.ResolvedDep) []lockgraph.Edge {
	edges := make([]lockgraph.Edge, 0, len(deps))
	for _, d := range deps {
		rev := d.Revision
		if rev < 1 {
			rev = 1
		}
		edges = append(edges, lockgraph.Edge{
			Kind:    kind,
			Name:    d.Name,
			Version: fmt.Sprintf("%s-%d", d.Version, rev),
		})
	}
	return edges
}

// sourceArtifact describes bytes this machine built. version is passed
// rather than derived: a git install's identity comes from the commit
// hash, not from the recipe's declared version. The artifact hash is
// deliberately absent — the extraction path takes it from the build
// result, so there is only ever one answer to what was produced.
func sourceArtifact(
	r *recipe.Recipe, version string, deps *build.BuildDeps,
) commitArtifact {
	buildDeps, ok := buildEdgeDeps(r, deps)
	return commitArtifact{
		Name:            r.Package.Name,
		Version:         version,
		Method:          lockgraph.MethodSource,
		BuildDeps:       buildDeps,
		ClosureUnusable: !ok,
	}
}

// buildEdgeDeps names the build dependencies a source artifact was
// produced from: the effective direct build list, implicit system
// tools included, resolved through the build environment's named dirs.
// ok is false when any declared name did not resolve.
//
// Direct, not transitive. NamedDirs holds the entire build
// environment — build and runtime, transitively — and recording all of
// it would attest a dependency set the recipe never named, and would
// move a package's digest whenever an unrelated tool deep in the
// toolchain moved. Each recorded dependency's own record names its
// closure, so nothing is lost.
//
// The completeness check is the point of returning ok.
// FromNamedDirsFiltered silently drops a name it cannot resolve, which
// would leave a record attesting fewer build edges than the recipe
// declared — a narrower claim that still looks complete. Unresolved
// means unusable, not omitted.
func buildEdgeDeps(
	r *recipe.Recipe, deps *build.BuildDeps,
) ([]depsmeta.ResolvedDep, bool) {
	effective, _ := withSystemDeps(
		r.DependenciesForPlatform(runtime.GOOS, runtime.GOARCH),
		r.Build.System,
	)
	names := dedupe(effective.Build)
	var namedDirs map[string]string
	if deps != nil {
		namedDirs = deps.NamedDirs
	}
	resolved := depsmeta.FromNamedDirsFiltered(namedDirs, names)
	if len(resolved) != len(names) {
		return nil, false
	}
	return resolved, true
}

// dedupe returns names with duplicates removed, preserving order. A
// recipe may list a build dep twice, and FromNamedDirsFiltered returns
// one entry per name, so the counts would not otherwise compare.
func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// canonicalVersion spells a store version the way provenance and the
// lockfile identify it. A git install stores under a bare commit hash,
// whose canonical identity is "<hash>-1"; store.ResolveDir bridges the
// canonical identity back to the physical directory.
func canonicalVersion(version string) string {
	if store.HasNumericRevisionSuffix(version) {
		return version
	}
	return version + "-1"
}
