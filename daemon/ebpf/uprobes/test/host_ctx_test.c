/* host_ctx_test.c
 *
 * Tests host_ctx.bpf.o against every k* binary in the build directory.
 *
 * Structure:
 *   1. environment check  (root + GPU)
 *   2. load once          (verifier runs once)
 *   3. attach once        (missing symbol = skip with reason, not a failure)
 *   4. loop every kernel  (run it, check tid_to_device for the workload's tid)
 *
 * The check: after the workload exits, tid_to_device[tid] must exist.
 * tid == pid for single-threaded programs (the main thread's tid is its pid).
 *
 * usage:  sudo ./host_ctx_test [build-dir]    (default: build)
 * env:    LIBCUDA=/path/to/libcuda.so.1
 * exit:   0 PASS  1 FAIL  77 SKIP
 *
 * Naming convention in host_ctx.bpf.c:
 *   trace_X        uprobe entry  for symbol X
 *   trace_X_ret    uretprobe exit for symbol X
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
#include "host_ctx.skel.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

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
        setenv("WEDJAT_SLEEP_MS", "200", 1);
        execl(path, path, (char *)NULL);
        _exit(127);
    }
    close(p[0]);
    *go_fd = p[1];
    return pid;
}

/* Release child, but do NOT wait for it here — we need to poll while it runs. */
static void release_child(int go_fd)
{
    if (write(go_fd, "x", 1) != 1) perror("write");
    close(go_fd);
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
    const char *lib = env_on("LIBCUDA") ? getenv("LIBCUDA") : "libcuda.so.1";

    /* 1. environment */
    if (geteuid() != 0)        { printf("SKIP: run as root\n");   return SKIP; }
    if (access("/dev/nvidiactl", F_OK) != 0)
                               { printf("SKIP: no NVIDIA GPU\n"); return SKIP; }

    /* 2. load */
    printf("== host_ctx ==\n");
    struct host_ctx_bpf *skel = host_ctx_bpf__open_and_load();
    if (!skel) {
        printf("FAIL: load failed (see verifier output above)\n");
        return FAIL;
    }
    printf("load    ok\n");

    /* 3. attach */
    printf("attach\n");

#define H(sym)  { skel->progs.trace_##sym,      #sym, false, &skel->links.trace_##sym }
#define HR(sym) { skel->progs.trace_##sym##_ret, #sym, true,  &skel->links.trace_##sym##_ret }

    struct {
        struct bpf_program *prog;
        const char         *sym;
        bool                ret;
        struct bpf_link   **slot;
    } hooks[] = {
        H(cuDevicePrimaryCtxRetain),  HR(cuDevicePrimaryCtxRetain),
        H(cuCtxSetCurrent),
        H(cuCtxCreate_v2),            HR(cuCtxCreate_v2),
        H(cuCtxCreate_v3),            HR(cuCtxCreate_v3),
        H(cuCtxCreate_v4),            HR(cuCtxCreate_v4),
        H(cuCtxPushCurrent_v2),
        H(cuCtxPopCurrent_v2),        HR(cuCtxPopCurrent_v2),
        H(cuCtxDestroy_v2),
        H(cuDevicePrimaryCtxRelease_v2),
    };
#undef H
#undef HR

    int n_hooks  = (int)(sizeof(hooks) / sizeof(hooks[0]));
    int attached = 0;
    for (int i = 0; i < n_hooks; i++) {
        LIBBPF_OPTS(bpf_uprobe_opts, o,
                    .func_name = hooks[i].sym,
                    .retprobe  = hooks[i].ret);
        struct bpf_link *l =
            bpf_program__attach_uprobe_opts(hooks[i].prog, -1, lib, 0, &o);
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
        printf("SKIP: no hooks attached — try LIBCUDA=/full/path\n");
        host_ctx_bpf__destroy(skel);
        return SKIP;
    }

    /* 4. loop kernels */
    char *kpaths[64];
    int   nk = scan_kernels(dir, kpaths, 64);
    if (nk == 0) {
        printf("SKIP: no k* binaries in %s\n", dir);
        host_ctx_bpf__destroy(skel);
        return SKIP;
    }

    printf("\nkernel      tid_found  device  result\n");

    int ctx_fd = bpf_map__fd(skel->maps.tid_to_device);
    int fail   = 0;

    for (int k = 0; k < nk; k++) {
        const char *kname = strrchr(kpaths[k], '/') + 1;

        int go;
        pid_t pid = spawn_paused(kpaths[k], &go);
        if (pid < 0) {
            printf("  %-10s  FAIL(fork)\n", kname);
            fail = 1;
            continue;
        }

        release_child(go);

        /* Poll tid_to_device while the process is running.
         * The BPF program removes the TID when the context is destroyed on exit,
         * so we must catch it while it's still alive. */
        struct thread_key tid = { .pid_tgid = ((u64)(u32)pid << 32) | (u32)pid };
        u32 dev = 0xffffffff;
        int found = 0;

        while (1) {
            if (bpf_map_lookup_elem(ctx_fd, &tid, &dev) == 0) {
                found = 1;
                break;
            }
            int st = 0;
            if (waitpid(pid, &st, WNOHANG) > 0) {
                /* Process exited before we saw the TID. */
                break;
            }
            usleep(1000);
        }

        /* Now wait for it to fully exit if it hasn't already. */
        int st = 0;
        waitpid(pid, &st, 0);
        int rc = WIFEXITED(st) ? WEXITSTATUS(st) : -1;

        printf("  %-10s  %-9s  %-6u  ", kname,
               found ? "yes" : "no",
               found ? dev : 0xffffffff);

        if (!found) {
            printf("FAIL(no entry in tid_to_device)\n");
            fail = 1;
        } else if (rc != 0) {
            printf("FAIL(exit %d)\n", rc);
            fail = 1;
        } else {
            printf("ok\n");
        }

        free(kpaths[k]);
    }

    host_ctx_bpf__destroy(skel);

    printf("\nhost_ctx_test: %s\n", fail ? "FAIL" : "PASS");
    return fail ? FAIL : PASS;
}