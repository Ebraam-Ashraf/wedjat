# Wedjat Deployment & Distribution

## Distribution Strategy

Wedjat follows a single-repo, single-branch (`main`) model. All releases are built using GitHub Actions from tags (`v*`). 

### The Artifact
The single distribution format is a `.tar.gz` containing:
1. `wedjatd` (The Go/CGO Daemon)
2. `wedjat` (The TUI - currently stubbed)
3. `wedjat-uninstall` (Cleanup script)
4. `config.yaml` (Default configuration)
5. `wedjatd.service` (Systemd unit)

Because Wedjat uses CGO to statically link against `libbpf` and includes all eBPF bytecode (`.skel.h`), the final `wedjatd` binary is completely standalone on the target machine, requiring only the proprietary NVIDIA driver (`libnvidia-ml.so`).

## The Three User Flows

### Flow 1: Regular User (curl | bash)

```mermaid
graph TD
    A[curl -sSfL install.sh | sudo bash] --> B[Detects Architecture]
    B --> C[Downloads .tar.gz from GitHub Releases]
    C --> D[Verifies sha256 checksum]
    D --> E[Extracts Binaries & Scripts]
    E --> F[Copies to /usr/local/bin]
    F --> G[Creates /etc/wedjat/config.yaml]
    G --> H[Installs & enables wedjatd.service]
```

### Flow 2: Developer (Local Build + System Install)

Used rarely, only to test the systemd integration and true system paths before cutting a release.

```bash
sudo make install
```
This triggers `make release` to compile the BPF objects and Go daemon into `./dist/`, and then runs `sudo scripts/install.sh --local dist/` to mimic the remote install without downloading from GitHub.

### Flow 3: Developer (Fast Iteration)

Used daily for testing daemon logic.

```bash
make dev
```
Runs the daemon in foreground isolated mode (`sudo ./dist/wedjatd --dev`). This ensures the daemon reads from `./dev-config.yaml`, writes to `./dev-data/`, and does not pin BPF objects into `/sys/fs/bpf/`. 

## Safe Uninstallation

The uninstaller (`wedjat-uninstall`) handles tearing down the daemon securely:
1. Stops and disables `wedjatd.service`.
2. Removes the binaries from `/usr/local/bin/`.
3. Prompts the user (`[y/N]`) before deleting the configuration in `/etc/wedjat/` and the historical database in `/var/lib/wedjat/`. By defaulting to "No", Wedjat allows safe reinstalls/upgrades without losing historical metrics.
