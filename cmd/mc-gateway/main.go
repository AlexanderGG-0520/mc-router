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

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/logging"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	"github.com/AlexanderGG-0520/mc-router/internal/udprelay"
	"github.com/AlexanderGG-0520/mc-router/internal/voicechat"
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
	udpRuntime, err := startUDPRelay(ctx, cfg, logger, server.Metrics())
	if err != nil {
		cancel()
		waitMetricsShutdown(ctx, metricsServer)
		return err
	}
	voiceRuntime, err := startVoiceChat(ctx, cfg, logger, server.Metrics())
	if err != nil {
		cancel()
		waitUDPRelayShutdown(udpRuntime)
		waitMetricsShutdown(ctx, metricsServer)
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
	err = waitForServers(ctx, cancel, server, proxyErrCh, metricsServer, udpRuntime, voiceRuntime)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type udpRelayRuntime struct {
	relay *udprelay.Relay
	done  chan error
}

type voiceChatRuntime struct {
	runtime *voicechat.Runtime
	done    chan error
}

func startUDPRelay(ctx context.Context, cfg config.Config, logger *slog.Logger, recorder *gatewaymetrics.Recorder) (*udpRelayRuntime, error) {
	if !cfg.UDPRelay.Enabled {
		return nil, nil
	}
	relay, err := udprelay.New(udprelay.Config{
		Listen:             cfg.UDPRelay.Listen,
		Backend:            cfg.UDPRelay.Backend,
		IdleTimeout:        cfg.UDPRelay.IdleTimeout.Duration,
		BackendDialTimeout: cfg.UDPRelay.BackendDialTimeout.Duration,
		MaxSessions:        cfg.UDPRelay.MaxSessions,
		MaxPacketSize:      cfg.UDPRelay.MaxPacketSize,
	}, logger, recorder)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- relay.Serve(ctx)
	}()
	return &udpRelayRuntime{relay: relay, done: done}, nil
}

func startVoiceChat(ctx context.Context, cfg config.Config, logger *slog.Logger, recorder *gatewaymetrics.Recorder) (*voiceChatRuntime, error) {
	if !cfg.VoiceChat.Enabled {
		return nil, nil
	}
	backends := make(map[string]voicechat.BackendConfig, len(cfg.VoiceChat.Backends))
	for id, backend := range cfg.VoiceChat.Backends {
		backends[id] = voicechat.BackendConfig{
			UDP:   backend.UDP,
			Token: backend.Token(),
		}
	}
	runtime, err := voicechat.NewRuntime(voicechat.Config{
		Listen:             cfg.VoiceChat.Listen,
		RegistrationListen: cfg.VoiceChat.Registration.Listen,
		RegistrationTTL:    cfg.VoiceChat.Registration.RegistrationTTL.Duration,
		RequestTimeout:     cfg.VoiceChat.Registration.RequestTimeout.Duration,
		MaxRegistrations:   cfg.VoiceChat.Registration.MaxRegistrations,
		IdleTimeout:        cfg.VoiceChat.Session.IdleTimeout.Duration,
		BackendDialTimeout: cfg.VoiceChat.Session.BackendDialTimeout.Duration,
		MaxSessions:        cfg.VoiceChat.Session.MaxSessions,
		MaxPacketSize:      cfg.VoiceChat.Session.MaxPacketSize,
		Backends:           backends,
	}, logger, recorder)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- runtime.Serve(ctx)
	}()
	return &voiceChatRuntime{runtime: runtime, done: done}, nil
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

func waitForServers(ctx context.Context, cancel context.CancelFunc, server *proxy.Server, proxyErrCh <-chan error, metricsServer *gatewaymetrics.HTTPServer, udpRuntime *udpRelayRuntime, voiceRuntime *voiceChatRuntime) error {
	select {
	case err := <-proxyErrCh:
		cancel()
		waitVoiceChatShutdown(voiceRuntime)
		waitUDPRelayShutdown(udpRuntime)
		waitMetricsShutdown(ctx, metricsServer)
		return err
	case err := <-metricsServerDone(metricsServer):
		cancel()
		waitVoiceChatShutdown(voiceRuntime)
		waitUDPRelayShutdown(udpRuntime)
		server.Shutdown()
		proxyErr := <-proxyErrCh
		if err != nil {
			return err
		}
		return proxyErr
	case err := <-udpRelayDone(udpRuntime):
		cancel()
		waitVoiceChatShutdown(voiceRuntime)
		server.Shutdown()
		waitMetricsShutdown(ctx, metricsServer)
		return err
	case err := <-voiceChatDone(voiceRuntime):
		cancel()
		waitUDPRelayShutdown(udpRuntime)
		server.Shutdown()
		waitMetricsShutdown(ctx, metricsServer)
		return err
	}
}

func udpRelayDone(udpRuntime *udpRelayRuntime) <-chan error {
	if udpRuntime == nil {
		return nil
	}
	return udpRuntime.done
}

func metricsServerDone(metricsServer *gatewaymetrics.HTTPServer) <-chan error {
	if metricsServer == nil {
		return nil
	}
	return metricsServer.Done()
}

func voiceChatDone(voiceRuntime *voiceChatRuntime) <-chan error {
	if voiceRuntime == nil {
		return nil
	}
	return voiceRuntime.done
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

func waitUDPRelayShutdown(udpRuntime *udpRelayRuntime) {
	if udpRuntime == nil {
		return
	}
	_ = udpRuntime.relay.Close()
	<-udpRuntime.done
}

func waitVoiceChatShutdown(voiceRuntime *voiceChatRuntime) {
	if voiceRuntime == nil {
		return
	}
	_ = voiceRuntime.runtime.Close()
	<-voiceRuntime.done
}
