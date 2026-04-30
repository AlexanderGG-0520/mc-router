package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

type fakeReloader struct {
	paths chan string
	err   error
}

func (f *fakeReloader) ReloadFile(path string) error {
	f.paths <- path
	return f.err
}

func TestServeReloadSignalsCallsReloader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan os.Signal, 1)
	reloader := &fakeReloader{paths: make(chan string, 1)}
	go serveReloadSignals(ctx, reloadCh, "config.yaml", reloader)

	reloadCh <- os.Interrupt
	select {
	case got := <-reloader.paths:
		if got != "config.yaml" {
			t.Fatalf("reload path = %q, want config.yaml", got)
		}
	case <-time.After(time.Second):
		t.Fatal("reloader was not called")
	}
}

func TestServeReloadSignalsContinuesAfterReloadError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadCh := make(chan os.Signal, 2)
	reloader := &fakeReloader{
		paths: make(chan string, 2),
		err:   errors.New("reload failed"),
	}
	go serveReloadSignals(ctx, reloadCh, "config.yaml", reloader)

	reloadCh <- os.Interrupt
	reloadCh <- os.Interrupt
	for i := 0; i < 2; i++ {
		select {
		case <-reloader.paths:
		case <-time.After(time.Second):
			t.Fatal("reloader was not called after reload error")
		}
	}
}

func TestServeReloadSignalsStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reloadCh := make(chan os.Signal)
	reloader := &fakeReloader{paths: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveReloadSignals(ctx, reloadCh, "config.yaml", reloader)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reload signal loop did not stop after context cancellation")
	}
}
