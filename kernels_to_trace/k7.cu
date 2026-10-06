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

static int env_int(const char *name, int fallback) {
    const char *value = std::getenv(name);

    if (!value || !value[0])
        return fallback;

    char *end = nullptr;
    long parsed = std::strtol(value, &end, 10);

    if (end == value || parsed <= 0)
        return fallback;

    return static_cast<int>(parsed);
}


/*
 * ============================================================
 *  KERNEL 1
 *  Heavy compute kernel
 * ============================================================
 *
 * Doesn't allocate additional memory.
 *
 * The purpose is to keep the GPU computationally busy.
 */
__global__ void compute_kernel(float *data, int n, int rounds) {

    int idx = blockIdx.x * blockDim.x + threadIdx.x;

    if (idx >= n)
        return;

    float x = data[idx];

    for (int r = 0; r < rounds; ++r) {

        x = x * 1.000001f + 0.000001f;

        x = sinf(x);
        x = cosf(x);

        x = sqrtf(fabsf(x) + 1.0f);

        x = x * x + 0.123456f;

        x = logf(x + 1.000001f);

        x = expf(x * 0.001f);

        /*
         * Prevent the compiler from turning the whole
         * calculation into something trivial.
         */
        if (x > 1000.0f)
            x *= 0.5f;
    }

    data[idx] = x;
}


/*
 * ============================================================
 *  KERNEL 2
 *  Memory bandwidth / transformation workload
 * ============================================================
 */
__global__ void memory_kernel(float *data, int n, int rounds) {

    int idx = blockIdx.x * blockDim.x + threadIdx.x;

    if (idx >= n)
        return;

    float x = data[idx];

    for (int i = 0; i < rounds; ++i) {

        x += data[(idx + 1) % n] * 0.0001f;
        x *= 1.00001f;
        x -= data[(idx + 17) % n] * 0.00001f;

        if (x > 100.0f)
            x *= 0.5f;
    }

    data[idx] = x;
}


/*
 * ============================================================
 *  KERNEL 3
 *  Large matrix multiplication
 * ============================================================
 */
__global__ void matrix_mul_kernel(const float *A,
                                  const float *B,
                                  float *C,
                                  int N) {

    constexpr int TILE = 16;

    __shared__ float As[TILE][TILE];
    __shared__ float Bs[TILE][TILE];

    const int row =
        blockIdx.y * TILE + threadIdx.y;

    const int col =
        blockIdx.x * TILE + threadIdx.x;

    float sum = 0.0f;

    const int tiles =
        (N + TILE - 1) / TILE;

    for (int tile = 0; tile < tiles; ++tile) {

        const int a_col =
            tile * TILE + threadIdx.x;

        const int b_row =
            tile * TILE + threadIdx.y;

        if (row < N && a_col < N)
            As[threadIdx.y][threadIdx.x] =
                A[row * N + a_col];
        else
            As[threadIdx.y][threadIdx.x] = 0.0f;

        if (b_row < N && col < N)
            Bs[threadIdx.y][threadIdx.x] =
                B[b_row * N + col];
        else
            Bs[threadIdx.y][threadIdx.x] = 0.0f;

        __syncthreads();

        #pragma unroll
        for (int k = 0; k < TILE; ++k) {

            sum +=
                As[threadIdx.y][k] *
                Bs[k][threadIdx.x];
        }

        __syncthreads();
    }

    if (row < N && col < N)
        C[row * N + col] = sum;
}


/*
 * ============================================================
 *  IMPORTANT
 *
 *  Explicit exported cudaLaunchKernel().
 *
 *  This keeps the fixture on the same bpftime interception
 *  path you are testing.
 * ============================================================
 */
extern "C" cudaError_t cudaLaunchKernel(
    const void *func,
    dim3 gridDim,
    dim3 blockDim,
    void **args,
    size_t sharedMem,
    cudaStream_t stream
);


/*
 * ============================================================
 *  Launch helpers
 * ============================================================
 */

static cudaError_t launch_compute(float *data,
                                  int n,
                                  int rounds) {

    void *args[] = {
        &data,
        &n,
        &rounds
    };

    const int block = 256;
    const int grid =
        (n + block - 1) / block;

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(compute_kernel),
        dim3(grid),
        dim3(block),
        args,
        0,
        0
    );
}


static cudaError_t launch_memory(float *data,
                                 int n,
                                 int rounds) {

    void *args[] = {
        &data,
        &n,
        &rounds
    };

    const int block = 256;
    const int grid =
        (n + block - 1) / block;

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(memory_kernel),
        dim3(grid),
        dim3(block),
        args,
        0,
        0
    );
}


static cudaError_t launch_matmul(const float *A,
                                 const float *B,
                                 float *C,
                                 int N) {

    constexpr int BLOCK = 16;

    const int grid =
        (N + BLOCK - 1) / BLOCK;

    void *args[] = {
        const_cast<float **>(&A),
        const_cast<float **>(&B),
        &C,
        &N
    };

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(matrix_mul_kernel),
        dim3(grid, grid, 1),
        dim3(BLOCK, BLOCK, 1),
        args,
        0,
        0
    );
}


/*
 * ============================================================
 *  MAIN
 * ============================================================
 */

int main() {

    /*
     * Default runtime = 120 seconds.
     *
     * You can override:
     *
     * WEDJAT_SECONDS=60 ./build/k7
     */
    const int duration_seconds =
        env_int("WEDJAT_SECONDS", 120);

    /*
     * Memory workload sizes.
     *
     * These are deliberately conservative for a
     * 4 GB RTX 3050 Ti.
     */
    const size_t SMALL_MB = 128;
    const size_t MEDIUM_MB = 256;
    const size_t LARGE_MB = 512;

    /*
     * Compute workload.
     */
    const int compute_rounds =
        env_int("WEDJAT_COMPUTE_ROUNDS", 150);

    std::printf(
        "\n"
        "====================================================\n"
        " WEDJAT K7 GPU / MEMORY STRESS FIXTURE\n"
        "====================================================\n"
        " duration          : %d seconds\n"
        " small allocation  : %zu MB\n"
        " medium allocation : %zu MB\n"
        " large allocation  : %zu MB\n"
        " compute rounds    : %d\n"
        "====================================================\n\n",
        duration_seconds,
        SMALL_MB,
        MEDIUM_MB,
        LARGE_MB,
        compute_rounds
    );

    std::fflush(stdout);

    CUDA_CHECK(cudaSetDevice(0));


    /*
     * --------------------------------------------------------
     *  Get device information
     * --------------------------------------------------------
     */

    cudaDeviceProp prop{};

    CUDA_CHECK(
        cudaGetDeviceProperties(&prop, 0)
    );

    std::printf(
        "WEDJAT_K7_DEVICE name=%s "
        "global_memory=%zuMB "
        "multiprocessors=%d\n",
        prop.name,
        prop.totalGlobalMem / (1024 * 1024),
        prop.multiProcessorCount
    );

    std::fflush(stdout);


    /*
     * --------------------------------------------------------
     *  Persistent compute buffer
     * --------------------------------------------------------
     *
     * ~256 MB.
     */
    const size_t compute_bytes =
        MEDIUM_MB * 1024ULL * 1024ULL;

    const int compute_n =
        static_cast<int>(
            compute_bytes / sizeof(float)
        );

    float *compute_buffer = nullptr;

    CUDA_CHECK(
        cudaMalloc(
            &compute_buffer,
            compute_bytes
        )
    );

    CUDA_CHECK(
        cudaMemset(
            compute_buffer,
            0x3f,
            compute_bytes
        )
    );

    std::printf(
        "WEDJAT_K7_ALLOC persistent=%zuMB\n",
        MEDIUM_MB
    );

    std::fflush(stdout);


    /*
     * --------------------------------------------------------
     *  Matrix buffers
     * --------------------------------------------------------
     *
     * N=3072:
     *
     * One matrix ~= 36 MB.
     * Three ~= 108 MB.
     *
     * Enough to make GEMM expensive without
     * consuming the entire VRAM.
     */
    constexpr int MATRIX_N = 3072;

    const size_t matrix_elements =
        static_cast<size_t>(MATRIX_N) *
        static_cast<size_t>(MATRIX_N);

    const size_t matrix_bytes =
        matrix_elements * sizeof(float);

    float *A = nullptr;
    float *B = nullptr;
    float *C = nullptr;

    CUDA_CHECK(cudaMalloc(&A, matrix_bytes));
    CUDA_CHECK(cudaMalloc(&B, matrix_bytes));
    CUDA_CHECK(cudaMalloc(&C, matrix_bytes));

    CUDA_CHECK(cudaMemset(A, 0x3f, matrix_bytes));
    CUDA_CHECK(cudaMemset(B, 0x3f, matrix_bytes));
    CUDA_CHECK(cudaMemset(C, 0x00, matrix_bytes));

    std::printf(
        "WEDJAT_K7_ALLOC matrices=%zuMB each total=%zuMB\n",
        matrix_bytes / (1024 * 1024),
        (matrix_bytes * 3) / (1024 * 1024)
    );

    std::fflush(stdout);


    /*
     * --------------------------------------------------------
     *  Temporary memory
     * --------------------------------------------------------
     *
     * This pointer will repeatedly go:
     *
     * NULL
     *   ↓
     * 128 MB
     *   ↓
     * NULL
     *   ↓
     * 256 MB
     *   ↓
     * NULL
     *   ↓
     * 512 MB
     *   ↓
     * NULL
     *
     * This gives your tracing code actual GPU allocation
     * churn.
     */
    float *temporary = nullptr;


    /*
     * --------------------------------------------------------
     *  Host buffer
     * --------------------------------------------------------
     */
    std::vector<float> host_buffer(
        8 * 1024 * 1024 / sizeof(float),
        1.0f
    );


    /*
     * --------------------------------------------------------
     *  Timing
     * --------------------------------------------------------
     */
    const auto start =
        std::chrono::steady_clock::now();

    int iteration = 0;


    /*
     * ========================================================
     *  MAIN 120 SECOND LOOP
     * ========================================================
     */
    while (true) {

        const auto now =
            std::chrono::steady_clock::now();

        const double elapsed =
            std::chrono::duration<double>(
                now - start
            ).count();

        if (elapsed >= duration_seconds)
            break;


        /*
         * ----------------------------------------------------
         *  PHASE 1
         *
         *  Heavy compute
         * ----------------------------------------------------
         */

        CUDA_CHECK(
            launch_compute(
                compute_buffer,
                compute_n,
                compute_rounds
            )
        );

        CUDA_CHECK(cudaGetLastError());


        /*
         * ----------------------------------------------------
         *  PHASE 2
         *
         *  Memory-bandwidth workload
         * ----------------------------------------------------
         */

        CUDA_CHECK(
            launch_memory(
                compute_buffer,
                compute_n,
                20
            )
        );

        CUDA_CHECK(cudaGetLastError());


        /*
         * ----------------------------------------------------
         *  PHASE 3
         *
         *  Matrix multiplication
         * ----------------------------------------------------
         */

        CUDA_CHECK(
            launch_matmul(
                A,
                B,
                C,
                MATRIX_N
            )
        );

        CUDA_CHECK(cudaGetLastError());


        /*
         * ----------------------------------------------------
         *  PHASE 4
         *
         *  Synchronize.
         * ----------------------------------------------------
         */
        CUDA_CHECK(
            cudaDeviceSynchronize()
        );


        /*
         * ----------------------------------------------------
         *  PHASE 5
         *
         *  GPU MEMORY CHURN
         *
         *  Change allocation size every iteration.
         * ----------------------------------------------------
         */

        if (temporary != nullptr) {

            CUDA_CHECK(
                cudaFree(temporary)
            );

            temporary = nullptr;

            std::printf(
                "WEDJAT_K7_MEMORY_FREE iter=%d\n",
                iteration
            );

            std::fflush(stdout);
        }


        size_t temporary_mb;

        switch (iteration % 6) {

            case 0:
                temporary_mb = SMALL_MB;
                break;

            case 1:
                temporary_mb = LARGE_MB;
                break;

            case 2:
                temporary_mb = MEDIUM_MB;
                break;

            case 3:
                temporary_mb = LARGE_MB;
                break;

            case 4:
                temporary_mb = SMALL_MB;
                break;

            default:
                temporary_mb = MEDIUM_MB;
                break;
        }


        const size_t temporary_bytes =
            temporary_mb * 1024ULL * 1024ULL;


        CUDA_CHECK(
            cudaMalloc(
                &temporary,
                temporary_bytes
            )
        );

        CUDA_CHECK(
            cudaMemset(
                temporary,
                iteration & 0xff,
                temporary_bytes
            )
        );

        std::printf(
            "WEDJAT_K7_MEMORY_ALLOC "
            "iter=%d "
            "temporary=%zuMB\n",
            iteration,
            temporary_mb
        );

        std::fflush(stdout);


        /*
         * ----------------------------------------------------
         *  PHASE 6
         *
         *  Do some compute on the newly allocated memory.
         * ----------------------------------------------------
         */

        const int temporary_n =
            static_cast<int>(
                temporary_bytes / sizeof(float)
            );

        CUDA_CHECK(
            launch_compute(
                temporary,
                temporary_n,
                50
            )
        );

        CUDA_CHECK(cudaGetLastError());

        CUDA_CHECK(
            cudaDeviceSynchronize()
        );


        /*
         * ----------------------------------------------------
         *  PHASE 7
         *
         *  Host <-> GPU transfer.
         *
         *  This makes memory activity change again.
         * ----------------------------------------------------
         */

        const size_t copy_bytes =
            host_buffer.size() * sizeof(float);

        CUDA_CHECK(
            cudaMemcpy(
                temporary,
                host_buffer.data(),
                copy_bytes,
                cudaMemcpyHostToDevice
            )
        );

        CUDA_CHECK(
            cudaMemcpy(
                host_buffer.data(),
                temporary,
                copy_bytes,
                cudaMemcpyDeviceToHost
            )
        );


        /*
         * ----------------------------------------------------
         *  Progress event
         * ----------------------------------------------------
         */

        const auto event_now =
            std::chrono::steady_clock::now();

        const double event_elapsed =
            std::chrono::duration<double>(
                event_now - start
            ).count();

        std::printf(
            "WEDJAT_K7_EVENT "
            "iter=%d "
            "elapsed=%.2fs "
            "temporary=%zuMB "
            "compute_rounds=%d\n",
            iteration,
            event_elapsed,
            temporary_mb,
            compute_rounds
        );

        std::fflush(stdout);


        /*
         * ----------------------------------------------------
         *  Short pause.
         *
         * The GPU doesn't need to be 100%% busy continuously.
         * This also creates more distinct activity windows
         * for tracing.
         * ----------------------------------------------------
         */

        std::this_thread::sleep_for(
            std::chrono::milliseconds(100)
        );

        ++iteration;
    }


    /*
     * ========================================================
     *  CLEANUP
     * ========================================================
     */

    std::printf(
        "\nWEDJAT_K7_CLEANUP_BEGIN\n"
    );

    std::fflush(stdout);


    if (temporary != nullptr) {

        CUDA_CHECK(
            cudaFree(temporary)
        );

        temporary = nullptr;
    }


    CUDA_CHECK(cudaFree(A));
    CUDA_CHECK(cudaFree(B));
    CUDA_CHECK(cudaFree(C));

    CUDA_CHECK(cudaFree(compute_buffer));


    CUDA_CHECK(
        cudaDeviceSynchronize()
    );


    std::printf(
        "WEDJAT_K7_DONE iterations=%d duration=%d seconds\n",
        iteration,
        duration_seconds
    );

    std::fflush(stdout);


    usleep(500000);

    return 0;
}