
// LAYER 2 : CUDA Driver API Interception

    /*WHY: the actual kernel-dispatch event — grid/block dims, shared
    mem, stream handle. This is your core "a GPU kernel just ran"
    signal, launch frequency, workload shape.*/

SEC("uprobe/cuMemAlloc")  +  SEC("uretprobe/cuMemAlloc")
    /*WHY (entry): captures REQUESTED allocation size before the call
    runs.
    WHY (return): captures the returned devPtr + CUDA status code +
    computes allocation latency — only knowable after return. Pair
    = VRAM allocation velocity + leak tracking + OOM detection.*/

SEC("uprobe/cuMemcpyAsync")
    /*WHY: captures byte count + transfer direction (H2D/D2H/D2D) —
    this is your PCIe bandwidth signal.*/

SEC("uprobe/cuStreamSynchronize")  +  SEC("uretprobe/cuStreamSynchronize")
    /*WHY (entry): timestamp when the CPU thread starts waiting.
    WHY (return): timestamp when the GPU work finishes — delta =
    exact CPU-stall duration. This is your #1 "is the CPU the
    bottleneck, waiting on GPU" signal.*/
