/*
 * k9_incident_trigger.cu
 *
 * ============================================================
 * INCIDENT TRIGGER FIXTURE
 * ============================================================
 *
 * This fixture is designed to trigger Wedjat incident recording
 * for testing the incident pipeline end-to-end.
 *
 * It targets ALL THREE incident types:
 *
 *   1. sync_stall  (LatencyNs >= 250ms)
 *      → Achieved by running a deliberately slow kernel that
 *        holds the GPU for >250ms before cudaDeviceSynchronize()
 *        returns.
 *
 *   2. sync_hang   (FlagHungSync set by the hung-sync scanner)
 *      → Achieved by a kernel that does NOT return for >2 seconds. The
 *        hang kernel uses __nanosleep so the GPU watchdog does not kill it;
 *        cudaDeviceSynchronize() stays in flight past the 2 s threshold and
 *        the eBPF scanner reports a sync_hang incident.
 *
 *   3. xid         (NVIDIA driver Xid interrupt)
 *      → CANNOT be reliably triggered from user space. Xid errors
 *        are hardware/driver interrupts (ECC, thermal, power,
 *        NVLink). This fixture documents the trigger but cannot
 *        produce it without hardware fault injection tools
 *        (e.g., nvidia-smi -ecc, thermal throttling, power capping).
 *
 * RUN:
 *   make build/k9
 *   sudo ./build/k9          # needs GPU access
 *
 * EXPECTED INCIDENTS:
 *   - sync_stall:  HIGH probability (long kernel + sync)
 *   - sync_hang:   HIGH probability (__nanosleep keeps the sync in flight)
 *   - xid:         ZERO probability (requires hardware fault)
 *
 * Check incidents via UI: /incidents page or
 *   curl http://localhost:3000/api/incidents?limit=100
 */

#include <cuda_runtime.h>

#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <vector>
#include <unistd.h>

#define CUDA_CHECK(call)                                                        \
    do {                                                                        \
        cudaError_t err__ = (call);                                             \
        if (err__ != cudaSuccess) {                                             \
            std::fprintf(stderr,                                               \
                         "CUDA_CHECK failed %s:%d: %s\n",                       \
                         __FILE__, __LINE__, cudaGetErrorString(err__));        \
            return 1;                                                           \
        }                                                                        \
    } while (0)

static int env_int(const char *name, int fallback)
{
    const char *value = std::getenv(name);
    if (!value || !value[0]) return fallback;
    char *end = nullptr;
    long parsed = std::strtol(value, &end, 10);
    if (end == value || parsed <= 0) return fallback;
    return static_cast<int>(parsed);
}

/*
 * ----------------------------------------------------------------------------
 * KERNEL 1: STALL KERNEL
 * ----------------------------------------------------------------------------
 * Runs for approximately STALL_MS milliseconds by busy-waiting on the GPU.
 * When cudaDeviceSynchronize() is called after this launch, the sync
 * latency will be >= STALL_MS, triggering sync_stall incident.
 *
 * Default STALL_MS = 300 (exceeds 250ms threshold).
 * Override: WEDJAT_STALL_MS=500 ./build/k9
 */
__global__ void stall_kernel(int stall_ms)
{
    // Use conservative cycle estimate: assume 1.0 GHz effective clock
    // 1ms = 1,000,000 cycles at 1 GHz
    unsigned long long start = clock64();
    unsigned long long target_cycles = (unsigned long long)stall_ms * 1000000ULL;

    while (clock64() - start < target_cycles) {
        asm volatile("" ::: "memory");
    }
}

/*
 * ----------------------------------------------------------------------------
 * KERNEL 2: HANG KERNEL
 * ----------------------------------------------------------------------------
 * Runs for HANG_MS milliseconds so that cudaDeviceSynchronize() stays in
 * flight past the 2000 ms hung-sync threshold, letting the eBPF hung-sync
 * scanner (scanHungSyncs) detect and report a sync_hang incident.
 *
 * A pure busy-spin trips the GPU watchdog (~1500 ms on this driver), which
 * kills the kernel and returns the sync before the 2 s threshold — that is
 * why the spin variant never produced a hang.  __nanosleep puts the block to
 * sleep, which is normal GPU behaviour the watchdog does not interpret as a
 * hang, so the kernel runs for the full HANG_MS and the inflight entry lives
 * long enough to be scanned.
 *
 * Override: WEDJAT_HANG_MS=3000 ./build/k9
 */
__global__ void hang_kernel(int hang_ms)
{
    __nanosleep((long long)hang_ms * 1000000LL);
}

/*
 * ----------------------------------------------------------------------------
 * IMPORTANT: Explicit cudaLaunchKernel for bpftime interception
 * ----------------------------------------------------------------------------
 */
extern "C" cudaError_t cudaLaunchKernel(
    const void *func,
    dim3 gridDim,
    dim3 blockDim,
    void **args,
    size_t sharedMem,
    cudaStream_t stream
);

static cudaError_t launch_stall(int stall_ms)
{
    void *args[] = { &stall_ms };
    return cudaLaunchKernel(
        reinterpret_cast<const void *>(stall_kernel),
        dim3(1), dim3(1), args, 0, 0);
}

static cudaError_t launch_hang(int hang_ms)
{
    void *args[] = { &hang_ms };
    return cudaLaunchKernel(
        reinterpret_cast<const void *>(hang_kernel),
        dim3(1), dim3(1), args, 0, 0);
}

int main()
{
    const int stall_ms  = env_int("WEDJAT_STALL_MS",  500);  // >250ms threshold, conservative for clock variance
    const int hang_ms   = env_int("WEDJAT_HANG_MS",   4000); // >2000ms hung threshold; __nanosleep keeps the sync in flight
    const int iters     = env_int("WEDJAT_ITERS",       3);
    const int sleep_ms  = env_int("WEDJAT_SLEEP_MS",   2000); // longer sleep to let GPU cool

    std::printf(
        "\n"
        "====================================================\n"
        " WEDJAT K9 INCIDENT TRIGGER FIXTURE\n"
        "====================================================\n"
        " stall_ms  : %d ms (target sync_stall: >=250ms)\n"
        " hang_ms   : %d ms (target sync_hang:  >2000ms)\n"
        " iters     : %d\n"
        "====================================================\n\n",
        stall_ms, hang_ms, iters);

    std::printf(
        "NOTE: xid incidents CANNOT be triggered from user space.\n"
        "      They require hardware faults (ECC, thermal, power, NVLink).\n"
        "      Use nvidia-smi or hardware fault injection for xid testing.\n\n");

    std::fflush(stdout);

    CUDA_CHECK(cudaSetDevice(0));

    cudaDeviceProp prop{};
    CUDA_CHECK(cudaGetDeviceProperties(&prop, 0));
    std::printf("GPU: %s\n\n", prop.name);
    std::fflush(stdout);

    // Warm-up
    std::printf("Warm-up...\n");
    CUDA_CHECK(launch_stall(10));
    CUDA_CHECK(cudaDeviceSynchronize());
    std::printf("Warm-up done.\n\n");
    std::fflush(stdout);

    for (int i = 0; i < iters; ++i) {
        std::printf("=== Iteration %d/%d ===\n", i + 1, iters);
        std::fflush(stdout);

        // ---------------------------------------------------------
        // PHASE 1: TRIGGER sync_stall
        // ---------------------------------------------------------
        std::printf("[%d] Launching stall kernel (%d ms)...\n", i, stall_ms);
        std::fflush(stdout);

        auto stall_start = std::chrono::steady_clock::now();
        CUDA_CHECK(launch_stall(stall_ms));
        CUDA_CHECK(cudaGetLastError());

        // This sync should take >= stall_ms and trigger sync_stall
        CUDA_CHECK(cudaDeviceSynchronize());
        auto stall_end = std::chrono::steady_clock::now();
        double stall_elapsed = std::chrono::duration<double, std::milli>(stall_end - stall_start).count();

        std::printf("[%d] cudaDeviceSynchronize() returned after %.1f ms\n", i, stall_elapsed);
        std::fflush(stdout);

        if (stall_elapsed >= 250) {
            std::printf("[%d] ✓ sync_stall threshold exceeded (%.1f ms >= 250 ms)\n", i, stall_elapsed);
        } else {
            std::printf("[%d] ✗ sync_stall NOT triggered (%.1f ms < 250 ms)\n", i, stall_elapsed);
        }
        std::fflush(stdout);

        // ---------------------------------------------------------
        // PHASE 2: TRIGGER sync_hang
        // ---------------------------------------------------------
        std::printf("[%d] Launching hang kernel (%d ms, __nanosleep)...\n", i, hang_ms);
        std::fflush(stdout);

        auto hang_start = std::chrono::steady_clock::now();
        CUDA_CHECK(launch_hang(hang_ms));
        CUDA_CHECK(cudaGetLastError());

        // __nanosleep keeps the block alive for the full hang_ms, so the sync
        // stays in flight past the 2 s hung-sync threshold and the eBPF
        // scanner reports a sync_hang incident.
        CUDA_CHECK(cudaDeviceSynchronize());
        auto hang_end = std::chrono::steady_clock::now();
        double hang_elapsed = std::chrono::duration<double, std::milli>(hang_end - hang_start).count();

        std::printf("[%d] cudaDeviceSynchronize() returned after %.1f ms\n", i, hang_elapsed);
        std::fflush(stdout);

        if (hang_elapsed >= 2000) {
            std::printf("[%d] ⚠ sync_hang window exceeded (%.1f ms >= 2000 ms)\n", i, hang_elapsed);
            std::printf("[%d]    Check incidents: curl localhost:3000/api/incidents\n", i);
        } else {
            std::printf("[%d] sync_hang NOT triggered (%.1f ms < 2000 ms)\n", i, hang_elapsed);
        }
        std::fflush(stdout);

        // ---------------------------------------------------------
        // PHASE 3: xid (documentation only)
        // ---------------------------------------------------------
        std::printf("[%d] xid: Not triggerable from user space.\n", i);
        std::printf("[%d]      Requires: nvidia-smi -ecc, thermal, power, NVLink fault\n", i);
        std::fflush(stdout);

        if (i < iters - 1 && sleep_ms > 0) {
            std::printf("[%d] Sleeping %d ms before next iteration...\n\n", i, sleep_ms);
            std::fflush(stdout);
            std::this_thread::sleep_for(std::chrono::milliseconds(sleep_ms));
        }
    }

    std::printf("\n=== All iterations complete ===\n");
    std::printf("Check incidents: curl http://localhost:3000/api/incidents?limit=100\n");
    std::printf("Or open UI: http://localhost:3000/incidents\n");
    std::fflush(stdout);

    if (sleep_ms > 0) {
        usleep(sleep_ms * 1000);
    }

    return 0;
}