/*
 * k3_uvm_reduction.cu
 *
 * Heavy fixture: multi-pass parallel reduction over a Unified Virtual Memory
 * (UVM) array, using asynchronous stream-based memcpys and many kernel
 * launches.
 *
 * Why this is a good wedjat probe target:
 *   - cuMemAllocManaged         → EVENT_ALLOC     (UVM buffer)
 *   - cudaMalloc (scratch)      → EVENT_ALLOC     (device-only scratch)
 *   - cudaMemcpyAsync HtoD      → EVENT_MEMCPY    (async stream upload)
 *   - cuLaunchKernel            → EVENT_LAUNCH    (reduce_pass + finalize)
 *   - cuStreamSynchronize       → EVENT_SYNC      (per-pass barrier)
 *   - UVM page faults           → EVENT_UVM_FAULT (kprobe fires when GPU
 *                                  touches pages still on CPU)
 *   - cudaMemcpyAsync DtoH      → EVENT_MEMCPY    (partial sums back)
 *   - cudaFree x2               → EVENT_FREE
 *
 * The reduction:
 *   Pass 0 : N floats  → N/BLOCK partial sums  (each block does a warp-shuffle tree)
 *   Pass 1 : N/BLOCK   → N/BLOCK²              (second level)
 *   …until one block remains.
 *   Final  : single kernel reads the last-level array and writes a scalar.
 *
 * Default: N = 2^24 (≈ 64 M floats = 256 MB), ITERS = 8 repeated reductions.
 * Override: WEDJAT_N (power-of-2 recommended), WEDJAT_ITERS.
 *
 * The UVM buffer for the input is intentionally NOT prefetched — so the GPU
 * will demand-fault on first access, which drives kprobe/uvm_va_block_service_fault
 * events in driver_kprobes.bpf.c.
 */

#include <cuda_runtime.h>

#include <cassert>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <vector>
#include <unistd.h>

#define CUDA_CHECK(call)                                                        \
    do {                                                                        \
        cudaError_t err__ = (call);                                             \
        if (err__ != cudaSuccess) {                                             \
            std::fprintf(stderr, "CUDA_CHECK failed %s:%d: %s\n",              \
                         __FILE__, __LINE__, cudaGetErrorString(err__));        \
            std::exit(1);                                                       \
        }                                                                       \
    } while (0)

static long env_long(const char *name, long fallback)
{
    const char *v = std::getenv(name);
    if (!v || !*v) return fallback;
    char *end;
    long x = std::strtol(v, &end, 10);
    return (end == v || x <= 0) ? fallback : x;
}

/* ── Reduction kernel ─────────────────────────────────────────────────────
 * Each block reduces BLOCK elements to one partial sum using warp shuffles.
 * 'len' may be smaller than gridDim.x * BLOCK — guard loads accordingly.
 */
#define BLOCK 256

__global__ void reduce_pass(const float * __restrict__ in,
                             float       * __restrict__ out,
                             long len)
{
    /* warp-shuffle tree inside each warp, then inter-warp via shared mem */
    __shared__ float smem[BLOCK / 32];  /* one slot per warp */

    long gid = (long)blockIdx.x * BLOCK + threadIdx.x;
    float val = (gid < len) ? in[gid] : 0.0f;

    /* intra-warp reduction */
    for (int offset = 16; offset > 0; offset >>= 1)
        val += __shfl_down_sync(0xffffffff, val, offset);

    /* warp leaders write to shared memory */
    int lane = threadIdx.x & 31;
    int wid  = threadIdx.x >> 5;
    if (lane == 0) smem[wid] = val;
    __syncthreads();

    /* final inter-warp reduction in the first warp */
    val = (threadIdx.x < (BLOCK / 32)) ? smem[threadIdx.x] : 0.0f;
    if (wid == 0) {
        for (int offset = (BLOCK / 32) / 2; offset > 0; offset >>= 1)
            val += __shfl_down_sync(0xffffffff, val, offset);
        if (lane == 0) out[blockIdx.x] = val;
    }
}

/* Final kernel: reduce the last partial array (fits in one block) */
__global__ void reduce_final(const float * __restrict__ in,
                              float       * __restrict__ scalar_out,
                              long len)
{
    __shared__ float smem[BLOCK / 32];

    long gid = (long)threadIdx.x;
    float val = (gid < len) ? in[gid] : 0.0f;

    for (int offset = 16; offset > 0; offset >>= 1)
        val += __shfl_down_sync(0xffffffff, val, offset);

    int lane = threadIdx.x & 31;
    int wid  = threadIdx.x >> 5;
    if (lane == 0) smem[wid] = val;
    __syncthreads();

    val = (threadIdx.x < (BLOCK / 32)) ? smem[threadIdx.x] : 0.0f;
    if (wid == 0) {
        for (int offset = (BLOCK / 32) / 2; offset > 0; offset >>= 1)
            val += __shfl_down_sync(0xffffffff, val, offset);
        if (lane == 0) *scalar_out = val;
    }
}

int main()
{
    const long N     = env_long("WEDJAT_N",     1L << 24); /* 16 M floats = 64 MB */
    const int  iters = (int)env_long("WEDJAT_ITERS",    8);

    const size_t bytesN = (size_t)N * sizeof(float);

    std::printf("WEDJAT_FIXTURE name=k3_uvm_reduction "
                "N=%ld bytes=%zu iters=%d\n",
                N, bytesN, iters);

    CUDA_CHECK(cudaSetDevice(0));

    /* ── UVM input buffer (intentionally not prefetched → UVM faults) ── */
    float *uvm_in = nullptr;
    CUDA_CHECK(cudaMallocManaged(&uvm_in, bytesN)); /* EVENT_ALLOC (UVM) */

    /* initialise from CPU — each float = 1.0f so the known sum = N */
    for (long i = 0; i < N; ++i) uvm_in[i] = 1.0f;

    /* ── device-side scratch (two ping-pong buffers for multi-pass) ──── */
    long   pass0_blocks = (N     + BLOCK - 1) / BLOCK;
    long   pass1_blocks = (pass0_blocks + BLOCK - 1) / BLOCK;

    float *d_tmp0 = nullptr, *d_tmp1 = nullptr, *d_scalar = nullptr;
    CUDA_CHECK(cudaMalloc(&d_tmp0,   pass0_blocks * sizeof(float))); /* EVENT_ALLOC */
    CUDA_CHECK(cudaMalloc(&d_tmp1,   pass1_blocks * sizeof(float))); /* EVENT_ALLOC */
    CUDA_CHECK(cudaMalloc(&d_scalar, sizeof(float)));                 /* EVENT_ALLOC */

    /* ── create a stream so async copies fire ────────────────────────── */
    cudaStream_t stream;
    CUDA_CHECK(cudaStreamCreate(&stream));

    /* ── main loop ───────────────────────────────────────────────────── */
    for (int it = 0; it < iters; ++it) {

        /*
         * Async upload of a fresh host-side chunk into a regular device buf
         * (in addition to the UVM path) so EVENT_MEMCPY fires reliably on
         * every iteration even after UVM pages have migrated to the GPU.
         */
        size_t chunk = pass0_blocks * sizeof(float);
        CUDA_CHECK(cudaMemcpyAsync(d_tmp0, uvm_in, chunk,        /* EVENT_MEMCPY (HtoD) */
                                   cudaMemcpyDefault, stream));

        /* Pass 0: N floats → pass0_blocks partial sums
         * GPU accesses uvm_in → UVM page faults fire here on first iter     */
        reduce_pass<<<pass0_blocks, BLOCK, 0, stream>>>(           /* EVENT_LAUNCH */
                uvm_in, d_tmp0, N);
        CUDA_CHECK(cudaGetLastError());
        CUDA_CHECK(cudaStreamSynchronize(stream));                  /* EVENT_SYNC  */

        /* Pass 1: pass0_blocks → pass1_blocks partial sums */
        reduce_pass<<<pass1_blocks, BLOCK, 0, stream>>>(           /* EVENT_LAUNCH */
                d_tmp0, d_tmp1, pass0_blocks);
        CUDA_CHECK(cudaGetLastError());
        CUDA_CHECK(cudaStreamSynchronize(stream));                  /* EVENT_SYNC  */

        /* Final pass: remaining elements fit in one block */
        reduce_final<<<1, BLOCK, 0, stream>>>(                     /* EVENT_LAUNCH */
                d_tmp1, d_scalar, pass1_blocks);
        CUDA_CHECK(cudaGetLastError());
        CUDA_CHECK(cudaStreamSynchronize(stream));                  /* EVENT_SYNC  */

        /* Pull result back for verification */
        float host_result = 0.0f;
        CUDA_CHECK(cudaMemcpyAsync(&host_result, d_scalar,         /* EVENT_MEMCPY (DtoH) */
                                   sizeof(float),
                                   cudaMemcpyDeviceToHost, stream));
        CUDA_CHECK(cudaStreamSynchronize(stream));                  /* EVENT_SYNC  */

        float rel_err = std::fabs(host_result - (float)N) / (float)N;
        std::printf("WEDJAT_FIXTURE_EVENT name=k3_uvm_reduction iter=%d "
                    "sum=%.2f expected=%ld rel_err=%.2e %s\n",
                    it, host_result, N, rel_err,
                    rel_err < 1e-4f ? "ok" : "MISMATCH");
        std::fflush(stdout);
    }

    /* ── cleanup ─────────────────────────────────────────────────────── */
    CUDA_CHECK(cudaStreamDestroy(stream));
    CUDA_CHECK(cudaFree(uvm_in));   /* EVENT_FREE (UVM) */
    CUDA_CHECK(cudaFree(d_tmp0));   /* EVENT_FREE */
    CUDA_CHECK(cudaFree(d_tmp1));   /* EVENT_FREE */
    CUDA_CHECK(cudaFree(d_scalar)); /* EVENT_FREE */

    std::printf("WEDJAT_FIXTURE_DONE name=k3_uvm_reduction\n");
    
    long sleep_ms = env_long("WEDJAT_SLEEP_MS", 0);
    if (sleep_ms > 0) {
        usleep(sleep_ms * 1000);
    }

    return 0;
}
