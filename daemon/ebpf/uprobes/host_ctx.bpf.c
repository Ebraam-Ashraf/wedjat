#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>


SEC("uprobe/")
int get_host_pid(){


    
}


// LAYER 1 Host Process Context
    */
SEC("uprobe/cuCtxSetCurrent")
    /*WHY: fires when a thread binds itself to a CUDA context. This is
    how you map TID -> physical GPU device_id on multi-GPU hosts —
    without it, every later event is "unknown which GPU."*/

SEC("uprobe/cuDevicePrimaryCtxRetain")
    /*WHY: alternate/more direct path to the same TID->device mapping
    (some apps use this instead of cuCtxSetCurrent).*/

SEC("uprobe/cuLaunchKernel")   [Layer 1's copy — just captures identity]
    /*WHY: reuses the launch event as a trigger point to stamp
    pid/tid/comm/cgroup/cpu/timestamp — the "identity envelope"
    every other layer's event gets wrapped in.*/


