/*
 * kernels_to_trace/k8.cu
 *
 * Multi-stream concurrent launch fixture.
 *
 * WHAT THIS PROGRAM DOES:
 *   Creates NUM_STREAMS independent CUDA streams and launches a simple
 *   kernel on each stream every iteration, without cross-stream barriers.
 *   After all streams have been dispatched for one iteration, it synchronizes
 *   each stream in order before sleeping and moving to the next iteration.
 *
 * WHY IT EXISTS:
 *   k1 uses a single stream (the default stream) — all launches are strictly
 *   sequential from the driver's perspective.  k8 gives the driver real
 *   concurrent work across multiple streams.  This surfaces multi-stream
 *   behavior in:
 *     - cuStreamCreate / cuStreamDestroy  (stream lifecycle)
 *     - cuLaunchKernel                    (multiple, non-default stream args)
 *     - cuStreamSynchronize               (per-stream, not device-wide)
 *     - cuMemAlloc / cuMemFree            (one allocation per stream)
 *
 * CUDA API USED:
 *   CUDA Runtime API (cuda_runtime.h).
 *   Uses cudaStreamCreateWithFlags (non-blocking) so streams compete freely.
 *
 * FIXTURE CONTRACT (required by all k{n} programs):
 *   - reads WEDJAT_ITERS, WEDJAT_SLEEP_MS, WEDJAT_N via env_int()
 *   - prints WEDJAT_FIXTURE on startup
 *   - prints WEDJAT_FIXTURE_EVENT once per outer iteration
 *   - prints WEDJAT_FIXTURE_DONE on clean exit
 *   - returns 0 on success, 1 on CUDA error
 *   - uses CUDA_CHECK for all Runtime API calls
 */

#include <cuda_runtime.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <vector>

/* ============================================================
 * Error-check macro — Runtime API variant.
 * Prints file/line/error string and returns 1 on failure.
 * ============================================================ */
#define CUDA_CHECK(call)                                                    \
    do {                                                                    \
        cudaError_t err__ = (call);                                         \
        if (err__ != cudaSuccess) {                                         \
            std::fprintf(stderr, "CUDA_CHECK failed %s:%d: %s\n",           \
                         __FILE__, __LINE__, cudaGetErrorString(err__));    \
            return 1;                                                       \
        }                                                                   \
    } while (0)

/* ============================================================
 * env_int — read an integer environment variable with a fallback.
 * ============================================================ */
static int env_int(const char *name, int fallback)
{
    const char *value = std::getenv(name);
    if (value == nullptr || value[0] == '\0')
        return fallback;
    char *end = nullptr;
    long parsed = std::strtol(value, &end, 10);
    if (end == value || parsed <= 0)
        return fallback;
    return static_cast<int>(parsed);
}

/* ============================================================
 * GPU KERNEL
 *
 * Simple element-wise scale+bias.  One instance runs per stream per
 * iteration.  The work is trivial — the point is the cuLaunchKernel
 * call pattern, not GPU compute throughput.
 * ============================================================ */
__global__ void scale_kernel(float *data, float scale, int n)
{
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx < n)
        data[idx] *= scale;
}

/* ============================================================
 * CONSTANTS
 * ============================================================ */
#define NUM_STREAMS 4   /* number of concurrent streams per iteration */

/* ============================================================
 * MAIN
 * ============================================================ */
int main()
{
    const int    iters    = env_int("WEDJAT_ITERS",    20);
    const int    sleep_ms = env_int("WEDJAT_SLEEP_MS", 100);
    const int    n        = env_int("WEDJAT_N",        1 << 16); /* 64K floats per stream */
    const size_t bytes    = static_cast<size_t>(n) * sizeof(float);

    std::printf(
        "WEDJAT_FIXTURE name=k8_multistream_launch"
        " streams=%d n=%d bytes_per_stream=%zu iters=%d\n",
        NUM_STREAMS, n, bytes, iters);

    /* -------------------------------------------------------
     * Device setup
     * ------------------------------------------------------- */
    CUDA_CHECK(cudaSetDevice(0));

    /* -------------------------------------------------------
     * Allocate one device buffer per stream.
     * Each stream operates on its own buffer to avoid false sharing.
     * ------------------------------------------------------- */
    float *device[NUM_STREAMS] = {};
    for (int s = 0; s < NUM_STREAMS; s++) {
        CUDA_CHECK(cudaMalloc(&device[s], bytes));
        /* Initialize to 1.0 from a host-side vector. */
        std::vector<float> host(n, 1.0f);
        CUDA_CHECK(cudaMemcpy(device[s], host.data(), bytes,
                              cudaMemcpyHostToDevice));
    }

    /* -------------------------------------------------------
     * Create non-blocking streams.
     *
     * cudaStreamNonBlocking means these streams do not synchronize
     * with stream 0 (the default stream).  They only sync with each
     * other via explicit barriers.  This gives the driver the most
     * freedom to overlap execution across streams.
     * ------------------------------------------------------- */
    cudaStream_t streams[NUM_STREAMS] = {};
    for (int s = 0; s < NUM_STREAMS; s++) {
        CUDA_CHECK(cudaStreamCreateWithFlags(&streams[s],
                                             cudaStreamNonBlocking));
    }

    /* -------------------------------------------------------
     * Kernel launch parameters — same for all streams.
     * ------------------------------------------------------- */
    const int block = 256;
    const int grid  = (n + block - 1) / block;

    /* -------------------------------------------------------
     * Main loop
     * ------------------------------------------------------- */
    for (int i = 0; i < iters; i++) {
        /*
         * Dispatch: launch the kernel on every stream without waiting.
         * From the driver's view this is NUM_STREAMS concurrent launches.
         * Each call produces one cuLaunchKernel event in the ring buffer.
         */
        for (int s = 0; s < NUM_STREAMS; s++) {
            scale_kernel<<<grid, block, 0, streams[s]>>>(
                device[s], 1.0001f, n);
            CUDA_CHECK(cudaGetLastError());
        }

        /*
         * Drain: wait for each stream in order.
         * Each call blocks the CPU thread until that stream's GPU work
         * finishes.  Produces one cuStreamSynchronize event per stream.
         */
        for (int s = 0; s < NUM_STREAMS; s++) {
            CUDA_CHECK(cudaStreamSynchronize(streams[s]));
        }

        std::printf(
            "WEDJAT_FIXTURE_EVENT name=k8_multistream_launch"
            " iter=%d launches=%d syncs=%d\n",
            i, NUM_STREAMS, NUM_STREAMS);
        std::fflush(stdout);

        if (sleep_ms > 0)
            std::this_thread::sleep_for(
                std::chrono::milliseconds(sleep_ms));
    }

    /* -------------------------------------------------------
     * Cleanup
     * ------------------------------------------------------- */
    for (int s = 0; s < NUM_STREAMS; s++) {
        CUDA_CHECK(cudaStreamDestroy(streams[s]));
        CUDA_CHECK(cudaFree(device[s]));
    }

    std::printf("WEDJAT_FIXTURE_DONE name=k8_multistream_launch\n");
    return 0;
}
