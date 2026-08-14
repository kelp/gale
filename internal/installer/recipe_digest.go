package installer

import (
	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/recipemeta"
)

// workingTreeRecipeStale reports whether a working-tree recipe no
// longer matches the digest recorded in storeDir. Registry recipes
// (FromWorkingTree false) and a nil recipe always report false, so
// the IsInstalled cache stays today's path for those installs.
//
// A missing or unreadable sidecar is a miss: the directory predates
// this metadata, and a working-tree reinstall must rebuild once to
// record the digest (gh#265).
func workingTreeRecipeStale(storeDir string, r *recipe.Recipe) bool {
	if r == nil || !r.FromWorkingTree {
		return false
	}
	rec, err := recipemeta.Read(storeDir)
	if err != nil || rec.Digest == "" || rec.Digest != r.Digest {
		return true
	}
	return false
}

// recordRecipeDigest writes the recipe fingerprint into dir before
// the commit rename. An empty digest is a no-op: registry installs
// and in-memory test recipes do not carry one.
func recordRecipeDigest(dir, digest string) error {
	if digest == "" {
		return nil
	}
	return recipemeta.Write(dir, recipemeta.Metadata{Digest: digest})
}
