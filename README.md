# Wedjat

**Wedjat** is a low-overhead GPU monitoring daemon and dashboard for Linux. It combines NVIDIA Management Library (NVML) telemetry with eBPF-based kernel tracing to provide deep visibility into CUDA executions, memory allocations, and Unified Virtual Memory (UVM) page faults.

## Features

* **Zero-Code Changes**: Captures GPU activity transparently without altering your workload.
* **Low Overhead**: Uses modern eBPF (CO-RE) and `libbpf` to process events in the kernel before they reach userspace.
* **Persistent History**: Stores high-fidelity telemetry locally in a compact SQLite database (`/var/lib/wedjat`).
* **Always On**: Runs reliably in the background as a `systemd` service (`wedjatd`).
* **Web Dashboard**: Opens the installed dashboard with the `wedjat` command.

## Requirements

* **OS**: Linux (Tested on Ubuntu 22.04+)
* **Hardware**: NVIDIA GPU
* **Drivers**: NVIDIA Proprietary Driver installed
* **Runtime**: Node.js is required by the installed dashboard launcher.

The published release currently provides a Linux amd64 artifact. The daemon uses
the installed NVIDIA driver and does not require CUDA source compilation at
runtime.

## Installation

Installing Wedjat is a single command. The installer automatically downloads the latest release, installs the daemon, and starts the systemd service.

```bash
sudo curl -sSfL https://raw.githubusercontent.com/Ebraam-Ashraf/wedjat/main/scripts/install.sh | sudo bash
```

### What the installer does:
1. Downloads and verifies the release archive and SHA-256 checksum.
2. Installs `wedjatd`, `wedjat`, and `wedjat-uninstall` in `/usr/local/bin/`.
3. Installs the eBPF objects in `/usr/local/lib/wedjat/ebpf/`.
4. Creates a default configuration file in `/etc/wedjat/config.yaml`.
5. Creates the database directory `/var/lib/wedjat/` and assigns permissions.
6. Enables and starts the `wedjatd` systemd service.

## Usage

Check the status of the background daemon at any time:
```bash
sudo systemctl status wedjatd
```

To open the live GPU dashboard:
```bash
sudo wedjat
```

The dashboard command may request `sudo` so it can access the daemon socket.
To remove Wedjat while preserving collected data and configuration:

```bash
sudo wedjat uninstall
```

## Configuration

The default configuration file is located at `/etc/wedjat/config.yaml`. You can modify data retention policies and storage limits here. After making changes, restart the daemon:
```bash
sudo systemctl restart wedjatd
```

## Uninstallation

The normal uninstall removes the installed program and service while preserving
configuration and history:

```bash
sudo wedjat uninstall
```

For a source checkout, `sudo make uninstall` performs the same non-destructive
uninstall. `sudo make clean` additionally removes Wedjat data, configuration,
and all repository build artifacts.

## Developer Commands

Run these commands from the repository root:

```bash
sudo make                 # Build daemon, eBPF objects, UI, and dist/
sudo make install         # Install an existing dist/ release; does not rebuild
sudo make uninstall       # Remove installed Wedjat; keep build artifacts
sudo make clean           # Remove installed Wedjat and all build artifacts
sudo make ci              # Format and test the daemon/eBPF path
sudo make help            # Show command help
```

`make ci` formats Go and C/C++ sources, builds the development daemon, runs Go,
NVML, and eBPF tests, and cleans generated test artifacts. UI packaging is part
of `make` and `make release`, but UI tests are not part of CI.

## Publishing a Release

The GitHub Actions release workflow runs when a version tag is pushed. It builds
the release archive and checksum, then uploads both to the GitHub Release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The remote installer downloads the latest published release. Local installation
uses the same archive contract after `sudo make`:

```bash
sudo make
sudo make install
```

---
**Developers:** Want to build Wedjat from source or contribute? Please see [CONTRIBUTING.md](CONTRIBUTING.md).
