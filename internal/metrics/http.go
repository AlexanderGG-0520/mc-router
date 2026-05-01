package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type HTTPServer struct {
	server *http.Server
	addr   string
	done   chan error
}

func StartHTTP(ctx context.Context, cfg config.Metrics, recorder *Recorder, logger *slog.Logger) (*HTTPServer, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if recorder == nil {
		return nil, errors.New("metrics recorder is nil")
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Path, promhttp.HandlerFor(recorder.Registry(), promhttp.HandlerOpts{}))
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen metrics %q: %w", cfg.Listen, err)
	}

	metricsServer := &HTTPServer{
		server: server,
		addr:   listener.Addr().String(),
		done:   make(chan error, 1),
	}
	logger.Info("metrics_listening", "address", listener.Addr().String(), "path", cfg.Path)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err != nil {
			logger.Error("metrics_server_failed", "error", err)
		}
		metricsServer.done <- err
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("metrics_shutdown_failed", "error", err)
			_ = server.Close()
		}
	}()

	return metricsServer, nil
}

func (s *HTTPServer) Done() <-chan error {
	if s == nil {
		return nil
	}
	return s.done
}

func (s *HTTPServer) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

func (s *HTTPServer) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
