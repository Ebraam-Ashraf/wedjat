#include <cuda_runtime.h>

#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <iostream>
#include <vector>

#define CUDA_CHECK(call)                                                   \
    do {                                                                   \
        cudaError_t err = (call);                                          \
        if (err != cudaSuccess) {                                          \
            fprintf(stderr, "CUDA error at %s:%d: %s\n",                   \
                    __FILE__, __LINE__, cudaGetErrorString(err));          \
            std::exit(EXIT_FAILURE);                                       \
        }                                                                  \
    } while (0)

constexpr int THREADS = 256;
constexpr int STREAMS = 4;

// 64M floats = 256 MB per array.
// Three arrays => ~768 MB VRAM.
constexpr size_t N = 64ULL * 1024ULL * 1024ULL;

// Heavy arithmetic per element.
constexpr int INNER_ITERS = 400;

// Run for at least 2 minutes.
constexpr int RUNTIME_SECONDS = 120;


__global__ void heavy_kernel(
    const float* __restrict__ a,
    const float* __restrict__ b,
    float* __restrict__ c,
    size_t n)
{
    size_t idx = blockIdx.x * blockDim.x + threadIdx.x;

    if (idx >= n)
        return;

    float x = a[idx];
    float y = b[idx];

    #pragma unroll 4
    for (int i = 0; i < INNER_ITERS; ++i) {
        x = fmaf(x, 1.000001f, y);
        y = fmaf(y, 0.999999f, x);

        x = __sinf(x);
        y = __cosf(y);

        x = fmaf(x, y, 0.000001f);
        y = fmaf(y, x, 0.000002f);

        x = sqrtf(fabsf(x) + 1.0e-6f);
        y = sqrtf(fabsf(y) + 1.0e-6f);
    }

    c[idx] = x + y;
}


__global__ void initialize_kernel(
    float* a,
    float* b,
    size_t n)
{
    size_t idx = blockIdx.x * blockDim.x + threadIdx.x;

    if (idx >= n)
        return;

    float v = static_cast<float>(idx % 1000) * 0.001f;

    a[idx] = v + 1.0f;
    b[idx] = v + 2.0f;
}


int main()
{
    int device = 0;

    cudaDeviceProp prop{};
    CUDA_CHECK(cudaGetDeviceProperties(&prop, device));
    CUDA_CHECK(cudaSetDevice(device));

    std::cout << "GPU: " << prop.name << "\n";
    std::cout << "VRAM: "
              << (static_cast<double>(prop.totalGlobalMem) / (1024.0 * 1024.0 * 1024.0))
              << " GiB\n";

    std::cout << "N: " << N << " elements\n";
    std::cout << "Streams: " << STREAMS << "\n";
    std::cout << "Inner iterations: " << INNER_ITERS << "\n";
    std::cout << "Runtime: " << RUNTIME_SECONDS << " seconds\n";

    float* d_a = nullptr;
    float* d_b = nullptr;
    float* d_c = nullptr;

    CUDA_CHECK(cudaMalloc(&d_a, N * sizeof(float)));
    CUDA_CHECK(cudaMalloc(&d_b, N * sizeof(float)));
    CUDA_CHECK(cudaMalloc(&d_c, N * sizeof(float)));

    std::cout << "Allocated ~"
              << (3.0 * N * sizeof(float) / (1024.0 * 1024.0))
              << " MiB of GPU memory\n";

    int blocks = static_cast<int>(
        (N + THREADS - 1) / THREADS
    );

    std::cout << "Blocks: " << blocks << "\n";

    // Initialize data.
    initialize_kernel<<<blocks, THREADS>>>(d_a, d_b, N);
    CUDA_CHECK(cudaGetLastError());
    CUDA_CHECK(cudaDeviceSynchronize());

    cudaStream_t streams[STREAMS];

    for (int i = 0; i < STREAMS; ++i) {
        CUDA_CHECK(cudaStreamCreateWithFlags(
            &streams[i],
            cudaStreamNonBlocking
        ));
    }

    std::cout << "\nStarting heavy CUDA workload...\n";
    std::cout << "Press Ctrl+C to stop.\n\n";

    auto start = std::chrono::steady_clock::now();

    uint64_t launches = 0;

    while (true) {
        auto now = std::chrono::steady_clock::now();

        double elapsed =
            std::chrono::duration<double>(now - start).count();

        if (elapsed >= RUNTIME_SECONDS)
            break;

        /*
         * Launch several independent kernels.
         *
         * This gives CUDA/eBPF tracing plenty of activity:
         *
         *   kernel launch
         *   execution
         *   synchronization
         *   stream activity
         *
         * while keeping the GPU continuously busy.
         */
        for (int s = 0; s < STREAMS; ++s) {
            heavy_kernel<<<
                blocks,
                THREADS,
                0,
                streams[s]
            >>>(
                d_a,
                d_b,
                d_c,
                N
            );

            CUDA_CHECK(cudaGetLastError());

            launches++;
        }

        // Periodically synchronize so errors are surfaced.
        if ((launches % 32) == 0) {
            CUDA_CHECK(cudaDeviceSynchronize());

            now = std::chrono::steady_clock::now();

            elapsed =
                std::chrono::duration<double>(now - start).count();

            std::printf(
                "\rTime: %6.1f / %d sec | launches: %llu",
                elapsed,
                RUNTIME_SECONDS,
                static_cast<unsigned long long>(launches)
            );

            std::fflush(stdout);
        }
    }

    CUDA_CHECK(cudaDeviceSynchronize());

    std::cout << "\n\nWorkload finished.\n";
    std::cout << "Total kernel launches: " << launches << "\n";

    for (int i = 0; i < STREAMS; ++i) {
        CUDA_CHECK(cudaStreamDestroy(streams[i]));
    }

    CUDA_CHECK(cudaFree(d_a));
    CUDA_CHECK(cudaFree(d_b));
    CUDA_CHECK(cudaFree(d_c));

    CUDA_CHECK(cudaDeviceReset());

    std::cout << "Done.\n";

    return 0;
}