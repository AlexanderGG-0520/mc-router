package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"
)

// WatchRunner runs one watch lifecycle.
type WatchRunner interface {
	Run(context.Context) error
}

// Sleeper waits for a retry delay, or returns early when ctx is cancelled.
type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

// BackoffPolicy controls retry delays after a watch exits with a retryable error.
type BackoffPolicy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Factor       float64
	Jitter       float64
}

// WatchSupervisorOptions configures a WatchSupervisor.
type WatchSupervisorOptions struct {
	Runner  WatchRunner
	Backoff BackoffPolicy
	Sleeper Sleeper
	Ready   <-chan struct{}
	Synced  <-chan struct{}
	Logger  *slog.Logger
}

// WatchSupervisor restarts a one-shot watch runner after runtime watch failures.
type WatchSupervisor struct {
	runner  WatchRunner
	backoff BackoffPolicy
	sleeper Sleeper
	ready   <-chan struct{}
	synced  <-chan struct{}
	logger  *slog.Logger
}

const (
	defaultWatchInitialRetryDelay = time.Second
	defaultWatchMaxRetryDelay     = 30 * time.Second
	defaultWatchRetryFactor       = 2.0
)

// NewWatchSupervisor creates a supervisor for a one-shot watch runner.
func NewWatchSupervisor(options WatchSupervisorOptions) (*WatchSupervisor, error) {
	if options.Runner == nil {
		return nil, errors.New("watch runner is nil")
	}
	backoff := options.Backoff.withDefaults()
	return &WatchSupervisor{
		runner:  options.Runner,
		backoff: backoff,
		sleeper: withDefaultSleeper(options.Sleeper),
		ready:   options.Ready,
		synced:  options.Synced,
		logger:  options.Logger,
	}, nil
}

// Run starts the runner and retries only after the initial watch has produced
// at least one successful sync. An initial list or watch setup failure is
// returned to the caller so startup can fail fast.
func (s *WatchSupervisor) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("watch supervisor is nil")
	}
	if ctx == nil {
		return errors.New("context is nil")
	}

	initialSyncSeen := s.channelClosed(s.ready)
	attempt := 0
	for {
		syncedThisRun, err := s.runOnce(ctx, func() {
			if attempt > 0 {
				logWatchSupervisorRecovered(s.logger)
				attempt = 0
			}
		})
		if isWatchNormalStop(ctx, err) {
			logWatchSupervisorStopped(s.logger)
			return nil
		}
		if err == nil {
			logWatchSupervisorStopped(s.logger)
			return nil
		}

		if syncedThisRun {
			initialSyncSeen = true
		} else if !initialSyncSeen && !s.channelClosed(s.ready) {
			return err
		}

		attempt++
		delay := s.backoff.Delay(attempt)
		logWatchSupervisorRetry(s.logger, err, attempt, delay)
		if sleepErr := s.sleeper.Sleep(ctx, delay); sleepErr != nil {
			if isWatchNormalStop(ctx, sleepErr) {
				logWatchSupervisorStopped(s.logger)
				return nil
			}
			return fmt.Errorf("sleep before Kubernetes watch retry: %w", sleepErr)
		}
	}
}

func (s *WatchSupervisor) runOnce(ctx context.Context, onSync func()) (bool, error) {
	s.drainSynced()
	done := make(chan error, 1)
	go func() {
		done <- s.runner.Run(ctx)
	}()

	synced := false
	for {
		select {
		case <-ctx.Done():
			err := <-done
			return synced, err
		case <-s.synced:
			if !synced {
				synced = true
				onSync()
			}
		case err := <-done:
			if !synced && s.drainSynced() {
				synced = true
				onSync()
			}
			return synced, err
		}
	}
}

func (s *WatchSupervisor) drainSynced() bool {
	drained := false
	for {
		select {
		case <-s.synced:
			drained = true
		default:
			return drained
		}
	}
}

func (s *WatchSupervisor) channelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (p BackoffPolicy) withDefaults() BackoffPolicy {
	if p.InitialDelay <= 0 {
		p.InitialDelay = defaultWatchInitialRetryDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = defaultWatchMaxRetryDelay
	}
	if p.Factor <= 1 {
		p.Factor = defaultWatchRetryFactor
	}
	if p.MaxDelay < p.InitialDelay {
		p.MaxDelay = p.InitialDelay
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	return p
}

// Delay returns the retry delay for a 1-based retry attempt.
func (p BackoffPolicy) Delay(attempt int) time.Duration {
	p = p.withDefaults()
	if attempt < 1 {
		attempt = 1
	}
	delayFloat := float64(p.InitialDelay) * math.Pow(p.Factor, float64(attempt-1))
	if delayFloat > float64(p.MaxDelay) {
		delayFloat = float64(p.MaxDelay)
	}
	delay := time.Duration(delayFloat)
	if p.Jitter == 0 || delay <= 0 {
		return delay
	}

	spread := float64(delay) * p.Jitter
	minDelay := float64(delay) - spread
	maxDelay := float64(delay) + spread
	jittered := minDelay + rand.Float64()*(maxDelay-minDelay)
	if jittered < 0 {
		jittered = 0
	}
	if jittered > float64(p.MaxDelay) {
		jittered = float64(p.MaxDelay)
	}
	return time.Duration(jittered)
}

type timerSleeper struct{}

func (timerSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func withDefaultSleeper(sleeper Sleeper) Sleeper {
	if sleeper != nil {
		return sleeper
	}
	return timerSleeper{}
}

func isWatchNormalStop(ctx context.Context, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func logWatchSupervisorRetry(logger *slog.Logger, err error, attempt int, delay time.Duration) {
	if logger != nil {
		logger.Warn(
			"kubernetes_discovery_watch_retry_scheduled",
			"error", err,
			"attempt", attempt,
			"next_retry_delay", delay.String(),
		)
	}
}

func logWatchSupervisorRecovered(logger *slog.Logger) {
	if logger != nil {
		logger.Info("kubernetes_discovery_watch_recovered")
	}
}

func logWatchSupervisorStopped(logger *slog.Logger) {
	if logger != nil {
		logger.Info("kubernetes_discovery_watch_supervisor_stopped")
	}
}
