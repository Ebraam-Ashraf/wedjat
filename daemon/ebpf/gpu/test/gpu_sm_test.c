/* gpu_sm_test.c
 *
 * Tests gpu_sm.bpf.o against every k* binary in the build directory.
 * Same shape as driver_kprobes_test.c / cuda_actions_test.c / host_ctx_test.c,
 * with three differences that come from layer 5 living on the GPU:
 *
 *   1. The object is not loaded into the Linux kernel. bpftime JITs it to PTX
 *      and injects it into the target CUDA kernel, so the test itself has to
 *      run under bpftime's syscall server (LD_PRELOAD) and each child has to
 *      run with bpftime's agent preloaded. Without either, this test skips.
 *   2. The probes are bound to ONE mangled kernel name (see SEC in
 *      gpu_sm.bpf.c) — the same "one symbol per hook" situation as
 *      driver_kprobes_test.c, except here a k* binary that simply doesn't
 *      contain that kernel is "n/a", not a failure. A binary that DOES contain
 *      it and produces nothing is a failure.
 *   3. Block events arrive while the workload runs, so we poll during the run
 *      (same loop host_ctx_test.c uses) instead of once after it exits. The
 *      poll itself goes through bpftime's
 *      bpftime_syscall_server__poll_gpu_ringbuf_map, found with dlsym — the
 *      libbpf ring_buffer API refuses the GPU map type (1527).
 *
 * What a passing row means: the two probes were injected into the target
 * kernel's PTX, the ring reached the GPU, and every block of every launch
 * reported exactly one START/END pair on one SM, with a non-zero globaltimer.
 * Block pairing is checked per START/END pair — see block_state.
 *
 * usage:  sudo LD_PRELOAD=$HOME/.bpftime/libbpftime-syscall-server.so \
 *              ./gpu_sm_test [build-dir]    (default: build)
 * env:    BPFTIME_AGENT=/path/to/libbpftime-agent.so
 *         DUMP=1           print every matching event as it's polled
 *         LIBBPF_DEBUG=1   libbpf's section/relocation trace (warnings always)
 * exit:   0 PASS  1 FAIL  77 SKIP
 *
 * bpftime's own log goes to build/bpftime.log, not the terminal — see the
 * Makefile. Nothing here measures wall clock: the bootstrap, the extra ptxas
 * round a matching kernel pays, and the fixture's sleep are three separate
 * costs that no on/off comparison separates. `callbacks > 0` is the proof that
 * the probe runs.
 *
 * Naming convention in gpu_sm.bpf.c:
 *   trace_sm_block      kernel entry
 *   trace_sm_block_ret  kernel exit
 */

#define _GNU_SOURCE
#include <stdarg.h>
#include <dlfcn.h>
#include <stdint.h>

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
#include "gpu_sm.skel.h"

#define PASS 0
#define FAIL 1
#define SKIP 77

/* %smid is not contiguous on every part, so this is headroom, not the SM
 * count of any particular GPU — an out-of-range sm_id is a bad event, a valid
 * one may still sit above the real SM count. */
#define MAX_SMS 256

/* ── event ids this file tracks ───────────────────────────────────────────
 * Change this list only when common.h's event_id enum changes. */
static const u32  TRACKED_IDS[]   = { EVENT_SM_BLOCK_START, EVENT_SM_BLOCK_END };
static const char *TRACKED_NAMES[] = { "START", "END" };
#define N_TRACKED (int)(sizeof(TRACKED_IDS) / sizeof(TRACKED_IDS[0]))

/* ── ring buffer state ────────────────────────────────────────────────────
 * Reset per kernel; the callback only sees events from the pid we just ran. */
static pid_t g_target_pid          = -1;
static u64   g_run_counts[N_TRACKED];
static bool  g_ever_covered[N_TRACKED];
static bool  g_sm_seen[MAX_SMS];
static bool  g_bad_event           = false;
static bool  g_dump                = false;

/* Per-block pairing. Counts alone cannot tell a correct run from a ring that
 * quietly dropped half its records, because a dropped START and its matching
 * END usually disappear together. So each block is a tiny state machine:
 * START opens a launch, END closes it.
 *
 * Invariants are checked per START/END *pair*, never per block over the whole
 * run: the same ctaid runs once per launch and may land on a different SM each
 * time, so "one SM per block" is only true inside one launch. (An earlier
 * version of this compared across launches and flagged every block that ever
 * moved, which is normal scheduler behaviour.)
 *
 * ts_ns is the GPU globaltimer in nanoseconds — 64-bit, and it wraps a u32
 * every ~4.3s — so it must be stored as u64 or the ordering check silently
 * stops working.
 *
 * Pairing as records arrive relies on each block's ring being FIFO. That holds
 * because the ring is indexed by global thread id and only thread (0,0,0) of a
 * block writes, so a block's records share one ring in program order. If
 * DUMP=1 ever shows START/END out of order, buffer and sort by ts_ns instead. */
#define MAX_CTA 4096
struct block_state {
    u32         launches;   /* completed START/END pairs                    */
    bool        open;       /* START seen, its END not seen yet             */
    u32         start_sm;
    u64         start_ts;
    const char *problem;    /* first invariant this block broke             */
};
static struct block_state g_blocks[MAX_CTA];
static int   g_max_cta              = 0;
static const char *g_block_problem  = NULL;

/* Fold the per-block state into one verdict. Returns NULL when every block is
 * clean, else a complete "FAIL(...)" string. `iters` and `expect_blocks` come
 * from the same env vars the fixture's launch geometry derives from, so a
 * mismatch means records were lost rather than that the expectation is wrong. */
static const char *block_problem(long iters, long expect_blocks)
{
    static char buf[96];
    long nblocks = 0;
    for (int c = 0; c < g_max_cta; c++) {
        struct block_state *b = &g_blocks[c];
        if (b->launches == 0 && !b->open && !b->problem) continue;
        nblocks++;
        /* First violation wins: later ones are usually consequences of it. */
        if (b->problem) {
            snprintf(buf, sizeof(buf), "FAIL(%s)", b->problem);
            return buf;
        }
        /* A START whose END never arrived is a dropped record, not a
         * still-running block: the child has already exited. */
        if (b->open)
            return "FAIL(a block reported START with no END)";
        if (b->launches != (u32)iters)
            return "FAIL(a block did not report once per launch)";
    }
    if (nblocks != expect_blocks) {
        snprintf(buf, sizeof(buf),
                 "FAIL(%ld blocks reported, expected %ld)", nblocks, expect_blocks);
        return buf;
    }
    return NULL;
}

/* ── poll diagnostics ─────────────────────────────────────────────────────
 * These separate the two ways this test can see nothing:
 *   g_poll_calls > 0 but g_cb_calls == 0  -> nothing was written: the probes
 *                                            did not run in the child at all
 *   g_cb_calls > 0 but no counts           -> the pid filter is dropping
 *                                            everything, i.e. the device-side
 *                                            bpf_get_current_pid_tgid isn't
 *                                            the host pid
 * Neither is visible in the per-kernel rows on its own. */
static u64   g_poll_calls           = 0;
static u64   g_cb_calls             = 0;

typedef int (*poll_gpu_fn)(int mapfd, void *ctx,
                           void (*cb)(const void *data, uint64_t size, void *ctx));

static void handle_event(const void *data, uint64_t size, void *ctx)
{
    (void)ctx;
    g_cb_calls++;

    if (size < sizeof(struct dev_event)) return;

    const struct dev_event *e = data;
    if (e->pid != (u32)g_target_pid) return;

    for (int i = 0; i < N_TRACKED; i++) {
        if (e->api_id != TRACKED_IDS[i]) continue;
        g_run_counts[i]++;
        g_ever_covered[i] = true;
        break;
    }

    /* ts_ns comes off the GPU globaltimer, not the host clock, so a zero here
     * means the clock helper never ran — and an sm_id past MAX_SMS means the
     * %smid read gave us something that isn't a device id. */
    if (e->ts_ns == 0 || e->sm_id >= MAX_SMS)
        g_bad_event = true;
    else
        g_sm_seen[e->sm_id] = true;

    /* Per-block pairing. ctaid_x is the block index for a 1D launch; for a
     * multi-dimensional launch it is only the x component, which is enough
     * to key on as long as y/z are recorded too. */
    unsigned long cta = (unsigned long)e->ctaid_x +
                        (unsigned long)e->ctaid_y * 65536UL +
                        (unsigned long)e->ctaid_z * 4294967296UL;
    if (cta >= MAX_CTA) {
        if (!g_block_problem) g_block_problem = "ctaid beyond MAX_CTA";
        return;
    }
    struct block_state *b = &g_blocks[cta];
    if ((int)cta + 1 > g_max_cta) g_max_cta = (int)cta + 1;

    if (e->api_id == EVENT_SM_BLOCK_START) {
        /* Only the first violation is kept: later ones are usually
         * consequences of it (a dropped START makes every following END
         * look unpaired), and the first is the one that points at the cause. */
        if (b->open && !b->problem)
            b->problem = "a block reported START twice without an END";
        b->open     = true;
        b->start_sm = e->sm_id;
        b->start_ts = e->ts_ns;
    } else if (e->api_id == EVENT_SM_BLOCK_END) {
        if (!b->open) {
            if (!b->problem)
                b->problem = "a block reported END with no START";
        } else {
            /* A block cannot migrate between SMs while it runs, so START and
             * END of the same launch must name the same one. */
            if (e->sm_id != b->start_sm && !b->problem)
                b->problem = "a block changed SM between its START and END";
            if (e->ts_ns < b->start_ts && !b->problem)
                b->problem = "a block's END timestamp precedes its START";
            b->open = false;
            b->launches++;
        }
    }

    if (g_dump)
        printf("    [event] %s sm=%u ctaid=(%u,%u,%u) ts=%llu\n",
               e->api_id == EVENT_SM_BLOCK_START ? "START" : "END  ",
               e->sm_id, e->ctaid_x, e->ctaid_y, e->ctaid_z,
               (unsigned long long)e->ts_ns);
}

/* ── helpers ──────────────────────────────────────────────────────────────*/

static bool env_on(const char *name)
{
    const char *v = getenv(name);
    return v && *v;
}

static long env_long(const char *name, long fallback)
{
    const char *v = getenv(name);
    if (!v || !*v) return fallback;
    char *end = NULL;
    long parsed = strtol(v, &end, 10);
    if (end == v || parsed <= 0) return fallback;
    return parsed;
}

/* True if `needle` appears anywhere in the file. nvcc hands each kernel's
 * mangled name to __cudaRegisterFunction as a string, so a fixture that
 * contains the target kernel contains WEDJAT_TARGET_SYM in its .rodata. This
 * is what lets the test tell "this binary has no target kernel" (k2/k3, the
 * expected n/a) from "it has the kernel but nothing arrived" (k4), which are
 * very different results and used to be indistinguishable.
 * Verify with: strings build/k1 | grep scale_add  */
static bool file_contains(const char *path, const char *needle)
{
    FILE *f = fopen(path, "rb");
    if (!f) return false;
    if (fseek(f, 0, SEEK_END) != 0) { fclose(f); return false; }
    long n = ftell(f);
    rewind(f);
    if (n <= 0) { fclose(f); return false; }
    char *buf = malloc((size_t)n);
    bool hit = false;
    if (buf && fread(buf, 1, (size_t)n, f) == (size_t)n)
        hit = memmem(buf, (size_t)n, needle, strlen(needle)) != NULL;
    free(buf);
    fclose(f);
    return hit;
}

/* Fixtures that contain the target kernel but are EXPECTED to produce no
 * events. k4 launches it with <<<>>>, which calls __cudaLaunchKernel; bpftime
 * replaces cudaLaunchKernel / cudaLaunchKernel_ptsz / cuLaunchKernel, so the
 * launch is never intercepted and the probes never run. Listing it here keeps
 * the gap visible as XFAIL instead of hiding it, and turns into XPASS the day
 * bpftime grows a __cudaLaunchKernel hook — at which point remove the entry
 * and update daemon/ebpf/gpu/README.md. */
static bool known_untraced(const char *name)
{
    return strcmp(name, "k4") == 0;
}

/* Fork a child that waits on a pipe before execl(), with bpftime's agent
 * preloaded so the probes reach the CUDA kernels it launches. */
static pid_t spawn_paused(const char *path, const char *agent, int *go_fd)
{
    int p[2];
    if (pipe(p)) return -1;

    pid_t pid = fork();
    if (pid == 0) {
        char c;
        close(p[1]);
        if (read(p[0], &c, 1) != 1) _exit(126);
        setenv("LD_PRELOAD", agent, 1);
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

/* libbpf's own debug stream is ~40 lines of section/relocation chatter before
 * the first fixture even runs. It is genuinely useful when a load or attach
 * fails and pure noise when it succeeds, so it is opt-in via LIBBPF_DEBUG=1.
 * Warnings are always shown: they are how a rejected object announces itself. */
static bool g_libbpf_debug;

static int libbpf_log(enum libbpf_print_level level, const char *fmt, va_list args)
{
    if (level == LIBBPF_DEBUG && !g_libbpf_debug) return 0;
    const char *tag = level == LIBBPF_WARN ? "warn" : "debug";
    fprintf(stderr, "  [libbpf %s] ", tag);
    vfprintf(stderr, fmt, args);
    return 0;
}

int main(int argc, char **argv)
{
    /* Line-buffer stdout. The Makefile redirects it to a file, and a file is
     * block-buffered by default, so every printf() here would sit in a buffer
     * until exit while the forked children — which write straight to the same
     * fd — got theirs out immediately. The result was all four fixtures'
     * output printed first and all four result rows last. Line buffering keeps
     * a parent's row next to its own fixture, exactly as when this ran on a
     * terminal. */
    setvbuf(stdout, NULL, _IOLBF, 0);

    g_libbpf_debug = env_on("LIBBPF_DEBUG");
    libbpf_set_print(libbpf_log);
    const char *dir = argc > 1 ? argv[1] : "build";
    g_dump = env_on("DUMP");

    const char *home = getenv("HOME");
    char        agent_buf[512];
    snprintf(agent_buf, sizeof(agent_buf), "%s/.bpftime/libbpftime-agent.so",
             home ? home : "/root");
    const char *agent = env_on("BPFTIME_AGENT") ? getenv("BPFTIME_AGENT") : agent_buf;

    /* 1. environment — the extra checks over the other tests are what makes
     *    this one run under bpftime instead of the Linux kernel */
    if (geteuid() != 0)        { printf("SKIP: run as root\n");   return SKIP; }
    if (access("/dev/nvidiactl", F_OK) != 0)
                               { printf("SKIP: no NVIDIA GPU\n"); return SKIP; }
    if (!env_on("LD_PRELOAD") || !strstr(getenv("LD_PRELOAD"), "bpftime")) {
        printf("SKIP: run under bpftime, e.g.\n"
               "  sudo LD_PRELOAD=$HOME/.bpftime/libbpftime-syscall-server.so %s\n",
               argv[0]);
        return SKIP;
    }
    if (access(agent, R_OK) != 0) {
        printf("SKIP: bpftime agent not readable at %s (set BPFTIME_AGENT)\n", agent);
        return SKIP;
    }

    /* 2. load */
    printf("== gpu_sm ==\n");
    struct gpu_sm_bpf *skel = gpu_sm_bpf__open_and_load();
    if (!skel) {
        printf("FAIL: load failed (see verifier output above)\n");
        return FAIL;
    }
    printf("load    ok\n");

    /* 3. attach — same direct bpf_program__attach() the kprobe test uses,
     * since these are SEC("kprobe/<mangled name>") programs too. */
    printf("attach\n");

/* bpftime identifies a GPU program by its eBPF function name starting with
 * "cuda__" (bpftime_prog::is_cuda), so the skeleton fields carry that prefix
 * too: cuda__trace_sm_block / cuda__trace_sm_block_ret. */
#define H(sym)  { skel->progs.cuda__##sym,      #sym, false, &skel->links.cuda__##sym }
#define HR(sym) { skel->progs.cuda__##sym##_ret, #sym, true,  &skel->links.cuda__##sym##_ret }

    struct {
        struct bpf_program *prog;
        const char         *sym;
        bool                ret;
        struct bpf_link   **slot;
    } hooks[] = {
        H(trace_sm_block), HR(trace_sm_block),
    };
#undef H
#undef HR

    int n_hooks  = (int)(sizeof(hooks) / sizeof(hooks[0]));
    int attached = 0;
    for (int i = 0; i < n_hooks; i++) {
        struct bpf_link *l = bpf_program__attach(hooks[i].prog);
        /* bpf_program__attach returns ERR_PTR on failure, which is non-NULL:
         * a plain !l test would print "attached" for an error. */
        long err = libbpf_get_error(l);
        if (err || !l) {
            printf("  skip  %s%s (%s)\n",
                   hooks[i].sym, hooks[i].ret ? "[ret]" : "",
                   err ? strerror((int)-err) : "null link");
            continue;
        }
        *hooks[i].slot = l;
        attached++;
    }
    printf("  %d/%d attached\n", attached, n_hooks);

    if (attached < n_hooks)
        printf("  note: a partial attach means the section name was not routed\n"
               "        to bpftime's CUDA path (is_cuda() checks the eBPF\n"
               "        function name for the \"cuda__\" prefix)\n");

    if (attached == 0) {
        printf("SKIP: no hooks attached — is bpftime built with CUDA attach support?\n");
        gpu_sm_bpf__destroy(skel);
        return SKIP;
    }

    /* events are read through bpftime, not libbpf: the GPU ringbuf map is not
     * a kernel BPF_MAP_TYPE_RINGBUF, so ring_buffer__new refuses it */
    poll_gpu_fn poll_fn = dlsym(RTLD_DEFAULT,
                                "bpftime_syscall_server__poll_gpu_ringbuf_map");
    if (!poll_fn) {
        printf("SKIP: bpftime poll function not found — not running under bpftime?\n");
        gpu_sm_bpf__destroy(skel);
        return SKIP;
    }
    int mapfd = bpf_map__fd(skel->maps.dev_events_pipe);

    /* 4. loop kernels */
    char *kpaths[64];
    int   nk = scan_kernels(dir, kpaths, 64);
    if (nk == 0) {
        printf("SKIP: no k* binaries in %s\n", dir);
        gpu_sm_bpf__destroy(skel);
        return SKIP;
    }

    /* header row — same shape as cuda_actions_test */
    printf("\nkernel    ");
    for (int i = 0; i < N_TRACKED; i++)
        printf("  %10s", TRACKED_NAMES[i]);
    printf("  %5s  result\n", "SMs");

    int fail = 0;

    /* Expected event count, so the test can catch a *truncated* run and not
     * just an asymmetric one. `start == end` passes at 5 == 5, which is what
     * a ring that silently dropped records would look like.
     *
     * The cap is read from BPFTIME_MAP_GPU_THREAD_COUNT rather than duplicated
     * as a constant here, because that env var and WEDJAT_MAX_GLOBAL_TID in
     * gpu_sm.bpf.c are already required to be equal: bpftime allocates one
     * ring per thread and the device-side helper drops any thread at or above
     * the ring count. Reading the env var the Makefile sets keeps a single
     * source of truth instead of two numbers that can drift apart silently. */
    const long ring_threads = env_long("BPFTIME_MAP_GPU_THREAD_COUNT", 8192);
    const long n_elems     = env_long("WEDJAT_N", 1 << 20);
    const long iters       = env_long("WEDJAT_ITERS", 20);
    const long block_dim   = 256;  /* k1's only; the hooked kernel is k1's */
    long grid_blocks = (n_elems + block_dim - 1) / block_dim;
    long cap_blocks  = ring_threads / block_dim;
    long expect_blocks = grid_blocks < cap_blocks ? grid_blocks : cap_blocks;
    long expect_each  = expect_blocks * iters;

    for (int k = 0; k < nk; k++) {
        const char *kname = strrchr(kpaths[k], '/') + 1;
        /* Decided from the binary itself, not from what other fixtures did:
         * "some other fixture had events" cannot distinguish a kernel that is
         * absent from one that is present but untraced. */
        const bool has_target = file_contains(kpaths[k], WEDJAT_TARGET_SYM);

        int go;
        pid_t pid = spawn_paused(kpaths[k], agent, &go);
        if (pid < 0) {
            printf("  %-10s  FAIL(fork)\n", kname);
            fail = 1;
            continue;
        }

        g_target_pid = pid;
        memset(g_run_counts, 0, sizeof(g_run_counts));
        memset(g_sm_seen, 0, sizeof(g_sm_seen));
        g_bad_event = false;
        memset(g_blocks, 0, sizeof(g_blocks));
        g_max_cta = 0;
        g_block_problem = NULL;

        release_child(go);

        /* The GPU keeps emitting while the workload runs, so poll until it
         * exits, then drain once more for whatever landed in the last window.
         *
         * bpftime's drain_data() returns at most one record per ring per call
         * (nv_gpu_ringbuf_map.cpp:68) and aborts the whole drain on the first
         * dirty ring it finds, so poll in a tight loop with no sleep: a 50ms
         * gap lets the rings fill and drop events. */
        int st = 0, perr = 0, poll_ret, child_reaped = 0;
        for (;;) {
            pid_t waited;
            do {
                waited = waitpid(pid, &st, WNOHANG);
            } while (waited < 0 && errno == EINTR);

            if (waited == pid) {
                child_reaped = 1;
                break;
            }
            if (waited < 0) {
                perror("waitpid");
                perr = 1;
                break;
            }

            for (int i = 0; i < 64; i++) {
                poll_ret = poll_fn(mapfd, NULL, handle_event);
                g_poll_calls++;
                if (poll_ret < 0) { perr = 1; break; }
            }
        }
        /* final drain: keep going until nothing new arrives */
        for (int i = 0; i < 4096; i++) {
            u64 before = g_cb_calls;
            poll_ret = poll_fn(mapfd, NULL, handle_event);
            g_poll_calls++;
            if (poll_ret < 0) { perr = 1; break; }
            if (g_cb_calls == before) break;
        }
        int rc = child_reaped && WIFEXITED(st) ? WEXITSTATUS(st) : -1;

        int nsm = 0;
        for (int i = 0; i < MAX_SMS; i++)
            if (g_sm_seen[i]) nsm++;
        u64 total = g_run_counts[0] + g_run_counts[1];

        /* Decide the verdict first, then print one row. Ordering it this way
         * is what keeps the row and its result on the same line, and it means
         * "did this fixture fail" is answered in exactly one place below. */
        const char *verdict = "ok";
        bool row_fail = false;

        /* Unlike the other tests, zero events is not a failure here: both probes
         * bind to one mangled kernel name, so a k* binary that does not contain
         * that kernel correctly produces nothing. What that is NOT allowed to
         * hide is a *present* kernel producing nothing, which is why the
         * symbol is checked in the binary rather than inferred from the other
         * fixtures. */
        /* One shared buffer: every branch that formats assigns `verdict` and
         * stops, so nothing can clobber a verdict already stored. */
        static char vbuf[96];
        const char *perr_str;

        if (perr) {
            verdict = "FAIL(poll error)";              row_fail = true;
        } else if (!child_reaped) {
            verdict = "FAIL(waitpid error)";           row_fail = true;
        } else if (rc != 0) {
            snprintf(vbuf, sizeof(vbuf), "FAIL(exit %d)", rc);
            verdict = vbuf;                           row_fail = true;
        } else if (total == 0) {
            if (!has_target)
                verdict = "n/a (target kernel not in this binary)";
            else if (known_untraced(kname))
                verdict = "XFAIL (target present, launch not intercepted)";
            else {
                verdict = "FAIL(target present, no events reached the callback)";
                row_fail = true;
            }
        } else if (known_untraced(kname)) {
            /* bpftime grew a __cudaLaunchKernel hook. Stop calling this XFAIL,
             * drop it from known_untraced() and update the README. */
            verdict = "XPASS (now traced: drop from known_untraced())";
        } else if (g_bad_event) {
            verdict = "FAIL(bad ts_ns or sm_id)";       row_fail = true;
        } else if (g_run_counts[0] != g_run_counts[1]) {
            snprintf(vbuf, sizeof(vbuf), "FAIL(start %llu != end %llu)",
                     (unsigned long long)g_run_counts[0],
                     (unsigned long long)g_run_counts[1]);
            verdict = vbuf;                           row_fail = true;
        } else if ((long)g_run_counts[0] != expect_each) {
            /* The count must be exactly right, not merely balanced. A ring that
             * dropped records still gives start == end, and 5 == 5 is
             * indistinguishable from correct without the expectation. */
            snprintf(vbuf, sizeof(vbuf), "FAIL(start %llu != expected %ld)",
                     (unsigned long long)g_run_counts[0], expect_each);
            verdict = vbuf;                           row_fail = true;
        } else if (g_block_problem) {
            snprintf(vbuf, sizeof(vbuf), "FAIL(%s)", g_block_problem);
            verdict = vbuf;                           row_fail = true;
        } else if ((perr_str = block_problem(iters, expect_blocks))) {
            verdict = perr_str;                        row_fail = true;
        }

        /* One row per fixture, same shape as cuda_actions_test: kernel, the
         * tracked event counts, distinct SMs, verdict. A '.' means zero, so a
         * fixture with nothing to trace reads the same as one in the other
         * tests. */
        printf("  %-10s", kname);
        for (int i = 0; i < N_TRACKED; i++) {
            if (g_run_counts[i] == 0) printf("  %10s", ".");
            else printf("  %10llu", (unsigned long long)g_run_counts[i]);
        }
        if (nsm == 0) printf("  %5s", ".");
        else          printf("  %5d", nsm);
        printf("  %s\n", verdict);
        if (row_fail) fail = 1;
    }

    for (int k = 0; k < nk; k++)
        free(kpaths[k]);

    /* 5. coverage — did each tracked event fire at all? */
    printf("\ncoverage\n");
    for (int i = 0; i < N_TRACKED; i++)
        printf("  %-4s  %s\n", g_ever_covered[i] ? "ok" : "WARN", TRACKED_NAMES[i]);
    if (!g_ever_covered[0] && !g_ever_covered[1]) {
        printf("  FAIL  no binary produced any block events\n");
        fail = 1;
    }

    /* The links are destroyed here rather than left to gpu_sm_bpf__destroy(),
     * and the slots are NULLed: these are &skel->links.*, and leaving a
     * destroyed link in the slot makes the skeleton's own destroy free it a
     * second time, killing the process before it can print its verdict. */
    for (int i = 0; i < n_hooks; i++) {
        struct bpf_link **slot = hooks[i].slot;
        if (*slot) {
            bpf_link__destroy(*slot);
            *slot = NULL;
        }
    }

    /* Failure-only diagnostics, so a passing run ends at the verdict like the
     * other tests. These two are the only ways the poll path can be broken
     * without any row noticing. */
    if (!g_cb_calls) {
        printf("\npoll: %llu calls, 0 callbacks\n",
               (unsigned long long)g_poll_calls);
        printf("  nothing reached the callback. In build/bpftime.log:\n"
               "    '[ptxpass] ... matched=N'  probe injected into the PTX\n"
               "    'Copying map fd ...'         the ring reached the GPU\n"
               "    'Ignored dirty pages'        the poll loop losing records\n");
    } else if (!g_ever_covered[0] && !g_ever_covered[1]) {
        printf("\npoll: %llu calls, %llu callbacks, none matched\n",
               (unsigned long long)g_poll_calls,
               (unsigned long long)g_cb_calls);
        printf("  the pid filter is dropping everything, so the device-side\n"
               "  bpf_get_current_pid_tgid is not the host pid\n");
    }

    gpu_sm_bpf__destroy(skel);

    printf("\ngpu_sm_test: %s\n", fail ? "FAIL" : "PASS");
    return fail ? FAIL : PASS;
}
