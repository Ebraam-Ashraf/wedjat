// https://eunomia.dev/tutorials/5-uprobe-bashreadline/

#include <vmlinux.h>
#include <bpf/bpf_helpers.h> 
#include <bpf/bpf_tracing.h>





SEC("kprobe/nvidia_ioctl")
SEC("kretprobe/nvidia_ioctl")

SEC("kprobe/uvm_va_block_service_fault")
SEC("kretprobe/uvm_va_block_service_fault")

SEC("kprobe/uvm_migrate")
SEC("kretprobe/uvm_migrate")

SEC("kprobe/uvm_va_block_evict_pages")
SEC("kretprobe/uvm_va_block_evict_pages")

SEC("kprobe/do_sys_openat2")

SEC("kprobe/queued_spin_lock_slowpath")

SEC("kprobe/nvidia_mmap")
SEC("kretprobe/nvidia_mmap")

SEC("kprobe/uvm_ioctl")
SEC("kretprobe/uvm_ioctl")

char LICENSE[] SEC("license") = "GPL";



