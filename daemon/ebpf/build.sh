#!/bin/bash
set -euo pipefail

# This script must be run from the repository root (wedjat/).
ARCH=$(uname -m | sed 's/x86_64/x86/;s/aarch64/arm64/')
BPF_CFLAGS="-g -O2 -target bpf -D__TARGET_ARCH_${ARCH} -D__bpf__"

mkdir -p daemon/ebpf/build

# Clear previous outputs before compiling.
#
# A build run under sudo leaves objects owned by root, and the next build as a
# normal user cannot open them for writing. Removing them first is enough to
# recover, because unlinking a file needs write permission on the directory, not
# on the file itself.
rm -f daemon/ebpf/build/*.bpf.o daemon/ebpf/build/*.skel.h

bpftool btf dump file /sys/kernel/btf/vmlinux format c > daemon/ebpf/build/vmlinux.h
BPF_CFLAGS+=" -Idaemon/ebpf/build -Idaemon/ebpf"

for src in daemon/ebpf/kprobes/driver_kprobes.bpf.c \
           daemon/ebpf/proc/proc_lifecycle.bpf.c \
           daemon/ebpf/uprobes/cuda_actions.bpf.c \
           daemon/ebpf/uprobes/host_ctx.bpf.c; do
    base=$(basename "$src" .bpf.c)
    echo "Compiling $src..."
    clang $BPF_CFLAGS -c "$src" -o "daemon/ebpf/build/${base}.bpf.o"
    bpftool gen skeleton "daemon/ebpf/build/${base}.bpf.o" > "daemon/ebpf/build/${base}.skel.h"
done
