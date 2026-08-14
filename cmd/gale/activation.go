package main

import (
	"fmt"

	"github.com/kelp/gale/internal/activation"
	"github.com/kelp/gale/internal/config"
	"github.com/kelp/gale/internal/generation"
)

// gateActivation refuses to let a scope's bin dir reach PATH when the
// active generation disagrees with the lockfile.
//
// It runs on every activation, not only when something changed. The
// direnv hook's mtime guard decides whether a full `gale sync` runs;
// this decides whether anything reaches PATH. The two are independent
// because upgrading the gale binary modifies no file in the project,
// so a lock this build refuses to honor trips no mtime.
//
// The global scope is deliberately not gated. `~/.gale/current/bin`
// reaches PATH from the user's shell rc with no gale invocation, so
// there is nothing to hook; global relies on sync-time enforcement,
// which is why a locked global plan forbids carry-forward instead (see
// activation.UnlockedVersions).
//
// Everything here is read-only and hashes nothing, because it runs on
// every `cd` and because a bug in it locks a user out of their shell.
func gateActivation(galeDir, configPath string) error {
	if configPath == "" || configInGaleDir(configPath, galeDir) {
		return nil
	}
	lockPath, err := lockfilePath(configPath)
	if err != nil {
		return err
	}
	storeRoot := defaultStoreRoot()
	// CurrentVersionsStrict recovers name to version from the active
	// generation's symlinks. A generation holds bin, lib, and man
	// links rather than per-package directories, so this is the only
	// place the installed identities can be read back from.
	//
	// Strict, because this is an enforcement gate (gh#210). Installed
	// is the set Check compares against the lock, and the lenient
	// reader answers a generation it could not walk with an EMPTY
	// set — which satisfies every lock, so each violation the gate
	// exists to catch vanishes. Fail-open on an enforcement gate is
	// worse than the refusal, and the refusal is not new surface:
	// the gate already errors when generation.Current fails, so the
	// shell-lockout it can cause is bounded and already reachable.
	installed, err := generation.CurrentVersionsStrict(galeDir, storeRoot)
	if err != nil {
		return fmt.Errorf("reading the active generation: %w", err)
	}
	// Same reasoning as the strict read above: Host selects which lock
	// roots this machine is held to, so an unknown one would quietly
	// hold it to fewer of them.
	host, err := config.CurrentHost()
	if err != nil {
		return err
	}
	return activation.Check(activation.Request{
		LockPath:  lockPath,
		Host:      host,
		Platform:  currentPlatform(),
		StoreRoot: storeRoot,
		Installed: installed,
	})
}
