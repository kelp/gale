package installer

import (
	"context"
	"strings"
	"testing"

	"github.com/kelp/gale/internal/recipe"
	"github.com/kelp/gale/internal/store"
)

func TestInstallRefusesSource(t *testing.T) {
	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Source:  recipe.Source{URL: "http://should-not-be-fetched", SHA256: strings.Repeat("ab", 32)},
		Build: recipe.Build{
			Steps: []string{"true"},
		},
	}
	got, err := inst.Install(context.Background(), r)
	if err == nil {
		t.Fatalf("Install compiled from source: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}

func TestInstallLocalRefusesSource(t *testing.T) {
	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}
	got, err := inst.InstallLocalWithFinalize(r, t.TempDir(), nil)
	if err == nil {
		t.Fatalf("InstallLocalWithFinalize compiled from source: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}

func TestInstallGitRefusesSource(t *testing.T) {
	inst := &Installer{Store: store.NewStore(t.TempDir())}
	r := &recipe.Recipe{
		Package: recipe.Package{Name: "tool", Version: "1.0"},
		Build:   recipe.Build{Steps: []string{"true"}},
	}
	got, err := inst.InstallGitWithFinalize(r, nil)
	if err == nil {
		t.Fatalf("InstallGitWithFinalize compiled from source: %+v", got)
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("refusal must name fetch: %v", err)
	}
}
