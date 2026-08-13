package build

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// makeBuildWorkspace creates a fresh build workspace under
// parent and returns its path. Unlike os.MkdirTemp, the random
// suffix has a FIXED length: the workspace path is baked into
// Mach-O install names at link time, and while FixupBinaries
// rewrites the strings to @rpath, install_name_tool preserves
// each load command's original size — so two builds whose
// workspace paths differed in length would ship different
// load-command layouts and break byte reproducibility
// (gale-recipes#79). MkdirTemp's suffix varies from 1 to 10
// digits; this one is always 10 hex characters.
//
// parent is always a real directory: TmpDir() resolves ~/.gale/tmp
// or falls back to a verified system temp dir, so there is no
// empty-parent case to handle here (gh#235).
func makeBuildWorkspace(parent string) (string, error) {
	const attempts = 1000
	for range attempts {
		buf := make([]byte, 5)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("random workspace suffix: %w", err)
		}
		dir := filepath.Join(parent,
			"gale-build-"+hex.EncodeToString(buf))
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create workspace: %w", err)
		}
	}
	return "", errors.New("create workspace: name space exhausted")
}
