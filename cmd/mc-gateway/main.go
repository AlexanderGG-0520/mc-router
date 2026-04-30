package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
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
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return err
	}
	routeTable, err := router.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := proxy.NewServer(cfg, routeTable, logger)
	err = server.ListenAndServe(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
