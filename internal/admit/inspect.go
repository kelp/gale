package admit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// Inspector runs host tools against one binary. Tests inject it.
type Inspector interface {
	CodeSign(path string) error
	DynamicLibs(path string) ([]string, error)
}

// ErrNoBinary reports a placed tree with no Mach-O or ELF.
var ErrNoBinary = errors.New("no inspectable binary")

// InspectTree fail-closes on arch, object-format, codesign, and
// non-system dynamic libraries. goos/goarch are the index platform.
func InspectTree(ctx context.Context, tree, goos, goarch string, insp Inspector) error {
	if insp == nil {
		insp = Native{}
	}
	var found bool
	err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		return inspectFile(inspectArgs{
			path: p, tree: tree, goos: goos, goarch: goarch,
			insp: insp, found: &found,
		})
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrNoBinary
	}
	return nil
}

type inspectArgs struct {
	path, tree, goos, goarch string
	insp                     Inspector
	found                    *bool
}

func inspectFile(a inspectArgs) error {
	p, tree, goos, goarch, insp, found := a.path, a.tree, a.goos, a.goarch, a.insp, a.found
	kind, arch, err := Classify(p)
	if errors.Is(err, ErrNotBinary) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("classify %s: %w", rel(tree, p), err)
	}
	if err := matchKind(goos, kind, rel(tree, p)); err != nil {
		return err
	}
	if !archAllowed(arch, goarch) {
		return fmt.Errorf("arch %s: got %s, want %s", rel(tree, p), arch, goarch)
	}
	if kind == KindMachO {
		if err := insp.CodeSign(p); err != nil {
			return fmt.Errorf("codesign %s: %w", rel(tree, p), err)
		}
	}
	libs, err := insp.DynamicLibs(p)
	if err != nil {
		return fmt.Errorf("dynamic libs %s: %w", rel(tree, p), err)
	}
	for _, lib := range libs {
		if !SystemOnly(goos, lib) {
			return fmt.Errorf("non-system library %s: %s", rel(tree, p), lib)
		}
	}
	*found = true
	return nil
}

func archAllowed(got, want string) bool {
	if got == want {
		return true
	}
	for _, a := range strings.Split(got, ",") {
		if a == want {
			return true
		}
	}
	return false
}

func matchKind(goos string, kind Kind, rel string) error {
	switch goos {
	case "darwin":
		if kind != KindMachO {
			return fmt.Errorf("%s: want Mach-O on darwin", rel)
		}
	case "linux":
		if kind != KindELF {
			return fmt.Errorf("%s: want ELF on linux", rel)
		}
	default:
		return fmt.Errorf("unsupported os %s", goos)
	}
	return nil
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}
