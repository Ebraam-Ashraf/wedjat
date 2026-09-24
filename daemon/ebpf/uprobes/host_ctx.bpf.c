SEC("uprobe/cuCtxSetCurrent")           // ctx -> look up device, write tid_to_device
SEC("uprobe/cuDevicePrimaryCtxRetain")  // pctx, dev -> write tid_to_device directly

SEC("uretprobe/cuDevicePrimaryCtxRetain") // exit: read *pctx, fill ctx_to_device
SEC("uprobe/cuCtxSetCurrent_ptsz")        // only if your libcuda exports it, check with nm
SEC("uprobe/cuCtxCreate_v2")              // pctx, flags, dev -> new ctx bound to dev
SEC("uretprobe/cuCtxCreate_v2")           // exit: read *pctx, fill ctx_to_device
SEC("uprobe/cuCtxPushCurrent_v2")         // ctx -> thread binding changes here too
SEC("uprobe/cuCtxPopCurrent_v2")          // thread unbinds, tid_to_device goes stale
SEC("uprobe/cuCtxDestroy_v2")             // cleanup ctx_to_device entry
SEC("uprobe/cuDevicePrimaryCtxRelease_v2")