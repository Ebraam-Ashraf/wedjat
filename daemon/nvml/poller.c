#include <stddef.h>
#include <string.h>
#include <nvml.h>

struct device_snapshot {
    char   uuid[96];
    unsigned int index;

    unsigned int gpu_util;
    unsigned int mem_util;

    unsigned long long mem_used;
    unsigned long long mem_free;
    unsigned long long mem_total;

    unsigned int temp_c;
    unsigned int power_mw;

    unsigned int sm_clock_mhz;
    unsigned int mem_clock_mhz;

    int          valid;
    int          nvml_error;
};

struct process_entry {
    unsigned int           pid;
    unsigned long long     used_gpu_memory;
};

#define MAX_PROCESSES 64

struct process_snapshot {
    unsigned int              count;
    struct process_entry entries[MAX_PROCESSES];
    int                        valid;
    int                        nvml_error;
};

typedef void *nvml_device_handle_t;

int poller_init(void)
{
    if (nvmlInit() != NVML_SUCCESS)
        return -1;
    return 0;
}

void poller_shutdown(void)
{
    nvmlShutdown();
}

int poller_device_count(void)
{
    unsigned int count = 0;
    if (nvmlDeviceGetCount(&count) != NVML_SUCCESS)
        return -1;
    return (int)count;
}

void poller_snapshot_device(unsigned int index, struct device_snapshot *out)
{
    memset(out, 0, sizeof(*out));
    out->index = index;

    nvmlDevice_t device;
    if (nvmlDeviceGetHandleByIndex(index, &device) != NVML_SUCCESS) {
        out->nvml_error = 1;
        return;
    }

    nvmlDeviceGetUUID(device, out->uuid, sizeof(out->uuid));

    nvmlUtilization_t util;
    if (nvmlDeviceGetUtilizationRates(device, &util) == NVML_SUCCESS) {
        out->gpu_util = util.gpu;
        out->mem_util = util.memory;
    }

    nvmlMemory_t mem;
    if (nvmlDeviceGetMemoryInfo(device, &mem) == NVML_SUCCESS) {
        out->mem_used  = mem.used;
        out->mem_free  = mem.free;
        out->mem_total = mem.total;
    }

    unsigned int temp;
    if (nvmlDeviceGetTemperature(device, NVML_TEMPERATURE_GPU, &temp) == NVML_SUCCESS)
        out->temp_c = temp;

    unsigned int power;
    if (nvmlDeviceGetPowerUsage(device, &power) == NVML_SUCCESS)
        out->power_mw = power;

    unsigned int clock;
    if (nvmlDeviceGetClockInfo(device, NVML_CLOCK_SM, &clock) == NVML_SUCCESS)
        out->sm_clock_mhz = clock;
    if (nvmlDeviceGetClockInfo(device, NVML_CLOCK_MEM, &clock) == NVML_SUCCESS)
        out->mem_clock_mhz = clock;

    out->valid = 1;
}

void poller_snapshot_processes(unsigned int index, struct process_snapshot *out)
{
    memset(out, 0, sizeof(*out));

    nvmlDevice_t device;
    if (nvmlDeviceGetHandleByIndex(index, &device) != NVML_SUCCESS) {
        out->nvml_error = 1;
        return;
    }

    unsigned int infoCount = MAX_PROCESSES;
    nvmlProcessInfo_t infos[MAX_PROCESSES];
    if (nvmlDeviceGetComputeRunningProcesses(device, &infoCount, infos) != NVML_SUCCESS) {
        out->nvml_error = 1;
        return;
    }

    if (infoCount > MAX_PROCESSES)
        infoCount = MAX_PROCESSES;

    out->count = infoCount;
    for (unsigned int i = 0; i < infoCount; i++) {
        out->entries[i].pid            = infos[i].pid;
        out->entries[i].used_gpu_memory = infos[i].usedGpuMemory;
    }
    out->valid = 1;
}

const struct process_entry *poller_find_pid(
        const struct process_snapshot *snap,
        unsigned int pid)
{
    for (unsigned int i = 0; i < snap->count; i++) {
        if (snap->entries[i].pid == pid)
            return &snap->entries[i];
    }
    return NULL;
}
