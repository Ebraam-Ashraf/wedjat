#include <cuda_runtime.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <vector>

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

__global__ void scale_add_kernel(float *data, float scale, float bias, int n) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx < n) {
        data[idx] = data[idx] * scale + bias;
    }
}

int main() {
    const int n = env_int("WEDJAT_N", 1 << 20);
    const int iters = env_int("WEDJAT_ITERS", 20);
    const int sleep_ms = env_int("WEDJAT_SLEEP_MS", 100);
    const size_t bytes = static_cast<size_t>(n) * sizeof(float);

    std::printf("WEDJAT_FIXTURE name=k1_basic_launch n=%d bytes=%zu iters=%d\n",
                n, bytes, iters);

    std::vector<float> host(n, 1.0f);
    float *device = nullptr;

    CUDA_CHECK(cudaSetDevice(0));
    CUDA_CHECK(cudaMalloc(&device, bytes));
    CUDA_CHECK(cudaMemcpy(device, host.data(), bytes, cudaMemcpyHostToDevice));

    const int block = 256;
    const int grid = (n + block - 1) / block;

    for (int i = 0; i < iters; ++i) {
        scale_add_kernel<<<grid, block>>>(device, 1.0001f, 0.5f, n);
        CUDA_CHECK(cudaGetLastError());
        CUDA_CHECK(cudaDeviceSynchronize());

        std::printf("WEDJAT_FIXTURE_EVENT name=k1_basic_launch iter=%d grid=%d block=%d\n",
                    i, grid, block);
        std::fflush(stdout);

        if (sleep_ms > 0) {
            std::this_thread::sleep_for(std::chrono::milliseconds(sleep_ms));
        }
    }

    CUDA_CHECK(cudaMemcpy(host.data(), device, bytes, cudaMemcpyDeviceToHost));
    CUDA_CHECK(cudaFree(device));

    std::printf("WEDJAT_FIXTURE_DONE name=k1_basic_launch sample=%f\n", host[0]);
    return 0;
}
