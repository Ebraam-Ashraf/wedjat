#include <errno.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>
#include <bpf/bpf.h>
#include <bpf/libbpf.h>
#include "common.h"
#include "proc_lifecycle.skel.h"

#define SKIP 77
#define FAIL 1

static pid_t target_pid;
static unsigned int exit_events;

static int handle_event(void *ctx, void *data, size_t size)
{
    struct event *event = data;
    (void)ctx;

    if (size >= sizeof(*event) && event->api_id == EVENT_PROC_EXIT &&
        event->tgid == (u32)target_pid)
        exit_events++;
    return 0;
}

static void *wait_for_release(void *arg)
{
    int fd = *(int *)arg;
    char byte;
    if (read(fd, &byte, 1) != 1)
        return (void *)1;
    return NULL;
}

static int poll_for(struct ring_buffer *ring, int duration_ms)
{
    struct timespec start, now;
    clock_gettime(CLOCK_MONOTONIC, &start);
    do {
        int elapsed;
        clock_gettime(CLOCK_MONOTONIC, &now);
        elapsed = (int)((now.tv_sec - start.tv_sec) * 1000 +
                        (now.tv_nsec - start.tv_nsec) / 1000000);
        if (elapsed >= duration_ms)
            break;
        int rc = ring_buffer__poll(ring, duration_ms - elapsed);
        if (rc < 0 && rc != -EINTR)
            return rc;
    } while (1);
    return 0;
}

int main(void)
{
    struct proc_lifecycle_bpf *skel;
    struct ring_buffer *ring = NULL;
    struct process_seen_val seen = {};
    int workers[2], main_gate[2], ready[2], status = 0;
    char byte;
    pthread_t threads[3];

    if (geteuid() != 0) {
        fprintf(stderr, "SKIP: run as root\n");
        return SKIP;
    }
    skel = proc_lifecycle_bpf__open_and_load();
    if (!skel) {
        fprintf(stderr, "FAIL: could not load proc_lifecycle object\n");
        return FAIL;
    }
    if (proc_lifecycle_bpf__attach(skel)) {
        fprintf(stderr, "FAIL: could not attach lifecycle tracepoints\n");
        proc_lifecycle_bpf__destroy(skel);
        return FAIL;
    }
    ring = ring_buffer__new(bpf_map__fd(skel->maps.events_pipe),
                            handle_event, NULL, NULL);
    if (!ring) {
        fprintf(stderr, "FAIL: could not create ring buffer\n");
        proc_lifecycle_bpf__destroy(skel);
        return FAIL;
    }
    if (pipe(workers) || pipe(main_gate) || pipe(ready)) {
        perror("pipe");
        ring_buffer__free(ring);
        proc_lifecycle_bpf__destroy(skel);
        return FAIL;
    }

    target_pid = fork();
    if (target_pid == 0) {
        close(workers[1]);
        close(main_gate[1]);
        close(ready[0]);
        for (int i = 0; i < 3; i++) {
            if (pthread_create(&threads[i], NULL, wait_for_release,
                               &workers[0]) != 0)
                _exit(10);
        }
        if (write(ready[1], "r", 1) != 1)
            _exit(11);
        close(ready[1]);
        for (int i = 0; i < 3; i++)
            pthread_join(threads[i], NULL);
        if (read(main_gate[0], &byte, 1) != 1)
            _exit(12);
        _exit(0);
    }
    if (target_pid < 0) {
        perror("fork");
        ring_buffer__free(ring);
        proc_lifecycle_bpf__destroy(skel);
        return FAIL;
    }

    close(workers[0]);
    close(main_gate[0]);
    close(ready[1]);
    if (read(ready[0], &byte, 1) != 1) {
        fprintf(stderr, "FAIL: child threads did not start\n");
        kill(target_pid, SIGKILL);
        waitpid(target_pid, NULL, 0);
        return FAIL;
    }
    close(ready[0]);
    u32 pid = (u32)target_pid;
    if (bpf_map_update_elem(bpf_map__fd(skel->maps.seen_processes),
                            &pid, &seen, BPF_ANY) != 0) {
        perror("bpf_map_update_elem");
        kill(target_pid, SIGKILL);
        waitpid(target_pid, NULL, 0);
        return FAIL;
    }

    if (write(workers[1], "xxx", 3) != 3 || poll_for(ring, 200) != 0) {
        fprintf(stderr, "FAIL: worker phase polling failed\n");
        kill(target_pid, SIGKILL);
        waitpid(target_pid, NULL, 0);
        return FAIL;
    }
    if (exit_events != 0) {
        fprintf(stderr, "FAIL: process exit emitted while main thread was alive (%u)\n",
                exit_events);
        write(main_gate[1], "x", 1);
        waitpid(target_pid, &status, 0);
        return FAIL;
    }

    if (write(main_gate[1], "x", 1) != 1 ||
        waitpid(target_pid, &status, 0) != target_pid ||
        poll_for(ring, 250) != 0) {
        fprintf(stderr, "FAIL: final process exit polling failed\n");
        return FAIL;
    }
    close(workers[1]);
    close(main_gate[1]);
    ring_buffer__free(ring);
    proc_lifecycle_bpf__destroy(skel);

    if (!WIFEXITED(status) || WEXITSTATUS(status) != 0 || exit_events != 1) {
        fprintf(stderr, "FAIL: expected one final exit event, got %u\n", exit_events);
        return FAIL;
    }
    puts("PASS: three worker exits emitted no process exit; final thread emitted exactly one");
    return 0;
}
