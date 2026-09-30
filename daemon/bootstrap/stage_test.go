package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type testStage struct {
	name      string
	started   *[]string
	stopped   *[]string
	startErr  error
	stopErr   error
	noCleanup bool
}

func (s testStage) Name() string { return s.name }

func (s testStage) Start(context.Context) (StopFunc, error) {
	*s.started = append(*s.started, s.name)
	stop := func(context.Context) error {
		*s.stopped = append(*s.stopped, s.name)
		return s.stopErr
	}
	if s.noCleanup {
		stop = nil
	}
	return stop, s.startErr
}

func TestStartAndCloseReverseOrder(t *testing.T) {
	var started, stopped []string
	runtime, err := Start(context.Background(),
		testStage{name: "storage", started: &started, stopped: &stopped},
		testStage{name: "nvml", started: &started, stopped: &stopped},
		testStage{name: "probes", started: &started, stopped: &stopped},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"storage", "nvml", "probes"}; !reflect.DeepEqual(started, want) {
		t.Fatalf("start order = %v, want %v", started, want)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"probes", "nvml", "storage"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stop order = %v, want %v", stopped, want)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 3 {
		t.Fatalf("stages stopped more than once: %v", stopped)
	}
}

func TestStartFailureRollsBackIncludingFailingStage(t *testing.T) {
	var started, stopped []string
	startFailure := errors.New("attach failed")
	runtime, err := Start(context.Background(),
		testStage{name: "storage", started: &started, stopped: &stopped},
		testStage{name: "probes", started: &started, stopped: &stopped,
			startErr: startFailure},
		testStage{name: "socket", started: &started, stopped: &stopped},
	)
	if runtime != nil {
		t.Fatal("runtime should not be returned on failed startup")
	}
	if !errors.Is(err, startFailure) {
		t.Fatalf("startup error does not wrap original failure: %v", err)
	}
	if want := []string{"storage", "probes"}; !reflect.DeepEqual(started, want) {
		t.Fatalf("started stages = %v, want %v", started, want)
	}
	if want := []string{"probes", "storage"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("rollback order = %v, want %v", stopped, want)
	}
}

func TestCloseAttemptsAllStagesAndReportsErrors(t *testing.T) {
	var started, stopped []string
	stopFailure := errors.New("flush failed")
	runtime, err := Start(context.Background(),
		testStage{name: "storage", started: &started, stopped: &stopped,
			stopErr: stopFailure},
		testStage{name: "socket", started: &started, stopped: &stopped},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(context.Background()); !errors.Is(err, stopFailure) {
		t.Fatalf("Close() error = %v, want wrapped flush error", err)
	}
	if want := []string{"socket", "storage"}; !reflect.DeepEqual(stopped, want) {
		t.Fatalf("stops after cleanup error = %v, want %v", stopped, want)
	}
}

func TestStageMustReturnCleanup(t *testing.T) {
	var started, stopped []string
	_, err := Start(context.Background(), testStage{
		name: "broken", started: &started, stopped: &stopped, noCleanup: true,
	})
	if err == nil {
		t.Fatal("expected missing cleanup error")
	}
}
