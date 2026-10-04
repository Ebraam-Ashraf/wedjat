package nvml

import (
	"errors"
	"testing"

	"github.com/Ebraam-Ashraf/wedjat/daemon/core/source"
)

func TestPollAllMarksAnyGPUFailureIncompleteAndKeepsSuccesses(t *testing.T) {
	gpus, procs, complete := pollAll([]string{"bad", "good"}, func(uuid string) (source.GPUSample, error) {
		if uuid == "bad" {
			return source.GPUSample{}, errors.New("poll")
		}
		return source.GPUSample{UUID: uuid}, nil
	}, func(uuid string) ([]source.ProcessSample, error) {
		if uuid == "bad" {
			t.Fatal("processes should not be polled after failed GPU snapshot")
		}
		return []source.ProcessSample{{PID: 1, GPUUUID: uuid}}, nil
	})
	if complete {
		t.Fatal("partial poll reported complete")
	}
	if len(gpus) != 1 || gpus[0].UUID != "good" || gpus[0].Index != 1 {
		t.Fatalf("successful GPU samples: %#v", gpus)
	}
	if len(procs) != 1 || procs[0].GPUUUID != "good" {
		t.Fatalf("successful process rows: %#v", procs)
	}
}

func TestPollAllPropagatesProcessListFailureAndEmptyUUID(t *testing.T) {
	_, _, complete := pollAll([]string{"gpu"}, func(string) (source.GPUSample, error) { return source.GPUSample{}, nil }, func(string) ([]source.ProcessSample, error) { return nil, errors.New("process poll") })
	if complete {
		t.Fatal("process-list failure reported complete")
	}
	_, _, complete = pollAll([]string{""}, func(string) (source.GPUSample, error) { return source.GPUSample{}, nil }, func(string) ([]source.ProcessSample, error) { return nil, nil })
	if complete {
		t.Fatal("skipped GPU reported complete")
	}
}
