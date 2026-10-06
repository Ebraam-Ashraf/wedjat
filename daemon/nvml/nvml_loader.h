#ifndef WEDJAT_NVML_LOADER_H
#define WEDJAT_NVML_LOADER_H

#include <dlfcn.h>
#include <pthread.h>
#include <stdlib.h>

#define NVML_SYMBOL_INNER(name) #name
#define NVML_SYMBOL(name) NVML_SYMBOL_INNER(name)
#define NVML_DECLARE(name)                                                             \
    static __typeof__(&name) wedjat_##name __attribute__((unused))

static void *wedjat_nvml_library;
static pthread_once_t wedjat_nvml_once = PTHREAD_ONCE_INIT;

static void wedjat_nvml_open(void) {
    const char *override = getenv("WEDJAT_NVML_LIBRARY");
    if (override && override[0]) {
        wedjat_nvml_library = dlopen(override, RTLD_NOW | RTLD_LOCAL);
        return;
    }

    wedjat_nvml_library = dlopen("libnvidia-ml.so.1", RTLD_NOW | RTLD_LOCAL);
    if (!wedjat_nvml_library)
        wedjat_nvml_library = dlopen("libnvidia-ml.so", RTLD_NOW | RTLD_LOCAL);
}

static int wedjat_nvml_resolve(void **target, const char *symbol) {
    pthread_once(&wedjat_nvml_once, wedjat_nvml_open);
    if (!wedjat_nvml_library)
        return 0;
    if (!*target)
        *target = dlsym(wedjat_nvml_library, symbol);
    return *target != NULL;
}

#define NVML_CALL(name, ...)                                                           \
    (wedjat_nvml_resolve((void **)&wedjat_##name, NVML_SYMBOL(name))                   \
         ? wedjat_##name(__VA_ARGS__)                                                  \
         : NVML_ERROR_UNKNOWN)
NVML_DECLARE(nvmlInit);
NVML_DECLARE(nvmlShutdown);
NVML_DECLARE(nvmlDeviceGetCount);
NVML_DECLARE(nvmlDeviceGetHandleByIndex);
NVML_DECLARE(nvmlDeviceGetHandleByUUID);
NVML_DECLARE(nvmlDeviceGetUUID);
NVML_DECLARE(nvmlDeviceGetName);
NVML_DECLARE(nvmlDeviceGetPciInfo);
NVML_DECLARE(nvmlSystemGetDriverVersion);
NVML_DECLARE(nvmlDeviceGetIndex);
NVML_DECLARE(nvmlDeviceGetUtilizationRates);
NVML_DECLARE(nvmlDeviceGetMemoryInfo);
NVML_DECLARE(nvmlDeviceGetTemperature);
NVML_DECLARE(nvmlDeviceGetPowerUsage);
NVML_DECLARE(nvmlDeviceGetClockInfo);
#if NVML_API_VERSION >= 13
NVML_DECLARE(nvmlDeviceGetCurrentClocksEventReasons);
#else
NVML_DECLARE(nvmlDeviceGetCurrentClocksThrottleReasons);
#endif
NVML_DECLARE(nvmlDeviceGetPowerManagementLimit);
NVML_DECLARE(nvmlDeviceGetTotalEccErrors);
NVML_DECLARE(nvmlEventSetCreate);
NVML_DECLARE(nvmlEventSetFree);
NVML_DECLARE(nvmlDeviceRegisterEvents);
NVML_DECLARE(nvmlEventSetWait);

#endif
