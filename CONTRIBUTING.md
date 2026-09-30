# Contributing to Wedjat

This guide is for developers who want to compile Wedjat from source, modify its behavior, or contribute to the project.

## Architecture Overview

Wedjat is comprised of three main layers:
1. **The Go Daemon (`wedjatd`)**: The main entry point that handles configuration, background polling, and the SQLite `store`.
2. **The C/eBPF Collector (`poller.c` & eBPF skeletons)**: The low-level NVIDIA library interface and the compiled kernel-space eBPF programs.
3. **The CGO Bridge**: Go accesses the C functions (like NVML initialization and eBPF skeleton loading) through `cgo`.

Because of the CGO bridge, Wedjat is distributed as a **single, standalone binary**, but it requires a robust toolchain to compile.

## Build Requirements

To build Wedjat locally, your development machine needs:
* **Go** (1.22+)
* **Clang & LLVM** (for compiling eBPF C code to BPF bytecode)
* **libbpf-dev** (eBPF headers)
* **Linux Kernel Headers** (`linux-headers-generic` or equivalent)
* **bpftool** (`linux-tools-common` and `linux-tools-generic`)
* **NVIDIA CUDA Toolkit** (Specifically to provide `nvml.h` and the `libnvidia-ml.so` stub for linking)

### Installing Dependencies (Ubuntu 22.04)

```bash
sudo apt-get update
sudo apt-get install -y clang llvm libbpf-dev linux-headers-generic \
                        linux-tools-common linux-tools-generic \
                        pkg-config nvidia-cuda-toolkit
sudo apt-get install -y linux-tools-$(uname -r) || sudo apt-get install -y bpftool
```

## Developer Workflow

### 1. Isolated Dev Mode (`make dev`)

The safest and most common way to test your code changes is to run the daemon in foreground isolated mode.

```bash
make dev
```

**What happens:**
1. Compiles the eBPF objects and generates `*.skel.h` files via `scripts/build_bpf.sh`.
2. Compiles the Go daemon using CGO (`dist/wedjatd`).
3. Starts the daemon with the `--dev` flag.

**Why it is safe:**
When the `--dev` flag is detected, Wedjat actively protects your host system:
* It reads config from `./dev-config.yaml` instead of `/etc/wedjat`.
* It writes SQLite files to `./dev-data/` instead of `/var/lib/wedjat`.
* It uses `/tmp/wedjat-dev.lock` for file locking.
* It checks if the background `wedjatd.service` is actively running, and if it is, the dev daemon immediately exits. This prevents the dev daemon from colliding with the production daemon and double-attaching eBPF probes.
* It prints logs straight to `stdout` instead of to `journalctl`.

To stop the dev daemon, simply press `Ctrl+C`. The kernel automatically unloads any eBPF programs via `bpf_link`.

### 2. Testing System Installation (`sudo make install`)

When you need to test the real `systemd` flow (e.g. testing boot behavior, permissions, and log routing), you can install your local build directly to your host OS.

```bash
sudo make install
```
This command compiles your local code into `dist/wedjatd`, then runs `sudo ./scripts/install.sh --local dist/`. It replaces any existing Wedjat installation with your freshly compiled binary.

To clean up your test installation, use the standard uninstaller:
```bash
sudo wedjat-uninstall
```

### 3. Creating a Release

Wedjat leverages GitHub Actions to handle the complex build environment needed for eBPF and NVML, outputting the final `.tar.gz` to the GitHub Releases page.

**The Release Pipeline:**
1. You finish your code and push it to `main`.
2. When ready to publish, you create and push a Git Tag (e.g., `v0.1.0`):
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
3. The `.github/workflows/release.yml` file triggers GitHub's cloud builder.
4. GitHub checks out the code, installs Clang/Go/NVML dependencies, and runs `make release`.
5. The `dist/wedjat-linux-amd64.tar.gz` tarball is uploaded as a GitHub Release artifact.
6. End-users can immediately install it using the `curl | bash` command in the `README.md`.

*(Note: Binaries are NEVER committed to the Git repository.)*
