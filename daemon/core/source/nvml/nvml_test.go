package nvml_test

import (
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

func TestValidityBitsAreDistinct(t *testing.T) {
	bits := []uint64{
		source.ValidGPUUtil,
		source.ValidMemUtil,
		source.ValidMemUsed,
		source.ValidTemp,
		source.ValidPower,
		source.ValidSMClock,
		source.ValidMemClock,
		source.ValidThrottleReason,
		source.ValidPowerLimit,
		source.ValidECCUncorrected,
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
	combined := source.ValidGPUUtil | source.ValidTemp | source.ValidPower | source.ValidECCUncorrected

	for _, tc := range []struct {
		name string
		bit  uint64
		want bool
	}{
		{"GPUUtil", source.ValidGPUUtil, true},
		{"Temp", source.ValidTemp, true},
		{"Power", source.ValidPower, true},
		{"ECCUncorrected", source.ValidECCUncorrected, true},
		{"MemUtil", source.ValidMemUtil, false},
		{"MemUsed", source.ValidMemUsed, false},
		{"SMClock", source.ValidSMClock, false},
		{"MemClock", source.ValidMemClock, false},
		{"ThrottleReason", source.ValidThrottleReason, false},
		{"PowerLimit", source.ValidPowerLimit, false},
	} {
		present := combined&tc.bit != 0
		if present != tc.want {
			t.Errorf("bit %s (%#x): combined&bit != 0 = %v, want %v", tc.name, tc.bit, present, tc.want)
		}
	}

	cleared := combined &^ source.ValidTemp
	if cleared&source.ValidTemp != 0 {
		t.Error("ValidTemp still set after clearing it")
	}
	if cleared&source.ValidGPUUtil == 0 {
		t.Error("ValidGPUUtil was unintentionally cleared")
	}
	if cleared&source.ValidPower == 0 {
		t.Error("ValidPower was unintentionally cleared")
	}

	half1 := source.ValidGPUUtil | source.ValidTemp
	half2 := source.ValidPower | source.ValidECCUncorrected
	if half1|half2 != combined {
		t.Errorf("half1 | half2 = %#x, want %#x", half1|half2, combined)
	}
}

func TestSampleModelsPreserveUnknownState(t *testing.T) {
	sample := source.GPUSample{UUID: "GPU-test", Valid: true, ValidFields: source.ValidGPUUtil}
	if !sample.Valid || sample.ValidFields&source.ValidGPUUtil == 0 {
		t.Fatal("reported GPU sample lost its validity state")
	}
	if sample.ValidFields&source.ValidMemUsed != 0 {
		t.Fatal("unreported memory field was marked valid")
	}

	process := source.ProcessSample{PID: 42, GPUUUID: "GPU-test", VRAMBytes: 123, VRAMValid: false}
	if process.VRAMValid {
		t.Fatal("unavailable process VRAM was marked valid")
	}
}
