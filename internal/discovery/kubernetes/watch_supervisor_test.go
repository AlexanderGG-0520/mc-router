package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestWatchSupervisorRetriesAfterWatchError(t *testing.T) {
	ready := make(chan struct{})
	synced := make(chan struct{}, 2)
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				close(ready)
				synced <- struct{}{}
				return errors.New("watch error")
			},
			func(context.Context) error {
				synced <- struct{}{}
				return nil
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, ready, synced, sleeper, BackoffPolicy{InitialDelay: time.Second})

	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	if got, want := sleeper.delays, []time.Duration{time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sleep delays = %v, want %v", got, want)
	}
}

func TestWatchSupervisorRetriesAfterUnexpectedClose(t *testing.T) {
	ready := make(chan struct{})
	synced := make(chan struct{}, 2)
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				close(ready)
				synced <- struct{}{}
				return errors.New("watch channel closed")
			},
			func(context.Context) error {
				synced <- struct{}{}
				return nil
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, ready, synced, sleeper, BackoffPolicy{InitialDelay: time.Second})

	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
}

func TestWatchSupervisorRetriesRelistFailureAfterInitialSync(t *testing.T) {
	ready := make(chan struct{})
	synced := make(chan struct{}, 1)
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				close(ready)
				synced <- struct{}{}
				return errors.New("list failed")
			},
			func(context.Context) error {
				return nil
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, ready, synced, sleeper, BackoffPolicy{InitialDelay: 500 * time.Millisecond})

	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got, want := sleeper.delays, []time.Duration{500 * time.Millisecond}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sleep delays = %v, want %v", got, want)
	}
}

func TestWatchSupervisorStartupFailureDoesNotRetryBeforeInitialSync(t *testing.T) {
	ready := make(chan struct{})
	synced := make(chan struct{})
	startupErr := errors.New("initial list failed")
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				return startupErr
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, ready, synced, sleeper, BackoffPolicy{})

	err := supervisor.Run(context.Background())
	if !errors.Is(err, startupErr) {
		t.Fatalf("Run() error = %v, want %v", err, startupErr)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if len(sleeper.delays) != 0 {
		t.Fatalf("sleep delays = %v, want none", sleeper.delays)
	}
}

func TestWatchSupervisorContextCancellationStopsRetryLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(ctx context.Context) error {
				cancel()
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, nil, nil, sleeper, BackoffPolicy{})

	if err := supervisor.Run(ctx); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if len(sleeper.delays) != 0 {
		t.Fatalf("sleep delays = %v, want none", sleeper.delays)
	}
}

func TestWatchSupervisorCancellationErrorDoesNotRetry(t *testing.T) {
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				return context.Canceled
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, nil, nil, sleeper, BackoffPolicy{})

	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if len(sleeper.delays) != 0 {
		t.Fatalf("sleep delays = %v, want none", sleeper.delays)
	}
}

func TestWatchSupervisorBackoffResetsAfterSuccessfulSync(t *testing.T) {
	ready := make(chan struct{})
	synced := make(chan struct{}, 3)
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				close(ready)
				synced <- struct{}{}
				return errors.New("first runtime failure")
			},
			func(context.Context) error {
				synced <- struct{}{}
				return errors.New("second runtime failure")
			},
			func(context.Context) error {
				synced <- struct{}{}
				return nil
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, ready, synced, sleeper, BackoffPolicy{InitialDelay: time.Second, Factor: 2})

	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got, want := sleeper.delays, []time.Duration{time.Second, time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sleep delays = %v, want %v", got, want)
	}
}

func TestWatchSupervisorBackoffMaxDelayIsCapped(t *testing.T) {
	ready := make(chan struct{})
	synced := make(chan struct{}, 1)
	runner := &scriptedWatchRunner{
		runs: []func(context.Context) error{
			func(context.Context) error {
				close(ready)
				synced <- struct{}{}
				return errors.New("first runtime failure")
			},
			func(context.Context) error {
				return errors.New("second runtime failure")
			},
			func(context.Context) error {
				return errors.New("third runtime failure")
			},
			func(context.Context) error {
				return nil
			},
		},
	}
	sleeper := &recordingSleeper{}
	supervisor := newTestWatchSupervisor(t, runner, ready, synced, sleeper, BackoffPolicy{
		InitialDelay: time.Second,
		MaxDelay:     3 * time.Second,
		Factor:       2,
	})

	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got, want := sleeper.delays, []time.Duration{time.Second, 2 * time.Second, 3 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sleep delays = %v, want %v", got, want)
	}
}

func newTestWatchSupervisor(t *testing.T, runner WatchRunner, ready <-chan struct{}, synced <-chan struct{}, sleeper Sleeper, backoff BackoffPolicy) *WatchSupervisor {
	t.Helper()
	supervisor, err := NewWatchSupervisor(WatchSupervisorOptions{
		Runner:  runner,
		Ready:   ready,
		Synced:  synced,
		Sleeper: sleeper,
		Backoff: backoff,
	})
	if err != nil {
		t.Fatalf("NewWatchSupervisor: %v", err)
	}
	return supervisor
}

type scriptedWatchRunner struct {
	runs  []func(context.Context) error
	calls int
}

func (r *scriptedWatchRunner) Run(ctx context.Context) error {
	if r.calls >= len(r.runs) {
		r.calls++
		return nil
	}
	run := r.runs[r.calls]
	r.calls++
	return run(ctx)
}

type recordingSleeper struct {
	delays []time.Duration
}

func (s *recordingSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	s.delays = append(s.delays, delay)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
