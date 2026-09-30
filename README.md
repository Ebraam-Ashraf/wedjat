# Wedjat

**Wedjat** is a low-overhead GPU monitoring daemon and dashboard for Linux. It combines NVIDIA Management Library (NVML) telemetry with eBPF-based kernel tracing to provide deep visibility into CUDA executions, memory allocations, and Unified Virtual Memory (UVM) page faults.

## Features

* **Zero-Code Changes**: Captures GPU activity transparently without altering your workload.
* **Low Overhead**: Uses modern eBPF (CO-RE) and `libbpf` to process events in the kernel before they reach userspace.
* **Persistent History**: Stores high-fidelity telemetry locally in a compact SQLite database (`/var/lib/wedjat`).
* **Always On**: Runs reliably in the background as a `systemd` service (`wedjatd`).

## Requirements

* **OS**: Linux (Tested on Ubuntu 22.04+)
* **Hardware**: NVIDIA GPU
* **Drivers**: NVIDIA Proprietary Driver installed

*(Note: You do not need to install CUDA toolkits or compilers to just use Wedjat. The installer provides a standalone, statically-compiled binary.)*

## Installation

Installing Wedjat is a single command. The installer automatically downloads the latest release, installs the daemon, and starts the systemd service.

```bash
curl -sSfL https://raw.githubusercontent.com/Ebraam-Ashraf/wedjat/main/scripts/install.sh | sudo bash
```

### What the installer does:
1. Downloads the pre-compiled `wedjatd` (daemon) binary for your architecture.
2. Places binaries in `/usr/local/bin/`.
3. Creates a default configuration file in `/etc/wedjat/config.yaml`.
4. Creates the database directory `/var/lib/wedjat/` and assigns permissions.
5. Enables and starts the `wedjatd` systemd service.

## Usage

Check the status of the background daemon at any time:
```bash
systemctl status wedjatd
```

To view live GPU telemetry (TUI coming soon):
```bash
wedjat
```

## Configuration

The default configuration file is located at `/etc/wedjat/config.yaml`. You can modify data retention policies and storage limits here. After making changes, restart the daemon:
```bash
sudo systemctl restart wedjatd
```

## Uninstallation

To completely remove Wedjat and its service, run the included uninstaller:

```bash
sudo wedjat-uninstall
```

You will be prompted before your historical monitoring data (`/var/lib/wedjat`) and configuration files are permanently deleted.

---
**Developers:** Want to build Wedjat from source or contribute? Please see [CONTRIBUTING.md](CONTRIBUTING.md).
