#include <vmlinux.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "common.h"

SEC("tracepoint/sched/sched_process_exec")
int trace_sched_process_exec(struct trace_event_raw_sched_process_exec *ctx)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = (u32)(pid_tgid >> 32);
    struct process_seen_val *seen = bpf_map_lookup_elem(&seen_processes, &tgid);
    struct task_struct *task;
    struct event event = {};

    if (!seen)
        return 0;
    task = (struct task_struct *)bpf_get_current_task_btf();
    init_event(&event, EVENT_PROC_EXEC);
    event.start_boottime_ns = BPF_CORE_READ(task, start_boottime);
    if (!event.start_boottime_ns)
        event.start_boottime_ns = seen->start_boottime_ns;
    event.device_ordinal = WEDJAT_UNKNOWN_DEVICE;
    submit_event(&event);
    return 0;
}

SEC("tracepoint/sched/sched_process_exit")
int trace_sched_process_exit(struct trace_event_raw_sched_process_template *ctx)
{
    u64 pid_tgid = bpf_get_current_pid_tgid();
    u32 tgid = (u32)(pid_tgid >> 32);
    struct thread_key thread = { .pid_tgid = pid_tgid };
    struct process_seen_val *seen = bpf_map_lookup_elem(&seen_processes, &tgid);
    struct task_struct *task;
    struct signal_struct *signal;

    bpf_map_delete_elem(&tid_to_device, &thread);
    if (!seen)
        return 0;

    task = (struct task_struct *)bpf_get_current_task_btf();
    signal = BPF_CORE_READ(task, signal);
    /* do_exit decrements live before sched_process_exit; zero means last thread. */
    if (!signal || BPF_CORE_READ(signal, live.counter) != 0)
        return 0;

    struct event event = {};
    init_event(&event, EVENT_PROC_EXIT);
    event.start_boottime_ns = BPF_CORE_READ(task, start_boottime);
    if (!event.start_boottime_ns)
        event.start_boottime_ns = seen->start_boottime_ns;
    event.status = BPF_CORE_READ(task, exit_code);
    event.device_ordinal = WEDJAT_UNKNOWN_DEVICE;
    submit_event(&event);

    bpf_map_delete_elem(&seen_processes, &tgid);
    bpf_map_delete_elem(&pid_to_device, &tgid);
    return 0;
}

char LICENSE[] SEC("license") = "GPL";
