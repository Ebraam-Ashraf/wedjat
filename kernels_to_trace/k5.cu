#include <cuda_runtime.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <vector>
#include <unistd.h>

#define CUDA_CHECK(call)                                                        \
    do {                                                                        \
        cudaError_t err__ = (call);                                             \
        if (err__ != cudaSuccess) {                                             \
            std::fprintf(stderr, "CUDA_CHECK failed %s:%d: %s\n", __FILE__,     \
                         __LINE__, cudaGetErrorString(err__));                  \
            return 1;                                                           \
        }                                                                       \
    } while (0)

static int env_int(const char *name, int fallback) {
    const char *value = std::getenv(name);
    if (value == nullptr || value[0] == '\0') {
        return fallback;
    }

    char *end = nullptr;
    long parsed = std::strtol(value, &end, 10);

    if (end == value || parsed <= 0) {
        return fallback;
    }

    return static_cast<int>(parsed);
}

/*
 * Heavy matrix multiplication.
 *
 * C = A * B
 *
 * Each thread computes one element of C.
 * TILE x TILE threads cooperate on tiles of A and B.
 */
__global__ void matrix_mul_kernel(const float *A,
                                  const float *B,
                                  float *C,
                                  int N) {
    constexpr int TILE = 32;

    __shared__ float As[TILE][TILE];
    __shared__ float Bs[TILE][TILE];

    const int row = blockIdx.y * TILE + threadIdx.y;
    const int col = blockIdx.x * TILE + threadIdx.x;

    float sum = 0.0f;

    const int num_tiles = (N + TILE - 1) / TILE;

    for (int tile = 0; tile < num_tiles; ++tile) {

        const int a_col = tile * TILE + threadIdx.x;
        const int b_row = tile * TILE + threadIdx.y;

        if (row < N && a_col < N)
            As[threadIdx.y][threadIdx.x] = A[row * N + a_col];
        else
            As[threadIdx.y][threadIdx.x] = 0.0f;

        if (b_row < N && col < N)
            Bs[threadIdx.y][threadIdx.x] = B[b_row * N + col];
        else
            Bs[threadIdx.y][threadIdx.x] = 0.0f;

        __syncthreads();

        #pragma unroll
        for (int k = 0; k < TILE; ++k) {
            sum += As[threadIdx.y][k] * Bs[k][threadIdx.x];
        }

        __syncthreads();
    }

    if (row < N && col < N) {
        C[row * N + col] = sum;
    }
}

/*
 * Explicitly call the exported cudaLaunchKernel symbol.
 *
 * This is intentional: bpftime hooks cudaLaunchKernel(), while
 * CUDA's <<<>>> syntax can resolve through __cudaLaunchKernel().
 */
extern "C" cudaError_t cudaLaunchKernel(const void *func,
                                        dim3 gridDim,
                                        dim3 blockDim,
                                        void **args,
                                        size_t sharedMem,
                                        cudaStream_t stream);

static cudaError_t launch_matrix_mul(const float *A,
                                     const float *B,
                                     float *C,
                                     int N,
                                     int grid_x,
                                     int grid_y,
                                     int block_x,
                                     int block_y) {

    void *args[] = {
        const_cast<float **>(&A),
        const_cast<float **>(&B),
        &C,
        &N
    };

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(matrix_mul_kernel),
        dim3(grid_x, grid_y, 1),
        dim3(block_x, block_y, 1),
        args,
        0,
        0
    );
}

int main() {

    /*
     * Default:
     *
     * N = 4096
     *
     * A = 4096 x 4096
     * B = 4096 x 4096
     * C = 4096 x 4096
     *
     * Each matrix:
     *
     * 4096 * 4096 * 4 = 64 MB
     *
     * Total ~= 192 MB
     *
     * One GEMM:
     *
     * 2 * N^3 ~= 137 billion FLOPs
     */
    const int N = env_int("WEDJAT_MATRIX_N", 4096);
    const int iters = env_int("WEDJAT_ITERS", 10);
    const int sleep_ms = env_int("WEDJAT_SLEEP_MS", 100);

    const size_t elements =
        static_cast<size_t>(N) * static_cast<size_t>(N);

    const size_t matrix_bytes =
        elements * sizeof(float);

    const size_t total_bytes =
        matrix_bytes * 3;

    std::printf(
        "WEDJAT_FIXTURE name=k2_heavy_matmul "
        "N=%d matrix_bytes=%zu total_bytes=%zu iters=%d\n",
        N,
        matrix_bytes,
        total_bytes,
        iters
    );

    std::printf(
        "WEDJAT_FIXTURE_INFO "
        "estimated_flops_per_iter=%llu\n",
        static_cast<unsigned long long>(2ULL * N * N * N)
    );

    std::fflush(stdout);

    CUDA_CHECK(cudaSetDevice(0));

    float *A = nullptr;
    float *B = nullptr;
    float *C = nullptr;

    CUDA_CHECK(cudaMalloc(&A, matrix_bytes));
    CUDA_CHECK(cudaMalloc(&B, matrix_bytes));
    CUDA_CHECK(cudaMalloc(&C, matrix_bytes));

    /*
     * Initialize matrices.
     *
     * We use cudaMemset rather than another kernel so the fixture
     * has a predictable launch pattern.
     */
    CUDA_CHECK(cudaMemset(A, 0x3f, matrix_bytes));
    CUDA_CHECK(cudaMemset(B, 0x3f, matrix_bytes));
    CUDA_CHECK(cudaMemset(C, 0x00, matrix_bytes));

    constexpr int BLOCK = 32;

    const int GRID =
        (N + BLOCK - 1) / BLOCK;

    std::printf(
        "WEDJAT_FIXTURE_CONFIG "
        "grid=%dx%d block=%dx%d\n",
        GRID,
        GRID,
        BLOCK,
        BLOCK
    );

    std::fflush(stdout);

    /*
     * Warm-up.
     *
     * This makes sure CUDA has initialized the context and that
     * the first timed iteration isn't dominated by initialization.
     */
    std::printf("WEDJAT_FIXTURE_WARMUP_BEGIN\n");
    std::fflush(stdout);

    CUDA_CHECK(
        launch_matrix_mul(
            A,
            B,
            C,
            N,
            GRID,
            GRID,
            BLOCK,
            BLOCK
        )
    );

    CUDA_CHECK(cudaGetLastError());
    CUDA_CHECK(cudaDeviceSynchronize());

    std::printf("WEDJAT_FIXTURE_WARMUP_DONE\n");
    std::fflush(stdout);

    /*
     * Heavy workload.
     */
    for (int i = 0; i < iters; ++i) {

        const auto start = std::chrono::steady_clock::now();

        CUDA_CHECK(
            launch_matrix_mul(
                A,
                B,
                C,
                N,
                GRID,
                GRID,
                BLOCK,
                BLOCK
            )
        );

        CUDA_CHECK(cudaGetLastError());

        CUDA_CHECK(cudaDeviceSynchronize());

        const auto end = std::chrono::steady_clock::now();

        const double ms =
            std::chrono::duration<double, std::milli>(
                end - start
            ).count();

        std::printf(
            "WEDJAT_FIXTURE_EVENT "
            "name=k2_heavy_matmul "
            "iter=%d "
            "N=%d "
            "grid=%dx%d "
            "block=%dx%d "
            "elapsed_ms=%.3f\n",
            i,
            N,
            GRID,
            GRID,
            BLOCK,
            BLOCK,
            ms
        );

        std::fflush(stdout);

        if (sleep_ms > 0) {
            std::this_thread::sleep_for(
                std::chrono::milliseconds(sleep_ms)
            );
        }
    }

    /*
     * Copy one result back so the computation has a visible result.
     */
    float sample = 0.0f;

    CUDA_CHECK(
        cudaMemcpy(
            &sample,
            C,
            sizeof(float),
            cudaMemcpyDeviceToHost
        )
    );

    CUDA_CHECK(cudaFree(A));
    CUDA_CHECK(cudaFree(B));
    CUDA_CHECK(cudaFree(C));

    std::printf(
        "WEDJAT_FIXTURE_DONE "
        "name=k2_heavy_matmul "
        "sample=%f\n",
        sample
    );

    std::fflush(stdout);

    if (sleep_ms > 0) {
        usleep(static_cast<useconds_t>(sleep_ms) * 1000);
    }

    return 0;
}