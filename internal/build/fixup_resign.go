package build

// shouldResign reports whether FixupBinaries needs to re-sign a
// Mach-O after walking it. gale only modifies a binary when it
// rewrote a dependency path / set a dylib ID (changed) or added
// an rpath because the file lives in the package's lib/ dir
// (inLib). A binary gale left untouched must NOT be re-signed:
// re-signing it serves no purpose and risks clobbering an
// entitlement the upstream build applied (e.g. qemu's
// self-signed Hypervisor.framework binaries). See issue #27.
func shouldResign(changed, inLib bool) bool {
	return changed || inLib
}
