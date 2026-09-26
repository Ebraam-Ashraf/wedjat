/*
 * k2_tiled_matmul.cu
 *
 * Heavy fixture: tiled (shared-memory) single-precision matrix multiply.
 *
 * Why this is a good wedjat probe target:
 *   - cudaMalloc x3         → EVENT_ALLOC  (3 allocations)
 *   - cudaMemcpy HtoD x2    → EVENT_MEMCPY (A and B matrices into device)
 *   - cuLaunchKernel        → EVENT_LAUNCH  (one heavy SGEMM per iter)
 *   - cuCtxSynchronize      → EVENT_SYNC    (barrier per iter)
 *   - cudaMemcpy DtoH x1    → EVENT_MEMCPY (result back to host)
 *   - cudaFree x3           → EVENT_FREE
 *
 * Matrix size: M=N=K=2048 by default (2048×2048 fp32 ≈ 16 MB each).
 * Overrides via env vars: WEDJAT_M, WEDJAT_N, WEDJAT_K, WEDJAT_ITERS.
 *
 * The tiled kernel uses 32×32 shared-memory tiles — 2 KB smem per block,
 * high register pressure, close to peak FP32 throughput on any NVIDIA GPU.
 */

#include <cuda_runtime.h>

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <vector>
#include <unistd.h>

#define CUDA_CHECK(call)                                                        \
    do {                                                                        \
        cudaError_t err__ = (call);                                             \
        if (err__ != cudaSuccess) {                                             \
            std::fprintf(stderr, "CUDA_CHECK failed %s:%d: %s\n", __FILE__,    \
                         __LINE__, cudaGetErrorString(err__));                  \
            std::exit(1);                                                       \
        }                                                                       \
    } while (0)

static int env_int(const char *name, int fallback)
{
    const char *v = std::getenv(name);
    if (!v || !*v) return fallback;
    char *end;
    long x = std::strtol(v, &end, 10);
    return (end == v || x <= 0) ? fallback : (int)x;
}

/* ── Tiled SGEMM ──────────────────────────────────────────────────────────
 * C = alpha * A * B + beta * C
 * A: M×K   B: K×N   C: M×N   (row-major)
 * Tile size TILE × TILE — must divide evenly or guard the boundary loads.
 */
#define TILE 32

__global__ void tiled_sgemm(const float * __restrict__ A,
                             const float * __restrict__ B,
                             float       * __restrict__ C,
                             int M, int N, int K,
                             float alpha, float beta)
{
    __shared__ float sA[TILE][TILE];
    __shared__ float sB[TILE][TILE];

    int row = blockIdx.y * TILE + threadIdx.y;
    int col = blockIdx.x * TILE + threadIdx.x;

    float acc = 0.0f;

    for (int t = 0; t < (K + TILE - 1) / TILE; ++t) {
        /* load tile of A */
        int aCol = t * TILE + threadIdx.x;
        sA[threadIdx.y][threadIdx.x] =
            (row < M && aCol < K) ? A[row * K + aCol] : 0.0f;

        /* load tile of B */
        int bRow = t * TILE + threadIdx.y;
        sB[threadIdx.y][threadIdx.x] =
            (bRow < K && col < N) ? B[bRow * N + col] : 0.0f;

        __syncthreads();

        /* compute partial dot product */
        #pragma unroll
        for (int k = 0; k < TILE; ++k)
            acc += sA[threadIdx.y][k] * sB[k][threadIdx.x];

        __syncthreads();
    }

    if (row < M && col < N)
        C[row * N + col] = alpha * acc + beta * C[row * N + col];
}

int main()
{
    const int M     = env_int("WEDJAT_M",     2048);
    const int N     = env_int("WEDJAT_N",     2048);
    const int K     = env_int("WEDJAT_K",     2048);
    const int iters = env_int("WEDJAT_ITERS",    5);

    const size_t bytesA = (size_t)M * K * sizeof(float);
    const size_t bytesB = (size_t)K * N * sizeof(float);
    const size_t bytesC = (size_t)M * N * sizeof(float);

    std::printf("WEDJAT_FIXTURE name=k2_tiled_matmul "
                "M=%d N=%d K=%d iters=%d "
                "bytesA=%zu bytesB=%zu bytesC=%zu\n",
                M, N, K, iters, bytesA, bytesB, bytesC);

    /* ── host buffers ────────────────────────────────────────────────── */
    std::vector<float> hA(M * K), hB(K * N), hC(M * N, 0.0f);

    /* fill A and B with a simple pattern */
    for (int i = 0; i < M * K; ++i) hA[i] = (float)(i % 7 + 1) * 0.01f;
    for (int i = 0; i < K * N; ++i) hB[i] = (float)(i % 5 + 1) * 0.01f;

    /* ── device allocations ──────────────────────────────────────────── */
    CUDA_CHECK(cudaSetDevice(0));

    float *dA = nullptr, *dB = nullptr, *dC = nullptr;
    CUDA_CHECK(cudaMalloc(&dA, bytesA));    /* EVENT_ALLOC */
    CUDA_CHECK(cudaMalloc(&dB, bytesB));    /* EVENT_ALLOC */
    CUDA_CHECK(cudaMalloc(&dC, bytesC));    /* EVENT_ALLOC */

    /* ── upload inputs ───────────────────────────────────────────────── */
    CUDA_CHECK(cudaMemcpy(dA, hA.data(), bytesA, cudaMemcpyHostToDevice)); /* EVENT_MEMCPY */
    CUDA_CHECK(cudaMemcpy(dB, hB.data(), bytesB, cudaMemcpyHostToDevice)); /* EVENT_MEMCPY */
    CUDA_CHECK(cudaMemset(dC, 0, bytesC));

    /* ── launch loop ─────────────────────────────────────────────────── */
    dim3 block(TILE, TILE);
    dim3 grid((N + TILE - 1) / TILE, (M + TILE - 1) / TILE);

    for (int i = 0; i < iters; ++i) {
        tiled_sgemm<<<grid, block>>>(dA, dB, dC, M, N, K, 1.0f, 0.0f); /* EVENT_LAUNCH */
        CUDA_CHECK(cudaGetLastError());
        CUDA_CHECK(cudaDeviceSynchronize());                              /* EVENT_SYNC  */

        std::printf("WEDJAT_FIXTURE_EVENT name=k2_tiled_matmul iter=%d "
                    "grid=(%d,%d) block=(%d,%d)\n",
                    i, grid.x, grid.y, block.x, block.y);
        std::fflush(stdout);
    }

    /* ── download result ─────────────────────────────────────────────── */
    CUDA_CHECK(cudaMemcpy(hC.data(), dC, bytesC, cudaMemcpyDeviceToHost)); /* EVENT_MEMCPY */

    /* spot-check: C[0,0] = sum_k A[0,k]*B[k,0] */
    float expected = 0.0f;
    for (int k = 0; k < K; ++k)
        expected += hA[k] * hB[k * N];

    float got = hC[0];
    bool ok   = std::fabs(got - expected) / (std::fabs(expected) + 1e-6f) < 1e-3f;

    std::printf("WEDJAT_FIXTURE_DONE name=k2_tiled_matmul "
                "C[0,0]=%.6f expected=%.6f %s\n",
                got, expected, ok ? "PASS" : "MISMATCH");

    /* ── free ────────────────────────────────────────────────────────── */
    CUDA_CHECK(cudaFree(dA)); /* EVENT_FREE */
    CUDA_CHECK(cudaFree(dB)); /* EVENT_FREE */
    CUDA_CHECK(cudaFree(dC)); /* EVENT_FREE */

    long sleep_ms = env_int("WEDJAT_SLEEP_MS", 0);
    if (sleep_ms > 0) {
        usleep(sleep_ms * 1000);
    }

    return ok ? 0 : 1;
}
