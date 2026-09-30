package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const rollbackTimeout = 10 * time.Second

// StopFunc releases resources acquired by a stage.
type StopFunc func(context.Context) error

// Stage starts one daemon subsystem and returns its cleanup function.
// If Start returns both a cleanup function and an error, the cleanup function
// is called before previously started stages are unwound.
type Stage interface {
	Name() string
	Start(context.Context) (StopFunc, error)
}

type runningStage struct {
	name string
	stop StopFunc
}

// Runtime contains successfully started stages and closes them in reverse order.
type Runtime struct {
	stages []runningStage
	once   sync.Once
	err    error
}

// Start starts stages in order. A failure rolls back all acquired resources.
func Start(ctx context.Context, stages ...Stage) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("bootstrap: nil context")
	}

	runtime := &Runtime{}
	for _, stage := range stages {
		if stage == nil {
			return nil, errors.Join(errors.New("bootstrap: nil stage"), runtime.rollback(ctx))
		}

		name := stage.Name()
		if name == "" {
			return nil, errors.Join(errors.New("bootstrap: stage has empty name"), runtime.rollback(ctx))
		}

		stop, err := stage.Start(ctx)
		if err != nil {
			stageErr := fmt.Errorf("bootstrap stage %q: %w", name, err)
			var partialCleanupErr error
			if stop != nil {
				partialCleanupErr = stopWithRollbackContext(ctx, name, stop)
			}
			return nil, errors.Join(stageErr, partialCleanupErr, runtime.rollback(ctx))
		}
		if stop == nil {
			return nil, errors.Join(
				fmt.Errorf("bootstrap stage %q returned no cleanup function", name),
				runtime.rollback(ctx),
			)
		}
		runtime.stages = append(runtime.stages, runningStage{name: name, stop: stop})
	}
	return runtime, nil
}

// Close stops all stages in reverse order. It attempts every cleanup even if
// one fails, and subsequent calls return the original result without stopping
// a stage twice.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("bootstrap: nil shutdown context")
	}
	r.once.Do(func() {
		var errs []error
		for i := len(r.stages) - 1; i >= 0; i-- {
			stage := r.stages[i]
			if err := stage.stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("stop stage %q: %w", stage.name, err))
			}
		}
		r.err = errors.Join(errs...)
	})
	return r.err
}

func (r *Runtime) rollback(ctx context.Context) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	return r.Close(rollbackCtx)
}

func stopWithRollbackContext(ctx context.Context, name string, stop StopFunc) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := stop(rollbackCtx); err != nil {
		return fmt.Errorf("rollback stage %q: %w", name, err)
	}
	return nil
}
