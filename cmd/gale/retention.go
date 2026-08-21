package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/kelp/gale/internal/installer"
	"github.com/kelp/gale/internal/store"
)

// collectReferencedPackagesWithResolver merges all
// name@version pairs from global and project configs into
// a set. When a resolver is provided, each config package's
// runtime deps (transitively) are also added. Build deps are
// intentionally not expanded. Used by tests of the pin-union
// helper `gale remove` still shares; gc's mark set does not
// call this.
func collectReferencedPackagesWithResolver(
	globalDir, projPath string,
	s *store.Store,
	resolver installer.RecipeResolver,
) (map[string]bool, error) {
	referenced := map[string]bool{}
	var errs []error
	if globalDir != "" {
		errs = append(errs, mergeConfig(
			filepath.Join(globalDir, "gale.toml"),
			s, referenced, nil,
		))
	}
	if projPath != "" {
		errs = append(errs, mergeConfig(
			projPath, s, referenced, nil,
		))
	}
	if resolver != nil {
		expandRuntimeDeps(s, resolver, referenced)
	}
	return referenced, errors.Join(errs...)
}

func expandRuntimeDeps(
	s *store.Store,
	resolver installer.RecipeResolver,
	referenced map[string]bool,
) {
	queue := make([]string, 0, len(referenced))
	visited := make(map[string]bool, len(referenced))
	for key := range referenced {
		at := strings.LastIndexByte(key, '@')
		if at < 0 {
			continue
		}
		name := key[:at]
		if !visited[name] {
			visited[name] = true
			queue = append(queue, name)
		}
	}

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]

		r, err := resolver(context.Background(), name)
		if err != nil || r == nil {
			continue
		}

		for _, dep := range r.Dependencies.Runtime {
			if visited[dep] {
				continue
			}
			visited[dep] = true
			queue = append(queue, dep)

			depRecipe, rErr := resolver(context.Background(), dep)
			version := ""
			if rErr == nil && depRecipe != nil {
				version = depRecipe.Package.Version
			}
			if version == "" {
				continue
			}
			if dir, ok := s.StorePath(dep, version); ok {
				referenced[dep+"@"+filepath.Base(dir)] = true
			}
		}
	}
}
