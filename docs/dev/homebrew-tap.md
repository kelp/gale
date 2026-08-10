# Testing the Homebrew Formula

Gale ships a Homebrew formula at `kelp/tap/gale`
(source: `~/code/homebrew-tap/Formula/gale.rb`).

Test it in an OrbStack VM rather than on the dev
machine — installing Homebrew on a machine that already
uses gale for the same tools invites PATH confusion.

```sh
# Install brew in the VM (one-time)
orbctl run -m ubuntu-24.04 bash -c \
  'NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'

# Install gale from the tap
orbctl run -m ubuntu-24.04 bash -c \
  'eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)" && brew install kelp/tap/gale'

# Run brew test
orbctl run -m ubuntu-24.04 bash -c \
  'eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)" && brew test kelp/tap/gale'
```

Worth doing when the formula's build or test block
changes, or when a release changes the binary layout the
formula asserts on.
