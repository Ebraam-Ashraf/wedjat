/*1. open the .bpf.o          (parse maps + programs)
2. set map sizes/config      (optional, before load)
3. load                      (kernel creates maps, verifier checks programs)
4. attach probes             (uprobe to libcuda.so path + symbol, kprobes, tracepoints)
5. loop: read maps / poll ringbuf every ~1s  →  write to DB
6. on exit: detach + close   (kernel frees everything, unless pinned)



Open and load the 3 .bpf.o objects, and the kernel creates the maps and verifies the programs.
Connect the shared maps (reuse_fd), and turn off maps an object doesn't use (set_autocreate).
Find libcuda.so and the kernel symbols, and skip any hook whose symbol doesn't exist.
Attach the probes (uprobes, uretprobes, kprobes, kretprobes, tracepoints).
Loop every ~1s: read agg_map, sum the per-CPU values, compute deltas, drain the events ringbuf, and write everything to the DB.
On exit: detach and close everything, and the kernel frees the programs and maps.

*/