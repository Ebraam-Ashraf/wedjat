/* cuda_actions_test.c
 *
 * Tests cuda_actions.bpf.o against every k* binary in the build directory.
 *
 * Structure:
 *   1. environment check  (root + GPU)
 *   2. load once          (verifier runs once for all programs)
 *   3. attach once        (missing symbol = skip with reason, not a failure)
 *   4. loop every kernel  (run it, read agg_map for its pid, print one row)
 *   5. coverage           (warn about api_ids no kernel triggered)
 *
 * usage:  sudo ./cuda_actions_test [build-dir]    (default: build)
 * env:    LIBCUDA=/path/to/libcuda.so.1
 *         DUMP=1   print full agg_map after each kernel
 * exit:   0 PASS  1 FAIL  77 SKIP
 *
 * Naming convention in cuda_actions.bpf.c:
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

typedef __u32 u32;
typedef __u64 u64;

#include "common.h"
#include "cuda_actions.skel.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

/* ── api_ids this file tracks ─────────────────────────────────────────────
 * Change this list only when you add or remove a probe in cuda_actions.bpf.c
 * The coverage check warns if no kernel triggered an entry here.           */
static const u32  TRACKED_IDS[]   = { API_LAUNCH, API_ALLOC, API_FREE,
                                       API_MEMCPY_HTOD, API_MEMCPY_DTOH,
                                       API_MEMCPY_DTOD, API_STREAM_SYNC,
                                       API_CTX_SYNC };
static const char *TRACKED_NAMES[] = { "LAUNCH", "ALLOC", "FREE",
                                        "MEMCPY_HTOD", "MEMCPY_DTOH",
                                        "MEMCPY_DTOD", "STREAM_SYNC",
                                        "CTX_SYNC" };
#define N_TRACKED (int)(sizeof(TRACKED_IDS) / sizeof(TRACKED_IDS[0]))

/* ── helpers ──────────────────────────────────────────────────────────────*/

static bool env_on(const char *name)
{
    const char *v = getenv(name);
    return v && *v;
}

/* Sum agg_map for one pid and one api_id (0 = all). */
static struct agg_val sum_api(int fd, u32 pid, u32 api)
{
    int ncpu = libbpf_num_possible_cpus();
    struct agg_val *v = calloc(ncpu, sizeof(*v));
    struct agg_val  t = {0};
    struct agg_key  cur, next;
    struct agg_key *prev = NULL;

    while (bpf_map_get_next_key(fd, prev, &next) == 0) {
        if (next.pid == pid &&
            (api == 0 || next.api_id == api) &&
            bpf_map_lookup_elem(fd, &next, v) == 0) {
            for (int i = 0; i < ncpu; i++) {
                t.calls      += v[i].calls;
                t.bytes      += v[i].bytes;
                t.latency_ns += v[i].latency_ns;
                t.errors     += v[i].errors;
                if (v[i].max_latency_ns > t.max_latency_ns)
                    t.max_latency_ns = v[i].max_latency_ns;
            }
        }
        cur  = next;
        prev = &cur;
    }
    free(v);
    return t;
}

/* Print full agg_map for one pid (DUMP=1). */
static void dump_agg(int fd, u32 pid)
{
    int ncpu = libbpf_num_possible_cpus();
    struct agg_val *v = calloc(ncpu, sizeof(*v));
    struct agg_key  cur, next;
    struct agg_key *prev = NULL;

    printf("    [dump pid %u]\n", pid);
    while (bpf_map_get_next_key(fd, prev, &next) == 0) {
        if (next.pid == pid && bpf_map_lookup_elem(fd, &next, v) == 0) {
            u64 calls = 0, bytes = 0, lat = 0;
            for (int i = 0; i < ncpu; i++) {
                calls += v[i].calls;
                bytes += v[i].bytes;
                lat   += v[i].latency_ns;
            }
            printf("    api_id=%-2u dev=%u calls=%llu bytes=%llu lat_ns=%llu\n",
                   next.api_id, next.device_id,
                   (unsigned long long)calls,
                   (unsigned long long)bytes,
                   (unsigned long long)lat);
        }
        cur  = next;
        prev = &cur;
    }
    free(v);
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
    const char *lib = env_on("LIBCUDA") ? getenv("LIBCUDA") : "libcuda.so.1";

    /* 1. environment */
    if (geteuid() != 0)        { printf("SKIP: run as root\n");              return SKIP; }
    if (access("/dev/nvidiactl", F_OK) != 0)
                               { printf("SKIP: no NVIDIA GPU\n");            return SKIP; }

    /* 2. load */
    printf("== cuda_actions ==\n");
    struct cuda_actions_bpf *skel = cuda_actions_bpf__open_and_load();
    if (!skel) {
        printf("FAIL: load failed (see verifier output above)\n");
        return FAIL;
    }
    printf("load    ok\n");

    /* 3. attach — one line per symbol, missing = skip */
    printf("attach\n");

#define H(sym)  { skel->progs.trace_##sym,      #sym, false, &skel->links.trace_##sym }
#define HR(sym) { skel->progs.trace_##sym##_ret, #sym, true,  &skel->links.trace_##sym##_ret }

    struct {
        struct bpf_program *prog;
        const char         *sym;
        bool                ret;
        struct bpf_link   **slot;
    } hooks[] = {
        H(cuLaunchKernel),           H(cuLaunchKernel_ptsz),
        H(cuLaunchCooperativeKernel),H(cuGraphLaunch),
        H(cuMemAlloc_v2),            HR(cuMemAlloc_v2),
        H(cuMemFree_v2),             H(cuMemAllocManaged),
        H(cuMemcpyHtoD_v2),          H(cuMemcpyDtoH_v2),  H(cuMemcpyDtoD_v2),
        H(cuMemcpyHtoDAsync_v2),     H(cuMemcpyDtoHAsync_v2), H(cuMemcpyDtoDAsync_v2),
        H(cuMemcpyAsync),
        H(cuCtxSynchronize),         HR(cuCtxSynchronize),
        H(cuStreamSynchronize),      HR(cuStreamSynchronize),
        H(cuStreamSynchronize_ptsz), HR(cuStreamSynchronize_ptsz),
        H(cuModuleGetFunction),
    };
#undef H
#undef HR

    int n_hooks   = (int)(sizeof(hooks) / sizeof(hooks[0]));
    int attached  = 0;
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
        cuda_actions_bpf__destroy(skel);
        return SKIP;
    }

    /* 4. loop kernels */
    char *kpaths[64];
    int   nk = scan_kernels(dir, kpaths, 64);
    if (nk == 0) {
        printf("SKIP: no k* binaries in %s\n", dir);
        cuda_actions_bpf__destroy(skel);
        return SKIP;
    }

    /* header row */
    printf("\nkernel    ");
    for (int i = 0; i < N_TRACKED; i++)
        printf("  %10s", TRACKED_NAMES[i]);
    printf("  result\n");

    bool covered[N_TRACKED];
    memset(covered, 0, sizeof(covered));
    int fail = 0;

    int agg_fd = bpf_map__fd(skel->maps.agg_map);

    for (int k = 0; k < nk; k++) {
        const char *kname = strrchr(kpaths[k], '/') + 1;

        int go;
        pid_t pid = spawn_paused(kpaths[k], &go);
        if (pid < 0) {
            printf("  %-10s  FAIL(fork)\n", kname);
            fail = 1;
            continue;
        }

        int rc = release_and_wait(pid, go);

        if (env_on("DUMP"))
            dump_agg(agg_fd, (u32)pid);

        /* print row */
        printf("  %-10s", kname);
        struct agg_val total = sum_api(agg_fd, (u32)pid, 0);

        for (int i = 0; i < N_TRACKED; i++) {
            struct agg_val v = sum_api(agg_fd, (u32)pid, TRACKED_IDS[i]);
            if (v.calls > 0) covered[i] = true;
            if (v.calls == 0) printf("  %10s", ".");
            else              printf("  %10llu", (unsigned long long)v.calls);
        }

        if (rc != 0) {
            printf("  FAIL(exit %d)\n", rc);
            fail = 1;
        } else if (total.calls == 0) {
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
        if (!covered[i])
            printf("  WARN  %s not triggered by any kernel\n", TRACKED_NAMES[i]);
        else
            printf("  ok    %s\n", TRACKED_NAMES[i]);
    }

    cuda_actions_bpf__destroy(skel);

    printf("\ncuda_actions_test: %s\n", fail ? "FAIL" : "PASS");
    return fail ? FAIL : PASS;
}
