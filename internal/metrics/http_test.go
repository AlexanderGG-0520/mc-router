package metrics

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
)

func TestStartHTTPDisabledDoesNothing(t *testing.T) {
	server, err := StartHTTP(context.Background(), config.Metrics{}, NewRecorder(false), testLogger())
	if err != nil {
		t.Fatalf("StartHTTP returned error: %v", err)
	}
	if server != nil {
		t.Fatal("StartHTTP returned server when metrics are disabled")
	}
}

func TestStartHTTPRejectsDisabledRecorderWhenEnabled(t *testing.T) {
	_, err := StartHTTP(context.Background(), config.Metrics{
		Enabled: true,
		Listen:  "127.0.0.1:0",
		Path:    "/metrics",
	}, NewRecorder(false), testLogger())
	if err == nil {
		t.Fatal("StartHTTP succeeded with a disabled recorder")
	}
}

func TestDisabledRecorderIsNoop(t *testing.T) {
	recorder := NewRecorder(false)
	if recorder.Registry() != nil {
		t.Fatal("disabled recorder has a registry")
	}
	recorder.SetConfig(1, config.Defaults())
	recorder.ConnectionAccepted()
	recorder.ConnectionFinished(ConnectionResultDenied, ReasonRouteDenied, time.Millisecond)
	recorder.BackendDialFinished(ConnectionResultFailed, ReasonBackendDialFailed, time.Millisecond)
	recorder.FallbackResponse(FallbackStateStatus, ReasonRouteDenied)
	recorder.RouteDecision(RouteDecisionDenied)
	recorder.Reload(ReloadResultFailed)
}

func TestStartHTTPServesPrometheusTextAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	recorder := NewRecorder(true)
	recorder.ConnectionAccepted()
	recorder.ConnectionFinished(ConnectionResultDenied, ReasonRouteDenied, time.Millisecond)
	recorder.FallbackResponse(FallbackStateStatus, ReasonRouteDenied)

	server, err := StartHTTP(ctx, config.Metrics{
		Enabled: true,
		Listen:  "127.0.0.1:0",
		Path:    "/metrics",
	}, recorder, testLogger())
	if err != nil {
		t.Fatalf("StartHTTP returned error: %v", err)
	}

	client := http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + server.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("content type = %q, want Prometheus text", got)
	}
	if !strings.Contains(string(body), "mc_gateway_connections_total") {
		t.Fatalf("metrics body did not include connection counter:\n%s", string(body))
	}
	if !strings.Contains(string(body), `mc_gateway_fallback_responses_total{reason="route_denied",state="status"} 1`) {
		t.Fatalf("metrics body did not include fallback response counter:\n%s", string(body))
	}

	cancel()
	select {
	case err := <-server.Done():
		if err != nil {
			t.Fatalf("metrics server returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("metrics server did not stop after context cancellation")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
