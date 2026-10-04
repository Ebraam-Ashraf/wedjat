package ebpf

import "testing"

func TestMergeAggSumsCountersAndKeepsMaximum(t *testing.T) {
	got := mergeAgg([]aggVal{{Count: 2, Bytes: 5, LatencySumNs: 8, LatencyMaxNs: 7, AllocBytes: 3, FreeBytes: 4, Errors: 1, UvmFaults: 2, UvmEvicts: 3}, {Count: 6, Bytes: 9, LatencySumNs: 10, LatencyMaxNs: 4, AllocBytes: 11, FreeBytes: 12, Errors: 5, UvmFaults: 6, UvmEvicts: 7}})
	want := aggVal{Count: 8, Bytes: 14, LatencySumNs: 18, LatencyMaxNs: 7, AllocBytes: 14, FreeBytes: 16, Errors: 6, UvmFaults: 8, UvmEvicts: 10}
	if got != want {
		t.Fatalf("merged %#v, want %#v", got, want)
	}
}

func TestSumKernelStats(t *testing.T) {
	got, err := sumKernelStats(func(slot uint32) ([]statsVal, error) {
		if slot == 0 {
			return []statsVal{{RingbufDrops: 1, MapUpdateFailures: 2, UnknownDeviceEvents: 3, AllocFreeMisses: 4}, {RingbufDrops: 5, MapUpdateFailures: 6, UnknownDeviceEvents: 7, AllocFreeMisses: 8}}, nil
		}
		if slot == 19 {
			return []statsVal{{RingbufDrops: 9, MapUpdateFailures: 10, UnknownDeviceEvents: 11, AllocFreeMisses: 12}}, nil
		}
		return []statsVal{{}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Stats{RingbufDrops: 15, MapUpdateFailures: 18, UnknownDeviceEvents: 21, AllocFreeMisses: 24}
	if got != want {
		t.Fatalf("stats %#v, want %#v", got, want)
	}
}

func TestStateBelongsToProcess(t *testing.T) {
	if !stateBelongsToProcess(7, 99, 99) {
		t.Fatal("matching generation rejected")
	}
	if stateBelongsToProcess(7, 99, 100) || stateBelongsToProcess(7, 0, 0) || stateBelongsToProcess(0, 99, 99) {
		t.Fatal("mismatched/zero generation accepted")
	}
}
