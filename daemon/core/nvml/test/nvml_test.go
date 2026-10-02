package nvml_test

import (
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/nvml"
)

func TestValidityBitsAreDistinct(t *testing.T) {
	bits := []uint64{
		nvml.ValidGPUUtil,
		nvml.ValidMemUtil,
		nvml.ValidMemUsed,
		nvml.ValidTemp,
		nvml.ValidPower,
		nvml.ValidSMClock,
		nvml.ValidMemClock,
		nvml.ValidThrottleReason,
		nvml.ValidPowerLimit,
		nvml.ValidECCUncorrected,
	}
	seen := uint64(0)
	for _, bit := range bits {
		if bit == 0 || seen&bit != 0 {
			t.Fatalf("invalid or duplicate validity bit %#x", bit)
		}
		seen |= bit
	}
}

func TestValidFieldsBitmaskCombinations(t *testing.T) {
	// Build a combined mask with a representative subset of fields.
	combined := nvml.ValidGPUUtil | nvml.ValidTemp | nvml.ValidPower | nvml.ValidECCUncorrected

	// Every field ORed in must be detectable via AND.
	for _, tc := range []struct {
		name string
		bit  uint64
		want bool
	}{
		{"GPUUtil", nvml.ValidGPUUtil, true},
		{"Temp", nvml.ValidTemp, true},
		{"Power", nvml.ValidPower, true},
		{"ECCUncorrected", nvml.ValidECCUncorrected, true},
		// These were NOT ORed in — they must NOT appear.
		{"MemUtil", nvml.ValidMemUtil, false},
		{"MemUsed", nvml.ValidMemUsed, false},
		{"SMClock", nvml.ValidSMClock, false},
		{"MemClock", nvml.ValidMemClock, false},
		{"ThrottleReason", nvml.ValidThrottleReason, false},
		{"PowerLimit", nvml.ValidPowerLimit, false},
	} {
		present := combined&tc.bit != 0
		if present != tc.want {
			t.Errorf("bit %s (%#x): combined&bit != 0 = %v, want %v", tc.name, tc.bit, present, tc.want)
		}
	}

	// Clearing one bit must not affect any other bit.
	cleared := combined &^ nvml.ValidTemp
	if cleared&nvml.ValidTemp != 0 {
		t.Error("ValidTemp still set after clearing it")
	}
	if cleared&nvml.ValidGPUUtil == 0 {
		t.Error("ValidGPUUtil was unintentionally cleared")
	}
	if cleared&nvml.ValidPower == 0 {
		t.Error("ValidPower was unintentionally cleared")
	}

	// OR-ing two disjoint subsets must equal the full combined mask.
	half1 := nvml.ValidGPUUtil | nvml.ValidTemp
	half2 := nvml.ValidPower | nvml.ValidECCUncorrected
	if half1|half2 != combined {
		t.Errorf("half1 | half2 = %#x, want %#x", half1|half2, combined)
	}
}

func TestSampleModelsPreserveUnknownState(t *testing.T) {
	sample := nvml.GPUSample{UUID: "GPU-test", Valid: true, ValidFields: nvml.ValidGPUUtil}
	if !sample.Valid || sample.ValidFields&nvml.ValidGPUUtil == 0 {
		t.Fatal("reported GPU sample lost its validity state")
	}
	if sample.ValidFields&nvml.ValidMemUsed != 0 {
		t.Fatal("unreported memory field was marked valid")
	}

	process := nvml.ProcessSample{PID: 42, GPUUUID: "GPU-test", VRAMBytes: 123, VRAMValid: false}
	if process.VRAMValid {
		t.Fatal("unavailable process VRAM was marked valid")
	}
}
