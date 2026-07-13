# Installing Phlox-GW

Phlox-GW is distributed as a self-contained executable. It does not require Go, Node.js,
or a configuration file at runtime.

## Supported Installation Targets

| Operating system | Architecture | Release asset |
| --- | --- | --- |
| macOS | Apple silicon (ARM64) | `phlox-gw-darwin-arm64` |
| Linux | x86-64 | `phlox-gw-linux-amd64` |
| Linux | ARM64 | `phlox-gw-linux-arm64` |
| Windows | x86-64 | `phlox-gw-windows-amd64.exe` |
| Windows | ARM64 | `phlox-gw-windows-arm64.exe` |

The shell installer supports macOS, Linux, and Windows Subsystem for Linux (WSL). Native
Windows installations use the PowerShell workflow below. Intel Macs are not supported
because Phlox-GW does not currently publish a macOS AMD64 binary.

## Recommended Installation

### macOS, Linux, and WSL

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

### Native Windows with PowerShell

Open PowerShell and create a user-local directory for the executable:

```powershell
$InstallDir = Join-Path $env:LOCALAPPDATA "Programs\Phlox-GW"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Set-Location $InstallDir
```

Download the latest stable x86-64 release and its checksum manifest, verify the binary,
and confirm its version:

```powershell
$Asset = "phlox-gw-windows-amd64.exe"
$BaseURL = "https://github.com/robert-mcdermott/phlox-gw/releases/latest/download"

Invoke-WebRequest "$BaseURL/$Asset" -OutFile $Asset
Invoke-WebRequest "$BaseURL/checksums.txt" -OutFile checksums.txt

$Expected = ((Select-String -Path checksums.txt -Pattern "  $Asset$").Line -split '\s+')[0]
$Actual = (Get-FileHash -Algorithm SHA256 $Asset).Hash.ToLowerInvariant()
if ($Actual -ne $Expected) { throw "Checksum verification failed" }

& ".\$Asset" --version
```

Use `phlox-gw-windows-arm64.exe` as `$Asset` on a Windows ARM64 system. The commands save
the executable and `checksums.txt` in `$InstallDir`; they do not modify `PATH`, install a
Windows service, create application data, or start the gateway. See [First Run](#first-run)
for a recommended data directory and launch command.

## Inspect the Shell Installer Before Running

Piping a script directly to a shell is convenient, but some users and organizations
require review before execution. Download and inspect the same installer first:

```bash
curl --proto '=https' --tlsv1.2 -fsSLo install-phlox-gw.sh \
  https://raw.githubusercontent.com/robert-mcdermott/phlox-gw/main/install.sh

less install-phlox-gw.sh
sh install-phlox-gw.sh
```

## Installation Options

### macOS, Linux, and WSL

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

### Native Windows

To install a specific Windows release instead of the latest stable release, use its tag in
`$BaseURL`, then run the same download and verification commands shown above:

```powershell
$Asset = "phlox-gw-windows-amd64.exe"
$Version = "v0.1.0"
$BaseURL = "https://github.com/robert-mcdermott/phlox-gw/releases/download/$Version"
```

Choose another user-writable installation directory by assigning a different path to
`$InstallDir`. Administrator privileges are not required for a directory owned by the
current user. If organizational policy requires a system-wide directory, open an elevated
PowerShell session deliberately and review every command before running it.

## First Run

Without configuration, Phlox-GW uses SQLite, listens on `127.0.0.1:8080`, and stores its
database in the current working directory. Use a dedicated directory for a local
evaluation:

### macOS, Linux, and WSL

```bash
mkdir -p "$HOME/.local/share/phlox-gw"
cd "$HOME/.local/share/phlox-gw"
phlox-gw
```

### Native Windows

The following example keeps application data separate from the executable installed by
the recommended PowerShell workflow:

```powershell
$DataDir = Join-Path $env:LOCALAPPDATA "Phlox-GW"
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
Set-Location $DataDir

& "$env:LOCALAPPDATA\Programs\Phlox-GW\phlox-gw-windows-amd64.exe"
```

Use the ARM64 executable name in the final command on a Windows ARM64 system. Open
`http://127.0.0.1:8080` after the gateway starts.

The first start prints a one-time administrator password. Before shared or production
use, configure a persistent `PHLOX_GW_DATA_DIR` and a stable high-entropy
`PHLOX_GW_SESSION_SECRET`; see the [production run instructions](../README.md#production-oriented-run).

## Manual Downloads

Direct binary links remain available in the [README](../README.md#manual-downloads) and on
the [GitHub Releases page](https://github.com/robert-mcdermott/phlox-gw/releases). Manual
installation is appropriate when organizational policy prohibits remote shell installers
or scripted PowerShell downloads.

Release binaries are not currently code-signed or notarized. Windows SmartScreen or
macOS Gatekeeper may therefore require confirmation under your organization's security
policy. Build from source if unsigned binaries are not permitted in your environment.

## Checksum And Provenance Scope

The shell installer and documented PowerShell workflow verify the binary against
`checksums.txt` from the same GitHub Release. This detects corruption, incomplete
downloads, and a binary that does not match the published manifest. It does not protect
against an attacker who can replace both the release asset and its checksum or alter the
shell installer source.

For stronger supply-chain verification, future release automation should produce artifact
attestations and use immutable GitHub Releases. Consumers with GitHub CLI can then verify
an attested binary with `gh attestation verify`.

## Upgrading

On macOS, Linux, and WSL, running the shell installer again replaces the executable
atomically but does not stop or restart a running service and does not modify application
data. On Windows, stop Phlox-GW before replacing the executable, then rerun the PowerShell
download and verification steps from the installation directory (by default,
`$env:LOCALAPPDATA\Programs\Phlox-GW`); Windows may prevent overwriting a running
executable.

Before an upgrade:

1. Read the target release notes.
2. Back up the SQLite or Postgres database.
3. Stop the service if Phlox-GW is service-managed.
4. Install the intended version using `PHLOX_GW_VERSION` with the shell installer or a
   version-specific Windows `$BaseURL`.
5. Confirm the executable's `--version`, then restart the service and check `/health` and
   `/ready`.

Do not downgrade a database unless the target release explicitly documents that path.
