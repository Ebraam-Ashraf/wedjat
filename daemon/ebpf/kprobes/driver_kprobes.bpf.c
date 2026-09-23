// LAYER 3 : OS Driver Subsystems 

SEC("kprobe/nvidia_ioctl")
    /*WHY: catches EVERY syscall crossing from any host process into
    the core NVIDIA driver — the generic, catch-all "something
    talked to the GPU driver" signal, used as a high-volume
    fast-path counter, not per-event logging (matches the
    real symbol you found: nvidia_unlocked_ioctl).*/

SEC("kprobe/uvm_va_block_service_fault")
    /*WHY: fires when VRAM is oversubscribed and a page has to be
    faulted in over PCIe — direct signal of memory pressure /
    thrashing (your dump's version of this: uvm_va_block_cpu_fault /
    service_fault_batch_dispatch).*/

SEC("tp_btf/sched_switch")   [appears 3x — reused across sub-features]
    /*WHY: this is a TRACEPOINT, not a kprobe — fires whenever the
    kernel scheduler preempts/switches a thread. Used here
    specifically to detect when a GPU-launching thread gets kicked
    off-CPU mid-flight — a CPU scheduling bottleneck, not a GPU one.*/

SEC("tp_btf/softirq_entry")   [appears 2x]
    /*WHY: another tracepoint — fires on soft-interrupt handling
    (network RX, timers, etc). Used to detect when network/disk
    interrupt load is stealing CPU cycles away from GPU-launch
    threads — a "noisy neighbor" diagnostic.*/




// LAYER 5 — GPU Silicon (device-resident, PTX injection — future work)


SEC("kprobe/matmul_kernel")
    /*WHY: this one's different in KIND, not just target — it's a
    kprobe attached to a symbol name representing a KERNEL FUNCTION
    NAME (matmul_kernel is an example CUDA kernel, not a system
    function) via eGPU/bpftime PTX injection — meaning the "probe"
    here actually runs INSIDE the GPU's own execution, reading
    %smid/%ctaid registers on-chip, not on the host CPU at all. This
    is the exotic, experimental layer — treat the SEC() syntax
    similarity as coincidental; the underlying mechanism (PTX JIT
    into the GPU kernel itself) is nothing like kprobe/uprobe.*/