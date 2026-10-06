
#include <cuda_runtime.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
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
 *
 *  Compute intensity can be changed dynamically.
 * ============================================================
 */

__global__ void compute_kernel(
    float *data,
    int n,
    int rounds)
{
    int idx =
        blockIdx.x * blockDim.x +
        threadIdx.x;

    if (idx >= n)
        return;

    float x =
        data[idx];

    for (int i = 0; i < rounds; ++i) {

        x = x * 1.000001f + 0.000001f;

        x = sinf(x);
        x = cosf(x);

        x = sqrtf(fabsf(x) + 1.0f);

        x = x * x;

        x += 0.1234567f;

        x = logf(x + 1.000001f);

        x = expf(x * 0.001f);

        x = x * 1.00001f + 0.000001f;
    }

    data[idx] = x;
}


/*
 * ============================================================
 *  KERNEL 2
 *
 *  Memory bandwidth workload.
 * ============================================================
 */

__global__ void memory_kernel(
    float *data,
    int n)
{
    int idx =
        blockIdx.x * blockDim.x +
        threadIdx.x;

    if (idx >= n)
        return;

    int next =
        (idx + 1) % n;

    int next2 =
        (idx + 32) % n;

    float x =
        data[idx];

    x += data[next] * 0.5f;

    x += data[next2] * 0.25f;

    x *= 1.00001f;

    data[idx] = x;
}


/*
 * ============================================================
 *  KERNEL 3
 *
 *  Matrix multiplication.
 * ============================================================
 */

__global__ void matmul_kernel(
    const float *A,
    const float *B,
    float *C,
    int N)
{
    constexpr int TILE = 16;

    __shared__ float As[TILE][TILE];
    __shared__ float Bs[TILE][TILE];

    const int row =
        blockIdx.y * TILE +
        threadIdx.y;

    const int col =
        blockIdx.x * TILE +
        threadIdx.x;

    float sum = 0.0f;

    const int tiles =
        (N + TILE - 1) / TILE;

    for (int tile = 0; tile < tiles; ++tile) {

        int a_col =
            tile * TILE +
            threadIdx.x;

        int b_row =
            tile * TILE +
            threadIdx.y;

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
 *  Explicit cudaLaunchKernel().
 *
 *  Keeps this fixture on the same bpftime interception
 *  path as your previous k5/k7 fixtures.
 * ============================================================
 */

extern "C" cudaError_t cudaLaunchKernel(
    const void *func,
    dim3 gridDim,
    dim3 blockDim,
    void **args,
    size_t sharedMem,
    cudaStream_t stream);


/*
 * ============================================================
 *  Launch helpers
 * ============================================================
 */

static cudaError_t launch_compute(
    float *data,
    int n,
    int rounds)
{
    int block = 256;

    int grid =
        (n + block - 1) / block;

    void *args[] = {
        &data,
        &n,
        &rounds
    };

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(compute_kernel),
        dim3(grid),
        dim3(block),
        args,
        0,
        0);
}


static cudaError_t launch_memory(
    float *data,
    int n)
{
    int block = 256;

    int grid =
        (n + block - 1) / block;

    void *args[] = {
        &data,
        &n
    };

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(memory_kernel),
        dim3(grid),
        dim3(block),
        args,
        0,
        0);
}


static cudaError_t launch_matmul(
    const float *A,
    const float *B,
    float *C,
    int N)
{
    constexpr int BLOCK = 16;

    int grid =
        (N + BLOCK - 1) / BLOCK;

    void *args[] = {
        const_cast<float **>(&A),
        const_cast<float **>(&B),
        &C,
        &N
    };

    return cudaLaunchKernel(
        reinterpret_cast<const void *>(matmul_kernel),
        dim3(grid, grid, 1),
        dim3(BLOCK, BLOCK, 1),
        args,
        0,
        0);
}


/*
 * ============================================================
 *  MAIN
 * ============================================================
 */

int main()
{
    const int duration =
        env_int("WEDJAT_SECONDS", 120);

    /*
     * Base working buffer.
     *
     * ~256 MB.
     */
    const size_t BASE_MB = 256;

    const size_t base_bytes =
        BASE_MB * 1024ULL * 1024ULL;

    const int base_n =
        static_cast<int>(
            base_bytes / sizeof(float));


    /*
     * Matrix sizes.
     *
     * We intentionally change between these.
     */
    constexpr int SMALL_MATRIX = 1024;
    constexpr int MEDIUM_MATRIX = 2048;
    constexpr int LARGE_MATRIX = 3072;


    std::printf(
        "\n"
        "====================================================\n"
        " WEDJAT K8 GPU METRICS FIXTURE\n"
        "====================================================\n"
        " runtime       : %d seconds\n"
        " base memory   : %zu MB\n"
        " matrices      : %d / %d / %d\n"
        "====================================================\n\n",
        duration,
        BASE_MB,
        SMALL_MATRIX,
        MEDIUM_MATRIX,
        LARGE_MATRIX);

    std::fflush(stdout);


    CUDA_CHECK(cudaSetDevice(0));


    /*
     * --------------------------------------------------------
     * Device information
     * --------------------------------------------------------
     */

    cudaDeviceProp prop{};

    CUDA_CHECK(
        cudaGetDeviceProperties(
            &prop,
            0));

    std::printf(
        "WEDJAT_K8_DEVICE "
        "name=%s "
        "memory=%zuMB "
        "sms=%d\n",
        prop.name,
        prop.totalGlobalMem / (1024 * 1024),
        prop.multiProcessorCount);

    std::fflush(stdout);


    /*
     * --------------------------------------------------------
     * Persistent compute buffer
     * --------------------------------------------------------
     */

    float *base_buffer = nullptr;

    CUDA_CHECK(
        cudaMalloc(
            &base_buffer,
            base_bytes));

    CUDA_CHECK(
        cudaMemset(
            base_buffer,
            0x3f,
            base_bytes));


    /*
     * --------------------------------------------------------
     * Matrix buffers
     * --------------------------------------------------------
     *
     * Allocate for the largest matrix once.
     *
     * Then change the amount of work by changing N.
     */
    const size_t max_elements =
        static_cast<size_t>(LARGE_MATRIX) *
        LARGE_MATRIX;

    const size_t max_matrix_bytes =
        max_elements *
        sizeof(float);

    float *A = nullptr;
    float *B = nullptr;
    float *C = nullptr;

    CUDA_CHECK(
        cudaMalloc(
            &A,
            max_matrix_bytes));

    CUDA_CHECK(
        cudaMalloc(
            &B,
            max_matrix_bytes));

    CUDA_CHECK(
        cudaMalloc(
            &C,
            max_matrix_bytes));

    CUDA_CHECK(
        cudaMemset(
            A,
            0x3f,
            max_matrix_bytes));

    CUDA_CHECK(
        cudaMemset(
            B,
            0x3f,
            max_matrix_bytes));

    CUDA_CHECK(
        cudaMemset(
            C,
            0,
            max_matrix_bytes));


    /*
     * --------------------------------------------------------
     * Dynamic temporary allocation.
     *
     * This one is repeatedly allocated/freed.
     * --------------------------------------------------------
     */

    float *temporary = nullptr;


    /*
     * Host buffer for transfer workload.
     */

    const size_t HOST_MB = 32;

    std::vector<float> host(
        HOST_MB * 1024ULL * 1024ULL /
        sizeof(float),
        1.0f);


    /*
     * --------------------------------------------------------
     * Start timer
     * --------------------------------------------------------
     */

    const auto start =
        std::chrono::steady_clock::now();


    int iteration = 0;


    /*
     * ========================================================
     *
     *                  120 SECOND LOOP
     *
     * ========================================================
     */

    while (true)
    {
        const auto now =
            std::chrono::steady_clock::now();

        const double elapsed =
            std::chrono::duration<double>(
                now - start).count();

        if (elapsed >= duration)
            break;


        /*
         * ----------------------------------------------------
         * Create a repeating workload pattern.
         *
         * 0 = IDLE
         * 1 = LIGHT
         * 2 = MEDIUM
         * 3 = HEAVY
         * 4 = MEMORY
         * 5 = BURST
         * ----------------------------------------------------
         */

        const int phase =
            iteration % 6;


        /*
         * ====================================================
         * PHASE 0
         *
         * GPU mostly idle.
         * ====================================================
         */

        if (phase == 0)
        {
            std::printf(
                "WEDJAT_K8_PHASE "
                "iter=%d "
                "phase=IDLE "
                "elapsed=%.1fs\n",
                iteration,
                elapsed);

            std::fflush(stdout);

            /*
             * Important:
             *
             * This creates a visible GPU-utilization valley.
             */
            std::this_thread::sleep_for(
                std::chrono::milliseconds(1200));
        }


        /*
         * ====================================================
         * PHASE 1
         *
         * Light GPU workload.
         * ====================================================
         */

        else if (phase == 1)
        {
            std::printf(
                "WEDJAT_K8_PHASE "
                "iter=%d "
                "phase=LIGHT "
                "elapsed=%.1fs\n",
                iteration,
                elapsed);

            std::fflush(stdout);


            CUDA_CHECK(
                launch_compute(
                    base_buffer,
                    base_n,
                    10));

            CUDA_CHECK(
                cudaDeviceSynchronize());


            std::this_thread::sleep_for(
                std::chrono::milliseconds(500));
        }


        /*
         * ====================================================
         * PHASE 2
         *
         * Medium compute.
         * ====================================================
         */

        else if (phase == 2)
        {
            std::printf(
                "WEDJAT_K8_PHASE "
                "iter=%d "
                "phase=MEDIUM "
                "elapsed=%.1fs\n",
                iteration,
                elapsed);

            std::fflush(stdout);


            CUDA_CHECK(
                launch_compute(
                    base_buffer,
                    base_n,
                    75));

            CUDA_CHECK(
                cudaDeviceSynchronize());
        }


        /*
         * ====================================================
         * PHASE 3
         *
         * HEAVY GPU utilization.
         *
         * Several consecutive launches.
         * ====================================================
         */

        else if (phase == 3)
        {
            std::printf(
                "WEDJAT_K8_PHASE "
                "iter=%d "
                "phase=HEAVY "
                "elapsed=%.1fs\n",
                iteration,
                elapsed);

            std::fflush(stdout);


            for (int i = 0; i < 5; ++i)
            {
                CUDA_CHECK(
                    launch_compute(
                        base_buffer,
                        base_n,
                        250));

                CUDA_CHECK(cudaGetLastError());
            }

            CUDA_CHECK(
                cudaDeviceSynchronize());
        }


        /*
         * ====================================================
         * PHASE 4
         *
         * Memory-bandwidth workload.
         * ====================================================
         */

        else if (phase == 4)
        {
            std::printf(
                "WEDJAT_K8_PHASE "
                "iter=%d "
                "phase=MEMORY "
                "elapsed=%.1fs\n",
                iteration,
                elapsed);

            std::fflush(stdout);


            for (int i = 0; i < 15; ++i)
            {
                CUDA_CHECK(
                    launch_memory(
                        base_buffer,
                        base_n));

                CUDA_CHECK(cudaGetLastError());
            }

            CUDA_CHECK(
                cudaDeviceSynchronize());


            /*
             * Host → Device
             */

            const size_t copy_bytes =
                host.size() *
                sizeof(float);

            CUDA_CHECK(
                cudaMemcpy(
                    base_buffer,
                    host.data(),
                    copy_bytes,
                    cudaMemcpyHostToDevice));


            /*
             * Device → Host
             */

            CUDA_CHECK(
                cudaMemcpy(
                    host.data(),
                    base_buffer,
                    copy_bytes,
                    cudaMemcpyDeviceToHost));
        }


        /*
         * ====================================================
         * PHASE 5
         *
         * BURST:
         *
         * - Allocate memory
         * - Matrix multiplication
         * - Compute
         * - Free memory
         *
         * This should produce a very visible metrics spike.
         * ====================================================
         */

        else
        {
            std::printf(
                "WEDJAT_K8_PHASE "
                "iter=%d "
                "phase=BURST "
                "elapsed=%.1fs\n",
                iteration,
                elapsed);

            std::fflush(stdout);


            /*
             * Change allocation size every burst.
             */

            const size_t burst_mb =
                (iteration % 2 == 0)
                    ? 128
                    : 512;

            const size_t burst_bytes =
                burst_mb *
                1024ULL *
                1024ULL;


            CUDA_CHECK(
                cudaMalloc(
                    &temporary,
                    burst_bytes));


            CUDA_CHECK(
                cudaMemset(
                    temporary,
                    iteration & 0xff,
                    burst_bytes));


            std::printf(
                "WEDJAT_K8_MEMORY "
                "iter=%d "
                "allocated=%zuMB\n",
                iteration,
                burst_mb);

            std::fflush(stdout);


            /*
             * Heavy compute on temporary memory.
             */

            const int temporary_n =
                static_cast<int>(
                    burst_bytes /
                    sizeof(float));


            CUDA_CHECK(
                launch_compute(
                    temporary,
                    temporary_n,
                    150));


            /*
             * Large matrix multiplication.
             */

            CUDA_CHECK(
                launch_matmul(
                    A,
                    B,
                    C,
                    LARGE_MATRIX));


            CUDA_CHECK(cudaGetLastError());


            CUDA_CHECK(
                cudaDeviceSynchronize());


            /*
             * Free temporary allocation.
             *
             * GPU memory should drop here.
             */

            CUDA_CHECK(
                cudaFree(
                    temporary));

            temporary = nullptr;


            std::printf(
                "WEDJAT_K8_MEMORY "
                "iter=%d "
                "freed=%zuMB\n",
                iteration,
                burst_mb);

            std::fflush(stdout);
        }


        /*
         * ----------------------------------------------------
         * Query CUDA memory state.
         *
         * This is NOT the same as GPU utilization.
         *
         * Your external metrics collector should separately
         * observe GPU utilization.
         * ----------------------------------------------------
         */

        size_t free_mem = 0;
        size_t total_mem = 0;

        CUDA_CHECK(
            cudaMemGetInfo(
                &free_mem,
                &total_mem));


        const size_t used_mem =
            total_mem - free_mem;


        /*
         * ----------------------------------------------------
         * Unified event.
         * ----------------------------------------------------
         */

        std::printf(
            "WEDJAT_K8_EVENT "
            "iter=%d "
            "phase=%d "
            "elapsed=%.1fs "
            "gpu_mem_used=%zuMB "
            "gpu_mem_free=%zuMB "
            "gpu_mem_total=%zuMB\n",
            iteration,
            phase,
            elapsed,
            used_mem / (1024 * 1024),
            free_mem / (1024 * 1024),
            total_mem / (1024 * 1024));

        std::fflush(stdout);


        ++iteration;


        /*
         * Small gap between workload transitions.
         *
         * This makes the metric graph much easier to read.
         */

        std::this_thread::sleep_for(
            std::chrono::milliseconds(200));
    }


    /*
     * ========================================================
     * CLEANUP
     * ========================================================
     */

    std::printf(
        "\nWEDJAT_K8_CLEANUP_BEGIN\n");

    std::fflush(stdout);


    if (temporary)
    {
        CUDA_CHECK(
            cudaFree(
                temporary));

        temporary = nullptr;
    }


    CUDA_CHECK(cudaFree(A));
    CUDA_CHECK(cudaFree(B));
    CUDA_CHECK(cudaFree(C));
    CUDA_CHECK(cudaFree(base_buffer));


    CUDA_CHECK(
        cudaDeviceSynchronize());


    std::printf(
        "WEDJAT_K8_DONE "
        "duration=%d "
        "iterations=%d\n",
        duration,
        iteration);

    std::fflush(stdout);


    /*
     * Keep the process alive briefly so the final metrics
     * transition can be observed.
     */

    usleep(1000000);

    return 0;
}