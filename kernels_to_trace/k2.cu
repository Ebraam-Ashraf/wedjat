#include <cuda.h>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <vector>

#define CU_CHECK(call)                                                          \
    do {                                                                        \
        CUresult res__ = (call);                                                \
        if (res__ != CUDA_SUCCESS) {                                            \
            const char *name__ = "unknown";                                    \
            const char *desc__ = "unknown";                                    \
            cuGetErrorName(res__, &name__);                                     \
            cuGetErrorString(res__, &desc__);                                   \
            std::fprintf(stderr, "CU_CHECK failed %s:%d: %s (%s)\n", __FILE__,  \
                         __LINE__, name__, desc__);                             \
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

int main() {
    const int n = env_int("WEDJAT_N", 1 << 20);
    const int iters = env_int("WEDJAT_ITERS", 20);
    const int sleep_ms = env_int("WEDJAT_SLEEP_MS", 100);
    const size_t bytes = static_cast<size_t>(n) * sizeof(float);

    std::printf("WEDJAT_FIXTURE name=k2_driver_alloc n=%d bytes=%zu iters=%d\n",
                n, bytes, iters);

    CUdevice device;
    CUcontext context;
    CUstream stream;

    CU_CHECK(cuInit(0));
    CU_CHECK(cuDeviceGet(&device, 0));
    CU_CHECK(cuDevicePrimaryCtxRetain(&context, device));
    CU_CHECK(cuCtxSetCurrent(context));
    CU_CHECK(cuStreamCreate(&stream, CU_STREAM_DEFAULT));

    std::vector<float> host(n, 2.0f);

    for (int i = 0; i < iters; ++i) {
        CUdeviceptr devptr = 0;

        CU_CHECK(cuMemAlloc(&devptr, bytes));
        CU_CHECK(cuMemcpyHtoDAsync(devptr, host.data(), bytes, stream));
        CU_CHECK(cuStreamSynchronize(stream));
        CU_CHECK(cuMemFree(devptr));

        std::printf("WEDJAT_FIXTURE_EVENT name=k2_driver_alloc iter=%d bytes=%zu\n",
                    i, bytes);
        std::fflush(stdout);

        if (sleep_ms > 0) {
            std::this_thread::sleep_for(std::chrono::milliseconds(sleep_ms));
        }
    }

    CU_CHECK(cuStreamDestroy(stream));
    CU_CHECK(cuDevicePrimaryCtxRelease(device));

    std::printf("WEDJAT_FIXTURE_DONE name=k2_driver_alloc\n");
    return 0;
}
