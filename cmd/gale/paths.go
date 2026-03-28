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

func defaultStoreRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("/", "gale", "pkg")
	}
	return filepath.Join(home, ".gale", "pkg")
}
