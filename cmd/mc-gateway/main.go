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

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/logging"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	"github.com/AlexanderGG-0520/mc-router/internal/router"
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
	cfg, routeTable, err := loadRouteConfig(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := proxy.NewServer(cfg, routeTable, logger)
	reloadCh := make(chan os.Signal, 1)
	if signals := reloadSignals(); len(signals) > 0 {
		signal.Notify(reloadCh, signals...)
		defer signal.Stop(reloadCh)
		go serveReloadSignals(ctx, reloadCh, configPath, server)
	} else {
		logger.Info("config reload signal unavailable", "platform", runtime.GOOS)
	}
	err = server.ListenAndServe(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type configReloader interface {
	ReloadFile(path string) error
}

func loadRouteConfig(configPath string) (config.Config, *router.Router, error) {
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return config.Config{}, nil, err
	}
	routeTable, err := router.New(cfg)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, routeTable, nil
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
