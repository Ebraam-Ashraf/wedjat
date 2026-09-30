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

__global__ void scale_add_kernel(float *data, float scale, float bias, int n) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx < n) {
        data[idx] = data[idx] * scale + bias;
    }
}

/* `scale_add_kernel<<<grid, block>>>(...)` does NOT call cudaLaunchKernel.
 * In cuda_runtime.h that spelling is a `static __inline__` template, so nvcc
 * inlines it into this translation unit and the real dynamic call becomes
 * __cudaPushCallConfiguration + __cudaLaunchKernel. `nm -D` confirms it:
 *
 *   U __cudaLaunchKernel@libcudart.so.13
 *
 * bpftime intercepts kernel launches by replacing the *exported* symbols
 * cudaLaunchKernel / cudaLaunchKernel_ptsz / cuLaunchKernel
 * (nv_attach_impl.cpp: replace_hook_once). It never replaces
 * __cudaLaunchKernel, so with the <<<>>> spelling the patched module is built,
 * loaded, and then never launched: no probe runs and no event is ever
 * produced, while every log line still looks healthy.
 *
 * Both symbols do exist in libcudart (cudaLaunchKernel@@libcudart.so.13 and
 * __cudaLaunchKernel@@libcudart.so.13), so declaring the exported one by hand
 * and calling it through a function pointer binds to the symbol bpftime hooks
 * while going through the identical C runtime ABI underneath.
 *
 * Both call shapes do go through __cudaPushCallConfiguration /
 * __cudaPopCallConfiguration, so registering the function with the runtime
 * first keeps the out-of-line entry point's bookkeeping correct. */
extern "C" cudaError_t cudaLaunchKernel(const void *func, dim3 gridDim,
                                        dim3 blockDim, void **args,
                                        size_t sharedMem, cudaStream_t stream);

static cudaError_t launch_scale_add(float *data, float scale, float bias, int n,
                                    int grid, int block) {
    void *args[] = { &data, &scale, &bias, &n };
    return cudaLaunchKernel(reinterpret_cast<const void *>(scale_add_kernel),
                            dim3(grid), dim3(block), args, 0, 0);
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
        launch_scale_add(device, 1.0001f, 0.5f, n, grid, block);
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
    if (sleep_ms > 0) {
        usleep(sleep_ms * 1000);
    }
    return 0;
}
