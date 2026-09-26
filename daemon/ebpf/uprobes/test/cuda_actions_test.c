/* cuda_actions_test.c
 *
 * Tests cuda_actions.bpf.o against every k* binary in the build directory.
 *
 * Structure:
 *   1. environment check  (root + GPU)
 *   2. load once          (verifier runs once for all programs)
 *   3. attach once        (missing symbol = skip with reason, not a failure)
 *   4. loop every kernel  (run it, drain events_pipe for its pid, print one row)
 *   5. coverage           (warn about event ids no kernel triggered)
 *
 * common.h has no agg_map/agg_key/agg_val/API_* — the only channel
 * cuda_actions.bpf.c has to userspace is the events_pipe ring buffer of
 * struct event, tagged with the EVENT_* ids from common.h's event_id enum.
 * So unlike a hash-map read, this test has to actually poll the ring buffer
 * to see anything — there's no "read it back later" option.
 *
 * One real consequence: struct event's api_id is coarser than this test
 * used to assume. EVENT_MEMCPY covers HtoD/DtoH/DtoD alike (no per-direction
 * id), and EVENT_SYNC covers both cuStreamSynchronize and cuCtxSynchronize.
 * If you want that split back, it has to be added to common.h's enum first —
 * not done here since that's a schema change, not a test fix.
 *
 * usage:  sudo ./cuda_actions_test [build-dir]    (default: build)
 * env:    LIBCUDA=/path/to/libcuda.so.1
 *         DUMP=1   print every matching event as it's polled
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

#include "common.h"
#include "cuda_actions.skel.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

/* ── event ids this file tracks ───────────────────────────────────────────
 * Change this list only when common.h's event_id enum changes.
 * The coverage check warns if no kernel triggered an id here. */
static const u32  TRACKED_IDS[]   = { EVENT_LAUNCH, EVENT_ALLOC, EVENT_FREE,
                                       EVENT_MEMCPY, EVENT_SYNC };
static const char *TRACKED_NAMES[] = { "LAUNCH", "ALLOC", "FREE",
                                        "MEMCPY", "SYNC" };
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
    const char *lib = env_on("LIBCUDA") ? getenv("LIBCUDA") : "libcuda.so.1";
    g_dump = env_on("DUMP");

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

    /* open the ring buffer once — this is the only channel cuda_actions
     * probes have to userspace, so there's no way to skip this step. */
    struct ring_buffer *rb = ring_buffer__new(
        bpf_map__fd(skel->maps.events_pipe), handle_event, NULL, NULL);
    if (!rb) {
        printf("FAIL: ring_buffer__new failed\n");
        cuda_actions_bpf__destroy(skel);
        return FAIL;
    }

    /* 4. loop kernels */
    char *kpaths[64];
    int   nk = scan_kernels(dir, kpaths, 64);
    if (nk == 0) {
        printf("SKIP: no k* binaries in %s\n", dir);
        ring_buffer__free(rb);
        cuda_actions_bpf__destroy(skel);
        return SKIP;
    }

    /* header row */
    printf("\nkernel    ");
    for (int i = 0; i < N_TRACKED; i++)
        printf("  %10s", TRACKED_NAMES[i]);
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

        /* the workload has exited, but its events are still sitting in the
         * ring buffer until we drain it — one poll is enough, nothing more
         * will show up for a pid that's already gone. */
        ring_buffer__poll(rb, 100);

        /* print row */
        printf("  %-10s", kname);
        u64 total = 0;
        for (int i = 0; i < N_TRACKED; i++) {
            total += g_run_counts[i];
            if (g_run_counts[i] == 0) printf("  %10s", ".");
            else                      printf("  %10llu", (unsigned long long)g_run_counts[i]);
        }

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
    cuda_actions_bpf__destroy(skel);

    printf("\ncuda_actions_test: %s\n", fail ? "FAIL" : "PASS");
    return fail ? FAIL : PASS;
}