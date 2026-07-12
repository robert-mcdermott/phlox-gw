# Installing Phlox-GW

Phlox-GW is distributed as a self-contained executable. It does not require Go, Node.js,
or a configuration file at runtime.

## Supported Installer Targets

| Operating system | Architecture | Release asset |
| --- | --- | --- |
| macOS | Apple silicon (ARM64) | `phlox-gw-darwin-arm64` |
| Linux | x86-64 | `phlox-gw-linux-amd64` |
| Linux | ARM64 | `phlox-gw-linux-arm64` |

The shell installer does not support Intel Macs because Phlox-GW does not currently
publish a macOS AMD64 binary. Windows users should follow the PowerShell instructions in
the [README](../README.md#manual-downloads).

## Recommended Installation

Install the latest stable release into `$HOME/.local/bin`:

```bash
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/robert-mcdermott/phlox-gw/main/install.sh |
  sh
```

The installer:

1. Detects the operating system and architecture.
2. Downloads the matching raw binary and `checksums.txt` from the latest stable GitHub
   Release.
3. Verifies the binary's SHA-256 checksum.
4. Installs it atomically as `$HOME/.local/bin/phlox-gw`.
5. Runs only `phlox-gw --version` to verify the installed executable.

It does not use `sudo`, modify `PATH`, create Phlox-GW application data, or start the
gateway. If `$HOME/.local/bin` is not already in `PATH`, the installer prints the exact
installed path and leaves shell configuration unchanged.

## Inspect Before Running

Piping a script directly to a shell is convenient, but some users and organizations
require review before execution. Download and inspect the same installer first:

```bash
curl --proto '=https' --tlsv1.2 -fsSLo install-phlox-gw.sh \
  https://raw.githubusercontent.com/robert-mcdermott/phlox-gw/main/install.sh

less install-phlox-gw.sh
sh install-phlox-gw.sh
```

## Installation Options

Install a specific release:

```bash
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/robert-mcdermott/phlox-gw/main/install.sh |
  PHLOX_GW_VERSION=v0.1.0 sh
```

Choose another user-writable directory:

```bash
curl --proto '=https' --tlsv1.2 -fsSL \
  https://raw.githubusercontent.com/robert-mcdermott/phlox-gw/main/install.sh |
  PHLOX_GW_INSTALL_DIR="$HOME/bin" sh
```

The equivalent command-line options are useful with a downloaded installer:

```bash
sh install-phlox-gw.sh --version v0.1.0 --install-dir "$HOME/bin"
```

For a system-wide installation, inspect the script before deliberately granting elevated
permissions:

```bash
curl --proto '=https' --tlsv1.2 -fsSLo install-phlox-gw.sh \
  https://raw.githubusercontent.com/robert-mcdermott/phlox-gw/main/install.sh

less install-phlox-gw.sh
sudo sh install-phlox-gw.sh --install-dir /usr/local/bin
```

Never pipe the network response directly into `sudo sh`.

## First Run

Without configuration, Phlox-GW uses SQLite, listens on `127.0.0.1:8080`, and stores its
database in the current working directory. Use a dedicated directory for a local
evaluation:

```bash
mkdir -p "$HOME/.local/share/phlox-gw"
cd "$HOME/.local/share/phlox-gw"
phlox-gw
```

The first start prints a one-time administrator password. Before shared or production
use, configure a persistent `PHLOX_GW_DATA_DIR` and a stable high-entropy
`PHLOX_GW_SESSION_SECRET`; see the [production run instructions](../README.md#production-oriented-run).

## Manual Downloads

Direct binary links remain available in the [README](../README.md#manual-downloads) and on
the [GitHub Releases page](https://github.com/robert-mcdermott/phlox-gw/releases). Manual
installation is the appropriate path when organizational policy prohibits remote shell
installers.

## Checksum And Provenance Scope

The installer verifies the binary against `checksums.txt` from the same GitHub Release.
This detects corruption, incomplete downloads, and a binary that does not match the
published manifest. It does not protect against an attacker who can replace both the
release asset and its checksum or alter the installer source.

For stronger supply-chain verification, future release automation should produce artifact
attestations and use immutable GitHub Releases. Consumers with GitHub CLI can then verify
an attested binary with `gh attestation verify`.

## Upgrading

Running the installer again replaces the executable atomically but does not stop or
restart a running service and does not modify application data. Before an upgrade:

1. Read the target release notes.
2. Back up the SQLite or Postgres database.
3. Stop the service if Phlox-GW is service-managed.
4. Run the installer with the intended `PHLOX_GW_VERSION`.
5. Confirm `phlox-gw --version`, then restart the service and check `/health` and `/ready`.

Do not downgrade a database unless the target release explicitly documents that path.
