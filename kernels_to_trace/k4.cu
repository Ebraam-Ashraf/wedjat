/* Same kernel and same launch geometry as k1, but launched the ordinary way:
 * the <<<>>> spelling. Its only purpose is to answer one question about the
 * test suite itself.
 *
 * k1 has to call the exported cudaLaunchKernel by hand, because in
 * cuda_runtime.h the <<<>>> spelling is a `static __inline__` template: nvcc
 * inlines it and the real dynamic call becomes __cudaLaunchKernel, which
 * bpftime does not intercept. That makes k1 a hand-written fixture, which
 * leaves an open question — does an *unmodified* CUDA program get traced at
 * all?
 *
 * This fixture is that unmodified program. Its kernel has the same signature
 * as k1's, so the mangled name is the same _Z16scale_add_kernelPfffi and the
 * probe in gpu_sm.bpf.c matches it. If bpftime traced <<<>>>, this reports
 * events; if it does not, this reports none, and the test prints which.
 *
 * Run `make test` and read the k4 row. Expect `n/a` on a stock build.
 */
#include <cuda_runtime.h>

#include <cstdio>
#include <cstdlib>
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
    const size_t bytes = static_cast<size_t>(n) * sizeof(float);

    std::printf("WEDJAT_FIXTURE name=k4_bracket_launch n=%d bytes=%zu iters=%d\n",
                n, bytes, iters);
    std::fflush(stdout);

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

        std::printf("WEDJAT_FIXTURE_EVENT name=k4_bracket_launch iter=%d grid=%d block=%d\n",
                    i, grid, block);
        std::fflush(stdout);
    }

    CUDA_CHECK(cudaMemcpy(host.data(), device, bytes, cudaMemcpyDeviceToHost));
    CUDA_CHECK(cudaFree(device));

    std::printf("WEDJAT_FIXTURE_DONE name=k4_bracket_launch sample=%f\n", host[0]);
    return 0;
}
