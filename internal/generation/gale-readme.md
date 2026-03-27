Managed by gale. Do not edit.
https://github.com/kelp/gale

  gale.toml    Package manifest. Source of truth.
  config.toml  Settings (registry URL, API keys).
  current/     Active environment. Symlink to a gen.
  gen/         Generation snapshots. Each gen is a set
               of symlinks into pkg/. Swapped atomically
               so PATH never sees a broken state.
  pkg/         Package store. One directory per package
               per version. Immutable after install.
