#include<vmlinux.h>
#include<bfp/bpf_helpers.h>
#include<bpf/trace_helpers.h>
#include"../../common.h"




SEC("uprobe/cuLaunchKernel")
SEC("uprobe/cuLaunchKernel_ptsz")

SEC("uprobe/cuMemAlloc")
SEC("uretprobe/cuMemAlloc")

SEC("uprobe/cuMemcpyAsync")

SEC("uprobe/cuStreamSynchronize")
SEC("uretprobe/cuStreamSynchronize")



SEC("uprobe/cuMemAlloc_v2")
SEC("uretprobe/cuMemAlloc_v2")
SEC("uprobe/cuMemFree_v2")
SEC("uprobe/cuMemAllocManaged")


SEC("uprobe/cuMemcpyHtoD_v2")
SEC("uprobe/cuMemcpyDtoH_v2")
SEC("uprobe/cuMemcpyDtoD_v2")
SEC("uprobe/cuMemcpyHtoDAsync_v2")
SEC("uprobe/cuMemcpyDtoHAsync_v2")
SEC("uprobe/cuMemcpyDtoDAsync_v2")


SEC("uprobe/cuCtxSynchronize")
SEC("uretprobe/cuCtxSynchronize")
SEC("uprobe/cuStreamSynchronize_ptsz")
SEC("uretprobe/cuStreamSynchronize_ptsz")


SEC("uprobe/cuLaunchCooperativeKernel")
SEC("uprobe/cuGraphLaunch")


SEC("uprobe/cuModuleGetFunction")

char LICENSE[] SEC("license") = "GPL";