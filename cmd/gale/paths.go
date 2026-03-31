package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func galeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home dir: %w", err)
	}
	return filepath.Join(home, ".gale"), nil
}

// galeDirForConfig returns the .gale directory that owns
// configPath. If configPath is inside the global dir
// (~/.gale/), returns ~/.gale/. Otherwise returns
// <project>/.gale/ next to the config file. This is the
// single source of truth for deriving the generation
// directory from a config path.
func galeDirForConfig(configPath string) (string, error) {
	globalDir, err := galeConfigDir()
	if err != nil {
		return "", err
	}
	if filepath.Dir(configPath) == globalDir {
		return globalDir, nil
	}
	return filepath.Join(
		filepath.Dir(configPath), ".gale"), nil
}

func defaultStoreRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/", "gale", "pkg")
	}
	return filepath.Join(home, ".gale", "pkg")
}
