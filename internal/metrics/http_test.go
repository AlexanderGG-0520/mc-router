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
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	dto "github.com/prometheus/client_model/go"
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
	recorder.FallbackResponse(FallbackStateLogin, ReasonRouteDenied)
	recorder.FallbackResponse(FallbackStateStatus, ReasonRouteDenied)
	recorder.RouteDecision(RouteDecisionDenied)
	recorder.Reload(ReloadResultFailed)
	recorder.KubernetesWatchRunning(true)
	recorder.KubernetesWatchRunning(false)
	recorder.KubernetesWatchRestart(KubernetesWatchRestartReasonWatchError)
	recorder.KubernetesDiscoverySync(2)
	recorder.KubernetesSkippedServicesByReason(map[string]int{k8sdiscovery.ReasonInvalidHost: 1})
	recorder.KubernetesDiscoveryError(KubernetesDiscoveryErrorReasonRebuildFailed)
}

func TestStartHTTPServesPrometheusTextAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	recorder := NewRecorder(true)
	recorder.ConnectionAccepted()
	recorder.ConnectionFinished(ConnectionResultDenied, ReasonRouteDenied, time.Millisecond)
	recorder.FallbackResponse(FallbackStateStatus, ReasonRouteDenied)
	recorder.KubernetesWatchRunning(true)
	recorder.KubernetesWatchRestart(KubernetesWatchRestartReasonWatchError)
	recorder.KubernetesDiscoverySync(2)
	recorder.KubernetesSkippedServicesByReason(map[string]int{k8sdiscovery.ReasonInvalidHost: 1})
	recorder.KubernetesDiscoveryError(KubernetesDiscoveryErrorReasonRebuildFailed)

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
	for _, name := range []string{
		"mc_gateway_kubernetes_watch_restarts_total",
		"mc_gateway_kubernetes_watch_running",
		"mc_gateway_kubernetes_last_successful_sync_timestamp_seconds",
		"mc_gateway_kubernetes_discovered_routes",
		"mc_gateway_kubernetes_skipped_services",
		"mc_gateway_kubernetes_discovery_errors_total",
	} {
		if !strings.Contains(string(body), name) {
			t.Fatalf("metrics body did not include %s:\n%s", name, string(body))
		}
	}
	if strings.Contains(string(body), "namespace=") || strings.Contains(string(body), "service=") || strings.Contains(string(body), "host=") || strings.Contains(string(body), "backend=") {
		t.Fatalf("Kubernetes discovery metrics included high-cardinality labels:\n%s", string(body))
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

func TestKubernetesSkippedServicesByReasonMetricLabelsAndValues(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.KubernetesSkippedServicesByReason(map[string]int{
		k8sdiscovery.ReasonInvalidHost:   2,
		k8sdiscovery.ReasonDuplicateHost: 1,
	})

	family := metricFamily(t, recorder, "mc_gateway_kubernetes_skipped_services")
	if family == nil {
		t.Fatal("mc_gateway_kubernetes_skipped_services metric was not exported")
	}
	values := make(map[string]float64)
	for _, metric := range family.GetMetric() {
		labels := metric.GetLabel()
		if len(labels) != 1 || labels[0].GetName() != "reason" {
			t.Fatalf("metric labels = %v, want only reason", labels)
		}
		for _, label := range labels {
			switch label.GetName() {
			case "namespace", "service", "host", "backend":
				t.Fatalf("metric included high-cardinality label %q", label.GetName())
			}
		}
		values[labels[0].GetValue()] = metric.GetGauge().GetValue()
	}

	if values[k8sdiscovery.ReasonInvalidHost] != 2 {
		t.Fatalf("invalid_host count = %v, want 2", values[k8sdiscovery.ReasonInvalidHost])
	}
	if values[k8sdiscovery.ReasonDuplicateHost] != 1 {
		t.Fatalf("duplicate_host count = %v, want 1", values[k8sdiscovery.ReasonDuplicateHost])
	}
	if values[k8sdiscovery.ReasonDisabled] != 0 {
		t.Fatalf("disabled count = %v, want 0", values[k8sdiscovery.ReasonDisabled])
	}
}

func TestKubernetesSkippedServicesByReasonResetsMissingReasons(t *testing.T) {
	recorder := NewRecorder(true)
	recorder.KubernetesSkippedServicesByReason(map[string]int{
		k8sdiscovery.ReasonPortNotFound: 3,
	})
	if got := metricGaugeValue(t, recorder, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonPortNotFound}); got != 3 {
		t.Fatalf("port_not_found count = %v, want 3", got)
	}

	recorder.KubernetesSkippedServicesByReason(map[string]int{
		k8sdiscovery.ReasonDuplicateHost: 1,
	})
	if got := metricGaugeValue(t, recorder, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonPortNotFound}); got != 0 {
		t.Fatalf("stale port_not_found count = %v, want 0", got)
	}
	if got := metricGaugeValue(t, recorder, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonDuplicateHost}); got != 1 {
		t.Fatalf("duplicate_host count = %v, want 1", got)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func metricFamily(t *testing.T, recorder *Recorder, name string) *dto.MetricFamily {
	t.Helper()
	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	return nil
}

func metricGaugeValue(t *testing.T, recorder *Recorder, name string, labels map[string]string) float64 {
	t.Helper()
	family := metricFamily(t, recorder, name)
	if family == nil {
		t.Fatalf("metric %s was not exported", name)
	}
	for _, metric := range family.GetMetric() {
		if metricLabelsMatch(metric, labels) {
			return metric.GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s%v was not exported", name, labels)
	return 0
}

func metricLabelsMatch(metric *dto.Metric, labels map[string]string) bool {
	if len(metric.GetLabel()) != len(labels) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if labels[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}
