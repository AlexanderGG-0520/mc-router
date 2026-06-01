package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/logging"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
)

func main() {
	var configPath string
	var logLevel string
	flag.StringVar(&configPath, "config", "config.yaml", "path to YAML config file")
	flag.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	logger := logging.New(logLevel)
	if err := run(configPath, logger); err != nil {
		logger.Error("mc-gateway exited", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	startup, err := loadStartupWithDiscovery(ctx, configPath, logger, defaultDiscoveryStartupDeps())
	if err != nil {
		return err
	}
	snapshot := startup.Snapshot
	cfg := snapshot.Config

	server := proxy.NewServerFromSnapshot(snapshot, logger)
	recordStartupDiscoveryMetrics(server.Metrics(), startup.DiscoveryReport)
	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(ctx, snapshot.StaticConfig, server, logger, defaultDiscoveryRuntimeDeps())
	if err != nil {
		return err
	}
	defer runtimeDiscovery.Stop()

	metricsServer, err := gatewaymetrics.StartHTTP(ctx, cfg.Metrics, server.Metrics(), logger)
	if err != nil {
		return err
	}

	reloadCh := make(chan os.Signal, 1)
	if signals := reloadSignals(); len(signals) > 0 {
		signal.Notify(reloadCh, signals...)
		defer signal.Stop(reloadCh)
		reloader := newKubernetesReloadReloader(ctx, server, logger, defaultDiscoveryStartupDeps(), snapshot.StaticConfig.Discovery.Kubernetes)
		go serveReloadSignals(ctx, reloadCh, configPath, reloader)
	} else {
		logger.Info("config reload signal unavailable", "platform", runtime.GOOS)
	}
	proxyErrCh := make(chan error, 1)
	go func() {
		proxyErrCh <- server.ListenAndServe(ctx)
	}()
	err = waitForServers(ctx, cancel, server, proxyErrCh, metricsServer)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func recordStartupDiscoveryMetrics(recorder *gatewaymetrics.Recorder, report *startupDiscoveryReport) {
	if recorder == nil || report == nil {
		return
	}
	recorder.KubernetesSkippedServicesByReason(report.Result.SkippedByReason)
}

type configReloader interface {
	ReloadFile(path string) error
}

func serveReloadSignals(ctx context.Context, reloadCh <-chan os.Signal, configPath string, reloader configReloader) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloadCh:
			_ = reloader.ReloadFile(configPath)
		}
	}
}

func waitForServers(ctx context.Context, cancel context.CancelFunc, server *proxy.Server, proxyErrCh <-chan error, metricsServer *gatewaymetrics.HTTPServer) error {
	select {
	case err := <-proxyErrCh:
		cancel()
		waitMetricsShutdown(ctx, metricsServer)
		return err
	case err := <-metricsServerDone(metricsServer):
		cancel()
		server.Shutdown()
		proxyErr := <-proxyErrCh
		if err != nil {
			return err
		}
		return proxyErr
	}
}

func metricsServerDone(metricsServer *gatewaymetrics.HTTPServer) <-chan error {
	if metricsServer == nil {
		return nil
	}
	return metricsServer.Done()
}

func waitMetricsShutdown(ctx context.Context, metricsServer *gatewaymetrics.HTTPServer) {
	if metricsServer == nil {
		return
	}
	select {
	case <-metricsServer.Done():
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metricsServer.Shutdown(shutdownCtx)
		<-metricsServer.Done()
	}
}
