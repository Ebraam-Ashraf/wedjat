/* driver_kprobes_test.c
 *
 * Tests driver_kprobes.bpf.o against every k* binary in the build directory.
 * Same shape as host_ctx_test.c / cuda_actions_test.c, adapted for kprobes:
 *
 * Structure:
 *   1. environment check  (root + GPU)
 *   2. load once          (verifier runs once for all programs)
 *   3. attach once        (missing symbol = skip with reason, not a failure —
 *                          nvidia.ko / nvidia-uvm.ko may not export every
 *                          symbol on every driver version)
 *   4. loop every kernel  (run it, drain events_pipe for its pid, print one row)
 *   5. coverage           (warn about event ids no kernel triggered)
 *
 * Unlike uprobes, kprobe/kretprobe programs attach straight from their
 * SEC("kprobe/<symbol>") name via bpf_program__attach() — no library path
 * or uprobe_opts needed, since the target is a kernel symbol, not a
 * userspace .so.
 *
 * Two probes attach but never show up in the table: do_sys_openat2 and
 * queued_spin_lock_slowpath push nothing to events_pipe by design (no
 * matching event_id exists in common.h yet for "device opened" or "lock
 * contention" — see driver_kprobes.bpf.c). Their attach still gets checked;
 * they're just not part of the tracked/coverage counts below.
 *
 * nvidia_ioctl and uvm_ioctl both report as EVENT_IOCTL — common.h's enum
 * has one ioctl id, not one per driver module, so the table can't tell them
 * apart. Same schema-limitation note as cuda_actions_test.c.
 *
 * usage:  sudo ./driver_kprobes_test [build-dir]    (default: build)
 * env:    DUMP=1   print every matching event as it's polled
 * exit:   0 PASS  1 FAIL  77 SKIP
 *
 * Naming convention in driver_kprobes.bpf.c:
 *   trace_X        kprobe entry  for symbol X
 *   trace_X_ret    kretprobe exit for symbol X
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include <dirent.h>
#include <unistd.h>
#include <errno.h>
#include <sys/wait.h>
#include <linux/types.h>
#include <bpf/libbpf.h>
#include <bpf/bpf.h>

#include "common.h"
#include "driver_kprobes.skel.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

/* ── event ids this file tracks ───────────────────────────────────────────
 * Change this list only when common.h's event_id enum changes.
 * The coverage check warns if no kernel triggered an id here. */
static const u32  TRACKED_IDS[]   = { EVENT_MMAP, EVENT_IOCTL,
                                       EVENT_UVM_FAULT, EVENT_UVM_MIGRATE,
                                       EVENT_UVM_EVICT };
static const char *TRACKED_NAMES[] = { "MMAP", "IOCTL",
                                        "UVM_FAULT", "UVM_MIGRATE",
                                        "UVM_EVICT" };
#define N_TRACKED (int)(sizeof(TRACKED_IDS) / sizeof(TRACKED_IDS[0]))

/* ── ring buffer state ────────────────────────────────────────────────────
 * Reset per kernel; the callback only sees events from the pid we just ran. */
static pid_t g_target_pid          = -1;
static u64   g_run_counts[N_TRACKED];
static bool  g_ever_covered[N_TRACKED];
static bool  g_dump                = false;

static int handle_event(void *ctx, void *data, size_t sz)
{
    (void)ctx;
    if (sz < sizeof(struct event)) return 0;

    struct event *e = data;
    if (e->pid != (u32)g_target_pid) return 0;

    for (int i = 0; i < N_TRACKED; i++) {
        if (e->api_id != TRACKED_IDS[i]) continue;
        g_run_counts[i]++;
        g_ever_covered[i] = true;
        break;
    }

    if (g_dump)
        printf("    [event] api_id=%-2u dev=%u tid=%u addr=0x%llx bytes=%llu "
               "lat_ns=%llu status=%d\n",
               e->api_id, e->device_id, e->tid,
               (unsigned long long)e->address,
               (unsigned long long)e->bytes,
               (unsigned long long)e->latency_ns,
               e->status);

    return 0;
}

/* ── helpers ──────────────────────────────────────────────────────────────*/

static bool env_on(const char *name)
{
    const char *v = getenv(name);
    return v && *v;
}

/* Fork a child that waits on a pipe before execl(). */
static pid_t spawn_paused(const char *path, int *go_fd)
{
    int p[2];
    if (pipe(p)) return -1;

    pid_t pid = fork();
    if (pid == 0) {
        char c;
        close(p[1]);
        if (read(p[0], &c, 1) != 1) _exit(126);
        execl(path, path, (char *)NULL);
        _exit(127);
    }
    close(p[0]);
    *go_fd = p[1];
    return pid;
}

/* Release child and wait for it to finish. */
static int release_and_wait(pid_t pid, int go_fd)
{
    int st = 0;
    if (write(go_fd, "x", 1) != 1) perror("write");
    close(go_fd);
    waitpid(pid, &st, 0);
    return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

/* Scan dir for executable files starting with 'k' and no dot in the name. */
static int scan_kernels(const char *dir, char **paths, int max)
{
    DIR *d = opendir(dir);
    if (!d) { printf("FAIL: cannot open %s: %s\n", dir, strerror(errno)); return 0; }

    int n = 0;
    struct dirent *e;
    while ((e = readdir(d)) != NULL && n < max) {
        if (e->d_name[0] != 'k' || strchr(e->d_name, '.')) continue;
        char path[512];
        snprintf(path, sizeof(path), "%s/%s", dir, e->d_name);
        if (access(path, X_OK) == 0)
            paths[n++] = strdup(path);
    }
    closedir(d);
    /* sort alphabetically */
    for (int i = 0; i < n - 1; i++)
        for (int j = i + 1; j < n; j++)
            if (strcmp(paths[i], paths[j]) > 0) {
                char *tmp = paths[i]; paths[i] = paths[j]; paths[j] = tmp;
            }
    return n;
}

/* ── main ─────────────────────────────────────────────────────────────────*/

int main(int argc, char **argv)
{
    const char *dir = argc > 1 ? argv[1] : "build";
    g_dump = env_on("DUMP");

    /* 1. environment */
    if (geteuid() != 0)        { printf("SKIP: run as root\n");   return SKIP; }
    if (access("/dev/nvidiactl", F_OK) != 0)
                               { printf("SKIP: no NVIDIA GPU\n"); return SKIP; }

    /* 2. load */
    printf("== driver_kprobes ==\n");
    struct driver_kprobes_bpf *skel = driver_kprobes_bpf__open_and_load();
    if (!skel) {
        printf("FAIL: load failed (see verifier output above)\n");
        return FAIL;
    }
    printf("load    ok\n");

    /* 3. attach — one line per symbol, missing = skip
     * (uvm_* symbols need nvidia-uvm.ko loaded; a missing symbol just means
     * bpf_program__attach() fails and we skip that one hook, same as the
     * uprobe tests do for a missing libcuda symbol) */
    printf("attach\n");

#define H(sym)  { skel->progs.trace_##sym,      #sym, false, &skel->links.trace_##sym }
#define HR(sym) { skel->progs.trace_##sym##_ret, #sym, true,  &skel->links.trace_##sym##_ret }

    struct {
        struct bpf_program *prog;
        const char         *sym;
        bool                ret;
        struct bpf_link   **slot;
    } hooks[] = {
        H(do_sys_openat2),
        H(nvidia_mmap),                  HR(nvidia_mmap),
        H(nvidia_ioctl),                 HR(nvidia_ioctl),
        H(uvm_ioctl),                    HR(uvm_ioctl),
        H(uvm_va_block_service_fault),   HR(uvm_va_block_service_fault),
        H(uvm_migrate),                  HR(uvm_migrate),
        H(uvm_va_block_evict_pages),     HR(uvm_va_block_evict_pages),
        H(queued_spin_lock_slowpath),
    };
#undef H
#undef HR

    int n_hooks  = (int)(sizeof(hooks) / sizeof(hooks[0]));
    int attached = 0;
    for (int i = 0; i < n_hooks; i++) {
        struct bpf_link *l = bpf_program__attach(hooks[i].prog);
        if (!l) {
            printf("  skip  %s%s (%s)\n",
                   hooks[i].sym, hooks[i].ret ? "[ret]" : "", strerror(errno));
            continue;
        }
        *hooks[i].slot = l;
        attached++;
    }
    printf("  %d/%d attached\n", attached, n_hooks);

    if (attached == 0) {
        printf("SKIP: no hooks attached — nvidia.ko / nvidia-uvm.ko not loaded?\n");
        driver_kprobes_bpf__destroy(skel);
        return SKIP;
    }

    /* open the ring buffer once — same channel as cuda_actions, and same
     * reasoning: there's no hash map here to read back after the fact. */
    struct ring_buffer *rb = ring_buffer__new(
        bpf_map__fd(skel->maps.events_pipe), handle_event, NULL, NULL);
    if (!rb) {
        printf("FAIL: ring_buffer__new failed\n");
        driver_kprobes_bpf__destroy(skel);
        return FAIL;
    }

    /* 4. loop kernels */
    char *kpaths[64];
    int   nk = scan_kernels(dir, kpaths, 64);
    if (nk == 0) {
        printf("SKIP: no k* binaries in %s\n", dir);
        ring_buffer__free(rb);
        driver_kprobes_bpf__destroy(skel);
        return SKIP;
    }

    /* header row */
    printf("\nkernel    ");
    for (int i = 0; i < N_TRACKED; i++)
        printf("  %11s", TRACKED_NAMES[i]);
    printf("  result\n");

    int fail = 0;

    for (int k = 0; k < nk; k++) {
        const char *kname = strrchr(kpaths[k], '/') + 1;

        int go;
        pid_t pid = spawn_paused(kpaths[k], &go);
        if (pid < 0) {
            printf("  %-10s  FAIL(fork)\n", kname);
            fail = 1;
            continue;
        }

        g_target_pid = pid;
        memset(g_run_counts, 0, sizeof(g_run_counts));

        int rc = release_and_wait(pid, go);

        /* one poll is enough — the workload's events are already sitting in
         * the ring buffer by the time it has exited. */
        ring_buffer__poll(rb, 100);

        /* print row */
        printf("  %-10s", kname);
        u64 total = 0;
        for (int i = 0; i < N_TRACKED; i++) {
            total += g_run_counts[i];
            if (g_run_counts[i] == 0) printf("  %11s", ".");
            else                      printf("  %11llu", (unsigned long long)g_run_counts[i]);
        }

        /* every CUDA call reaches the GPU through an ioctl, so IOCTL should
         * fire for any k* binary — that's the per-kernel pass/fail signal.
         * MMAP/UVM_* only fire for workloads that actually trigger them
         * (mapping happens once per context, UVM only with managed memory),
         * so their absence on a given kernel isn't itself a failure — that's
         * what the coverage section below is for. */
        if (rc != 0) {
            printf("  FAIL(exit %d)\n", rc);
            fail = 1;
        } else if (total == 0) {
            printf("  FAIL(no probes fired)\n");
            fail = 1;
        } else {
            printf("  ok\n");
        }

        free(kpaths[k]);
    }

    /* 5. coverage */
    printf("\ncoverage\n");
    for (int i = 0; i < N_TRACKED; i++) {
        if (!g_ever_covered[i])
            printf("  WARN  %s not triggered by any kernel\n", TRACKED_NAMES[i]);
        else
            printf("  ok    %s\n", TRACKED_NAMES[i]);
    }

    ring_buffer__free(rb);
    driver_kprobes_bpf__destroy(skel);

    printf("\ndriver_kprobes_test: %s\n", fail ? "FAIL" : "PASS");
    return fail ? FAIL : PASS;
}