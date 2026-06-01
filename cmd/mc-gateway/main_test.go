package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery"
	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	gatewaymetrics "github.com/AlexanderGG-0520/mc-router/internal/metrics"
	"github.com/AlexanderGG-0520/mc-router/internal/proxy"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
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

func TestReloadDiscoveryReportRouteProviderUsesResultRoutes(t *testing.T) {
	report := newReloadDiscoveryReport(k8sdiscovery.Result{
		Routes: []k8sdiscovery.DiscoveredRoute{
			{Host: "one.example.com", Backend: "one.default.svc.cluster.local:25565"},
			{Host: "two.example.com", Backend: "two.default.svc.cluster.local:25565"},
		},
		SkippedByReason: map[string]int{
			k8sdiscovery.ReasonDuplicateHost: 2,
		},
	})

	routes, err := report.routeProvider().Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes length = %d, want 2", len(routes))
	}
	if routes[0].Host != "one.example.com" || routes[1].Host != "two.example.com" {
		t.Fatalf("routes = %#v", routes)
	}
	if got := report.Result.SkippedByReason[k8sdiscovery.ReasonDuplicateHost]; got != 2 {
		t.Fatalf("skipped duplicate host count = %d, want 2", got)
	}
}

func TestReloadDiscoveryReportRouteProviderDefensivelyCopiesRoutes(t *testing.T) {
	result := k8sdiscovery.Result{
		Routes: []k8sdiscovery.DiscoveredRoute{
			{Host: "copy.example.com", Backend: "copy.default.svc.cluster.local:25565"},
		},
	}
	report := newReloadDiscoveryReport(result)
	result.Routes[0].Host = "mutated-before-read.example.com"

	provider := report.routeProvider()
	first, err := provider.Routes(context.Background())
	if err != nil {
		t.Fatalf("first Routes: %v", err)
	}
	first[0].Host = "mutated-after-read.example.com"
	second, err := provider.Routes(context.Background())
	if err != nil {
		t.Fatalf("second Routes: %v", err)
	}
	if second[0].Host != "copy.example.com" {
		t.Fatalf("route provider did not preserve defensive copy, got host %q", second[0].Host)
	}
}

func TestBuildReloadDiscoveryReportFromServiceList(t *testing.T) {
	cfg := reloadDiscoveryConfig("minecraft", k8sdiscovery.DefaultAnnotationPrefix)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	report, err := buildReloadDiscoveryReport(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("buildReloadDiscoveryReport: %v", err)
	}
	if lister.namespace != "minecraft" {
		t.Fatalf("listed namespace = %q, want minecraft", lister.namespace)
	}
	routes, err := report.routeProvider().Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(routes))
	}
	if routes[0].Host != "smp.example.com" {
		t.Fatalf("route host = %q, want smp.example.com", routes[0].Host)
	}
	if routes[0].Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("route backend = %q, want smp.minecraft.svc.cluster.local:25565", routes[0].Backend)
	}
}

func TestBuildReloadDiscoveryReportRespectsAnnotationPrefix(t *testing.T) {
	const prefix = "custom.example.com"
	cfg := reloadDiscoveryConfig("minecraft", prefix)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			reloadServiceWithPrefix("custom", "minecraft", "custom.example.com", 25565, prefix),
			startupService("default-prefix", "minecraft", "default.example.com", 25566),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	report, err := buildReloadDiscoveryReport(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("buildReloadDiscoveryReport: %v", err)
	}
	routes, err := report.routeProvider().Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(routes))
	}
	if routes[0].Host != "custom.example.com" {
		t.Fatalf("route host = %q, want custom.example.com", routes[0].Host)
	}
	if got := report.Result.SkippedByReason[k8sdiscovery.ReasonDisabled]; got != 1 {
		t.Fatalf("disabled skip count = %d, want 1", got)
	}
}

func TestBuildReloadDiscoveryReportSkippedReasons(t *testing.T) {
	cfg := reloadDiscoveryConfig("minecraft", k8sdiscovery.DefaultAnnotationPrefix)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("bad-host", "minecraft", "bad host.example.com", 25565),
			startupService("duplicate-a", "minecraft", "duplicate.example.com", 25566),
			startupService("duplicate-b", "minecraft", "Duplicate.Example.COM.", 25567),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	report, err := buildReloadDiscoveryReport(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("buildReloadDiscoveryReport: %v", err)
	}
	if got := len(report.Result.Routes); got != 0 {
		t.Fatalf("routes length = %d, want 0", got)
	}
	if got := report.Result.SkippedByReason[k8sdiscovery.ReasonInvalidHost]; got != 1 {
		t.Fatalf("invalid host skip count = %d, want 1", got)
	}
	if got := report.Result.SkippedByReason[k8sdiscovery.ReasonDuplicateHost]; got != 2 {
		t.Fatalf("duplicate host skip count = %d, want 2", got)
	}
}

func TestBuildReloadDiscoveryReportReturnsListerErrors(t *testing.T) {
	cfg := reloadDiscoveryConfig("minecraft", k8sdiscovery.DefaultAnnotationPrefix)
	deps := fakeDiscoveryDeps(t, "minecraft", nil)
	deps.newServiceLister = func(*rest.Config) (k8sdiscovery.ServiceLister, error) {
		return nil, errors.New("create lister failed")
	}

	if _, err := buildReloadDiscoveryReport(context.Background(), cfg, deps); err == nil {
		t.Fatal("buildReloadDiscoveryReport succeeded with lister creation error")
	}
}

func TestBuildReloadDiscoveryReportReturnsListErrors(t *testing.T) {
	cfg := reloadDiscoveryConfig("minecraft", k8sdiscovery.DefaultAnnotationPrefix)
	lister := &fakeStartupServiceLister{err: errors.New("list failed")}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	if _, err := buildReloadDiscoveryReport(context.Background(), cfg, deps); err == nil {
		t.Fatal("buildReloadDiscoveryReport succeeded with list error")
	}
}

func TestBuildReloadDiscoveryReportOwnsResultData(t *testing.T) {
	cfg := reloadDiscoveryConfig("minecraft", k8sdiscovery.DefaultAnnotationPrefix)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	report, err := buildReloadDiscoveryReport(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("buildReloadDiscoveryReport: %v", err)
	}
	if len(report.Result.Routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(report.Result.Routes))
	}
	report.Result.Routes[0].Host = "mutated.example.com"
	routes, err := report.routeProvider().Routes(context.Background())
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if routes[0].Host != "mutated.example.com" {
		t.Fatalf("route provider host = %q, want mutated.example.com", routes[0].Host)
	}
	routes[0].Host = "provider-mutation.example.com"
	second, err := report.routeProvider().Routes(context.Background())
	if err != nil {
		t.Fatalf("second Routes: %v", err)
	}
	if second[0].Host != "mutated.example.com" {
		t.Fatalf("route provider leaked returned slice mutation, got host %q", second[0].Host)
	}
}

func TestLoadRouteSnapshotDiscoveryDisabledDoesNotCallKubernetesDeps(t *testing.T) {
	configPath := writeMainConfig(t, `
listen: ":0"
unknownHostPolicy: deny
routes:
  - serverAddress: "static.example.com"
    backend: "static.example.com:25565"
`)
	deps := discoveryStartupDeps{
		resolveNamespace: func(string, k8sdiscovery.NamespaceResolver) (string, error) {
			t.Fatal("resolveNamespace called when discovery is disabled")
			return "", nil
		},
		inClusterConfig: func() (*rest.Config, error) {
			t.Fatal("inClusterConfig called when discovery is disabled")
			return nil, nil
		},
		newServiceLister: func(*rest.Config) (k8sdiscovery.ServiceLister, error) {
			t.Fatal("newServiceLister called when discovery is disabled")
			return nil, nil
		},
	}

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	selection, err := snapshot.Router.Select("static.example.com")
	if err != nil {
		t.Fatalf("Select static route: %v", err)
	}
	if selection.Backend != "static.example.com:25565" {
		t.Fatalf("backend = %q, want static.example.com:25565", selection.Backend)
	}
	if snapshot.DiscoveryMerge.Stats.DiscoveredRoutes != 0 {
		t.Fatalf("discovered routes = %d, want 0", snapshot.DiscoveryMerge.Stats.DiscoveredRoutes)
	}
}

func TestLoadRouteSnapshotDiscoveryEnabledExplicitNamespace(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: "minecraft"`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	if lister.namespace != "minecraft" {
		t.Fatalf("listed namespace = %q, want minecraft", lister.namespace)
	}
	selection, err := snapshot.Router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select discovered route: %v", err)
	}
	if selection.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("backend = %q, want smp.minecraft.svc.cluster.local:25565", selection.Backend)
	}
}

func TestLoadRouteSnapshotDiscoveryEnabledCurrentNamespace(t *testing.T) {
	namespacePath := writeMainFile(t, "minecraft\n")
	configPath := writeDiscoveryConfig(t, `namespace: ""`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)
	deps.namespaceResolver = k8sdiscovery.NamespaceResolver{Path: namespacePath}
	deps.resolveNamespace = k8sdiscovery.ResolveNamespace

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	if lister.namespace != "minecraft" {
		t.Fatalf("listed namespace = %q, want minecraft", lister.namespace)
	}
	if _, err := snapshot.Router.Select("smp.example.com"); err != nil {
		t.Fatalf("Select discovered route: %v", err)
	}
}

func TestLoadRouteSnapshotSkipsInvalidServiceAnnotations(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: "minecraft"`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("bad", "minecraft", "bad host.example.com", 25565),
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	if _, err := snapshot.Router.Select("smp.example.com"); err != nil {
		t.Fatalf("Select valid discovered route: %v", err)
	}
	if snapshot.DiscoveryMerge.Stats.DiscoveredRoutes != 1 {
		t.Fatalf("merged discovered routes = %d, want 1", snapshot.DiscoveryMerge.Stats.DiscoveredRoutes)
	}
}

func TestLoadRouteSnapshotDisablesDuplicateDiscoveredHosts(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: "minecraft"`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp-a", "minecraft", "smp.example.com", 25565),
			startupService("smp-b", "minecraft", "SMP.Example.COM.", 25566),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	if _, err := snapshot.Router.Select("smp.example.com"); err == nil {
		t.Fatal("duplicate discovered host was routable")
	}
	if snapshot.DiscoveryMerge.Stats.DiscoveredRoutes != 0 {
		t.Fatalf("merged discovered routes = %d, want 0", snapshot.DiscoveryMerge.Stats.DiscoveredRoutes)
	}
}

func TestLoadRouteSnapshotStaticRouteWinsOverDiscoveredRoute(t *testing.T) {
	configPath := writeMainConfig(t, `
listen: ":0"
discovery:
  kubernetes:
    enabled: true
    namespace: "minecraft"
unknownHostPolicy: deny
routes:
  - serverAddress: "smp.example.com"
    backend: "static.example.com:25565"
`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	selection, err := snapshot.Router.Select("smp.example.com")
	if err != nil {
		t.Fatalf("Select static route: %v", err)
	}
	if selection.Backend != "static.example.com:25565" {
		t.Fatalf("backend = %q, want static.example.com:25565", selection.Backend)
	}
}

func TestLoadRouteSnapshotDefaultRouteRemainsOutsideExplicitRoutes(t *testing.T) {
	configPath := writeMainConfig(t, `
listen: ":0"
discovery:
  kubernetes:
    enabled: true
    namespace: "minecraft"
unknownHostPolicy: default
defaultRoute:
  backend: "default.example.com:25565"
  mode: "allow"
`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("smp", "minecraft", "smp.example.com", 25565),
		},
	}
	deps := fakeDiscoveryDeps(t, "minecraft", lister)

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	if len(snapshot.Config.Routes) != 1 {
		t.Fatalf("explicit routes = %d, want only discovered route", len(snapshot.Config.Routes))
	}
	selection, err := snapshot.Router.Select("unknown.example.com")
	if err != nil {
		t.Fatalf("Select default route: %v", err)
	}
	if selection.Backend != "default.example.com:25565" {
		t.Fatalf("default backend = %q, want default.example.com:25565", selection.Backend)
	}
}

func TestLoadRouteSnapshotNamespaceResolutionFailure(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: ""`)
	deps := fakeDiscoveryDeps(t, "", &fakeStartupServiceLister{})
	deps.resolveNamespace = func(string, k8sdiscovery.NamespaceResolver) (string, error) {
		return "", errors.New("namespace unavailable")
	}

	_, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err == nil {
		t.Fatal("expected namespace resolution error")
	}
	if !strings.Contains(err.Error(), "resolve Kubernetes discovery namespace") {
		t.Fatalf("error = %v, want namespace context", err)
	}
}

func TestLoadRouteSnapshotInClusterConfigFailure(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: "minecraft"`)
	deps := fakeDiscoveryDeps(t, "minecraft", &fakeStartupServiceLister{})
	deps.inClusterConfig = func() (*rest.Config, error) {
		return nil, errors.New("not in cluster")
	}

	_, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err == nil {
		t.Fatal("expected in-cluster config error")
	}
	if !strings.Contains(err.Error(), "create in-cluster Kubernetes config") {
		t.Fatalf("error = %v, want in-cluster config context", err)
	}
}

func TestLoadRouteSnapshotInitialListFailure(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: "minecraft"`)
	deps := fakeDiscoveryDeps(t, "minecraft", &fakeStartupServiceLister{err: errors.New("list failed")})

	_, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err == nil {
		t.Fatal("expected initial list error")
	}
	if !strings.Contains(err.Error(), "list Kubernetes Services") {
		t.Fatalf("error = %v, want list context", err)
	}
}

func TestLoadRouteSnapshotInvalidStaticConfigDoesNotCallDiscovery(t *testing.T) {
	configPath := writeMainConfig(t, `
listen: ""
discovery:
  kubernetes:
    enabled: true
    namespace: "minecraft"
unknownHostPolicy: deny
`)
	deps := fakeDiscoveryDeps(t, "minecraft", &fakeStartupServiceLister{})
	deps.inClusterConfig = func() (*rest.Config, error) {
		t.Fatal("inClusterConfig called for invalid config")
		return nil, nil
	}

	_, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err == nil {
		t.Fatal("expected config validation error")
	}
}

func TestLoadRouteSnapshotExternalNameServiceIsSkipped(t *testing.T) {
	configPath := writeDiscoveryConfig(t, `namespace: "minecraft"`)
	deps := fakeDiscoveryDeps(t, "minecraft", nil)
	deps.newServiceLister = func(*rest.Config) (k8sdiscovery.ServiceLister, error) {
		client := fake.NewSimpleClientset(
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "smp",
					Namespace: "minecraft",
					Annotations: map[string]string{
						k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationEnabled: "true",
						k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationHost:    "smp.example.com",
						k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationPort:    "25565",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Name: "minecraft", Port: 25565}},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "external",
					Namespace: "minecraft",
					Annotations: map[string]string{
						k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationEnabled: "true",
						k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationHost:    "external.example.com",
						k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationPort:    "25565",
					},
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeExternalName,
				},
			},
		)
		return k8sdiscovery.NewClientServiceLister(client), nil
	}

	snapshot, err := loadRouteSnapshotWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadRouteSnapshotWithDiscovery: %v", err)
	}
	if _, err := snapshot.Router.Select("smp.example.com"); err != nil {
		t.Fatalf("Select normal Service route: %v", err)
	}
	if _, err := snapshot.Router.Select("external.example.com"); err == nil {
		t.Fatal("ExternalName Service produced a route")
	}
}

func TestStartupDiscoveryRecordsSkippedServiceMetrics(t *testing.T) {
	configPath := writeMainConfig(t, `
listen: ":0"
metrics:
  enabled: true
discovery:
  kubernetes:
    enabled: true
    namespace: "minecraft"
unknownHostPolicy: deny
`)
	lister := &fakeStartupServiceLister{
		services: []k8sdiscovery.ServiceInput{
			startupService("valid", "minecraft", "valid.example.com", 25565),
			startupService("duplicate-a", "minecraft", "dup.example.com", 25565),
			startupService("duplicate-b", "minecraft", "DUP.Example.COM.", 25566),
			startupService("bad", "minecraft", "bad host.example.com", 25565),
		},
	}
	startup, err := loadStartupWithDiscovery(context.Background(), configPath, discardLogger(), fakeDiscoveryDeps(t, "minecraft", lister))
	if err != nil {
		t.Fatalf("loadStartupWithDiscovery: %v", err)
	}
	server := proxy.NewServerFromSnapshot(startup.Snapshot, discardLogger())

	recordStartupDiscoveryMetrics(server.Metrics(), startup.DiscoveryReport)

	waitMainMetricValue(t, server.Metrics(), "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonInvalidHost}, 1)
	waitMainMetricValue(t, server.Metrics(), "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonDuplicateHost}, 2)
	waitMainMetricValue(t, server.Metrics(), "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonPortNotFound}, 0)
}

func TestStartupDiscoverySkippedMetricsDisabledDiscoveryDoesNotRecord(t *testing.T) {
	configPath := writeMainConfig(t, `
listen: ":0"
metrics:
  enabled: true
unknownHostPolicy: deny
`)
	deps := discoveryStartupDeps{
		resolveNamespace: func(string, k8sdiscovery.NamespaceResolver) (string, error) {
			t.Fatal("resolveNamespace called when discovery is disabled")
			return "", nil
		},
	}
	startup, err := loadStartupWithDiscovery(context.Background(), configPath, discardLogger(), deps)
	if err != nil {
		t.Fatalf("loadStartupWithDiscovery: %v", err)
	}
	server := proxy.NewServerFromSnapshot(startup.Snapshot, discardLogger())

	recordStartupDiscoveryMetrics(server.Metrics(), startup.DiscoveryReport)

	if _, ok := mainMetricValue(t, server.Metrics(), "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonInvalidHost}); ok {
		t.Fatal("startup skipped Service metric was recorded when discovery is disabled")
	}
}

func TestStartKubernetesRuntimeDiscoveryDisabledDoesNotCallDeps(t *testing.T) {
	cfg := config.Defaults()
	updater := newRecordingDiscoveredRouteUpdater()
	deps := discoveryRuntimeDeps{
		resolveNamespace: func(string, k8sdiscovery.NamespaceResolver) (string, error) {
			t.Fatal("resolveNamespace called when discovery is disabled")
			return "", nil
		},
		inClusterConfig: func() (*rest.Config, error) {
			t.Fatal("inClusterConfig called when discovery is disabled")
			return nil, nil
		},
	}

	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(context.Background(), cfg, updater, discardLogger(), deps)
	if err != nil {
		t.Fatalf("startKubernetesRuntimeDiscovery: %v", err)
	}
	if runtimeDiscovery != nil {
		t.Fatal("runtime discovery started when discovery is disabled")
	}
	if updater.updated() {
		t.Fatal("updater was called when discovery is disabled")
	}
}

func TestStartKubernetesRuntimeDiscoveryWatchFailureReturnsStartupError(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	client := fake.NewSimpleClientset()
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.New("watch failed")
	})
	deps := fakeRuntimeDiscoveryDeps(t, "minecraft", client)

	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(context.Background(), cfg, newRecordingDiscoveredRouteUpdater(), discardLogger(), deps)
	if err == nil {
		t.Fatal("startKubernetesRuntimeDiscovery error = nil, want watch error")
	}
	if runtimeDiscovery != nil {
		t.Fatal("runtime discovery returned on watch startup failure")
	}
}

func TestStartKubernetesRuntimeDiscoveryNamespaceFailureRecordsMetric(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	updater := newRecordingDiscoveredRouteUpdater()
	updater.metrics = gatewaymetrics.NewRecorder(true)
	deps := fakeRuntimeDiscoveryDeps(t, "minecraft", fake.NewSimpleClientset())
	deps.resolveNamespace = func(string, k8sdiscovery.NamespaceResolver) (string, error) {
		return "", errors.New("namespace unavailable")
	}

	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(context.Background(), cfg, updater, discardLogger(), deps)
	if err == nil {
		t.Fatal("startKubernetesRuntimeDiscovery error = nil, want namespace error")
	}
	if runtimeDiscovery != nil {
		t.Fatal("runtime discovery returned on namespace failure")
	}
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_discovery_errors_total", map[string]string{"reason": "namespace_resolution_failed"}, 1)
}

func TestStartKubernetesRuntimeDiscoveryWatchSetupFailureRecordsMetric(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	client := fake.NewSimpleClientset()
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.New("watch failed")
	})
	deps := fakeRuntimeDiscoveryDeps(t, "minecraft", client)
	updater := newRecordingDiscoveredRouteUpdater()
	updater.metrics = gatewaymetrics.NewRecorder(true)

	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(context.Background(), cfg, updater, discardLogger(), deps)
	if err == nil {
		t.Fatal("startKubernetesRuntimeDiscovery error = nil, want watch setup error")
	}
	if runtimeDiscovery != nil {
		t.Fatal("runtime discovery returned on watch setup failure")
	}
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_discovery_errors_total", map[string]string{"reason": "watch_setup_failed"}, 1)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_watch_running", nil, 0)
}

func TestStartKubernetesRuntimeDiscoveryInitialListFailureRecordsMetric(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	client := fake.NewSimpleClientset()
	client.Fake.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("list failed")
	})
	deps := fakeRuntimeDiscoveryDeps(t, "minecraft", client)
	updater := newRecordingDiscoveredRouteUpdater()
	updater.metrics = gatewaymetrics.NewRecorder(true)

	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(context.Background(), cfg, updater, discardLogger(), deps)
	if err == nil {
		t.Fatal("startKubernetesRuntimeDiscovery error = nil, want list error")
	}
	if runtimeDiscovery != nil {
		t.Fatal("runtime discovery returned on list failure")
	}
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_discovery_errors_total", map[string]string{"reason": "initial_list_failed"}, 1)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_watch_running", nil, 0)
}

func TestKubernetesDiscoveryReasonClassification(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		restartReason string
		errorReason   string
	}{
		{
			name:          "watch error",
			err:           k8sdiscovery.ErrServiceWatchError,
			restartReason: "watch_error",
			errorReason:   "watch_error",
		},
		{
			name:          "watch closed",
			err:           k8sdiscovery.ErrServiceWatchClosed,
			restartReason: "watch_closed",
			errorReason:   "watch_closed",
		},
		{
			name:          "list failed",
			err:           k8sdiscovery.ErrServiceListFailed,
			restartReason: "list_failed",
			errorReason:   "initial_list_failed",
		},
		{
			name:          "watch setup failed",
			err:           k8sdiscovery.ErrServiceWatchSetupFailed,
			restartReason: "watch_setup_failed",
			errorReason:   "watch_setup_failed",
		},
		{
			name:          "unknown",
			err:           errors.New("other"),
			restartReason: "unknown",
			errorReason:   "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kubernetesWatchRestartReason(tc.err); got != tc.restartReason {
				t.Fatalf("restart reason = %q, want %q", got, tc.restartReason)
			}
			if got := kubernetesDiscoveryErrorReason(tc.err); got != tc.errorReason {
				t.Fatalf("error reason = %q, want %q", got, tc.errorReason)
			}
		})
	}
}

func TestKubernetesRuntimeDiscoveryUpdatesCompleteRouteSet(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	client := fake.NewSimpleClientset()
	updater := newRecordingDiscoveredRouteUpdater()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(ctx, cfg, updater, discardLogger(), fakeRuntimeDiscoveryDeps(t, "minecraft", client))
	if err != nil {
		t.Fatalf("startKubernetesRuntimeDiscovery: %v", err)
	}
	defer runtimeDiscovery.Stop()
	waitForDiscoveredHosts(t, updater, nil)

	if _, err := client.CoreV1().Services("minecraft").Create(context.Background(), runtimeAnnotatedService("smp", "minecraft", "smp.example.com", 25565), metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create Service: %v", err)
	}
	waitForDiscoveredHosts(t, updater, []string{"smp.example.com"})

	updated := runtimeAnnotatedService("smp", "minecraft", "new.example.com", 25565)
	if _, err := client.CoreV1().Services("minecraft").Update(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update Service: %v", err)
	}
	waitForDiscoveredHosts(t, updater, []string{"new.example.com"})

	if err := client.CoreV1().Services("minecraft").Delete(context.Background(), "smp", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete Service: %v", err)
	}
	waitForDiscoveredHosts(t, updater, nil)
}

func TestKubernetesRuntimeDiscoverySkipsInvalidAndDuplicateServices(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	client := fake.NewSimpleClientset(
		runtimeAnnotatedService("valid", "minecraft", "valid.example.com", 25565),
		runtimeAnnotatedService("duplicate-a", "minecraft", "dup.example.com", 25565),
		runtimeAnnotatedService("duplicate-b", "minecraft", "DUP.Example.COM.", 25566),
		runtimeAnnotatedService("bad", "minecraft", "bad host.example.com", 25565),
	)
	updater := newRecordingDiscoveredRouteUpdater()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(ctx, cfg, updater, discardLogger(), fakeRuntimeDiscoveryDeps(t, "minecraft", client))
	if err != nil {
		t.Fatalf("startKubernetesRuntimeDiscovery: %v", err)
	}
	defer runtimeDiscovery.Stop()

	waitForDiscoveredHosts(t, updater, []string{"valid.example.com"})
}

func TestKubernetesRuntimeDiscoveryMetricsRecordSkippedServices(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	cfg.Metrics.Enabled = true
	client := fake.NewSimpleClientset(
		runtimeAnnotatedService("valid", "minecraft", "valid.example.com", 25565),
		runtimeAnnotatedService("duplicate-a", "minecraft", "dup.example.com", 25565),
		runtimeAnnotatedService("duplicate-b", "minecraft", "DUP.Example.COM.", 25566),
		runtimeAnnotatedService("bad", "minecraft", "bad host.example.com", 25565),
	)
	updater := newRecordingDiscoveredRouteUpdater()
	updater.metrics = gatewaymetrics.NewRecorder(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(ctx, cfg, updater, discardLogger(), fakeRuntimeDiscoveryDeps(t, "minecraft", client))
	if err != nil {
		t.Fatalf("startKubernetesRuntimeDiscovery: %v", err)
	}
	defer runtimeDiscovery.Stop()

	waitForDiscoveredHosts(t, updater, []string{"valid.example.com"})
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonInvalidHost}, 1)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonDuplicateHost}, 2)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonPortNotFound}, 0)
}

func TestKubernetesRuntimeDiscoverySkippedMetricsPreservedOnUpdateFailure(t *testing.T) {
	updater := newRecordingDiscoveredRouteUpdater()
	updater.metrics = gatewaymetrics.NewRecorder(true)
	sink := newRuntimeDiscoverySink(context.Background(), updater, discardLogger(), updater.metrics)

	sink.UpdateResult(k8sdiscovery.Result{
		Routes: []k8sdiscovery.DiscoveredRoute{
			{Host: "valid.example.com", Backend: "valid.minecraft.svc.cluster.local:25565"},
		},
		SkippedByReason: map[string]int{
			k8sdiscovery.ReasonInvalidHost:   1,
			k8sdiscovery.ReasonDuplicateHost: 2,
		},
	})
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonInvalidHost}, 1)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonDuplicateHost}, 2)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonPortNotFound}, 0)

	updater.err = errors.New("update failed")
	sink.UpdateResult(k8sdiscovery.Result{
		SkippedByReason: map[string]int{
			k8sdiscovery.ReasonPortNotFound: 4,
		},
	})

	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonInvalidHost}, 1)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonDuplicateHost}, 2)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_skipped_services", map[string]string{"reason": k8sdiscovery.ReasonPortNotFound}, 0)
	if got := discoveredHosts(updater.snapshot()); strings.Join(got, ",") != "valid.example.com" {
		t.Fatalf("discovered hosts after failed update = %v, want [valid.example.com]", got)
	}
}

func TestKubernetesRuntimeDiscoveryRetriesWatchFailureAndKeepsLastKnownGood(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	client := fake.NewSimpleClientset(runtimeAnnotatedService("old", "minecraft", "old.example.com", 25565))
	watchers := make(chan *watch.FakeWatcher, 2)
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		watcher := watch.NewFake()
		watchers <- watcher
		return true, watcher, nil
	})
	sleeper := newBlockingRuntimeSleeper()
	deps := fakeRuntimeDiscoveryDeps(t, "minecraft", client)
	deps.newWatchSupervisor = func(options k8sdiscovery.WatchSupervisorOptions) (serviceWatchController, error) {
		options.Sleeper = sleeper
		options.Backoff = k8sdiscovery.BackoffPolicy{
			InitialDelay: time.Second,
			MaxDelay:     time.Second,
			Factor:       2,
		}
		return k8sdiscovery.NewWatchSupervisor(k8sdiscovery.WatchSupervisorOptions{
			Runner:  options.Runner,
			Ready:   options.Ready,
			Synced:  options.Synced,
			Logger:  options.Logger,
			OnRetry: options.OnRetry,
			Sleeper: options.Sleeper,
			Backoff: options.Backoff,
		})
	}
	updater := newRecordingDiscoveredRouteUpdater()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(ctx, cfg, updater, discardLogger(), deps)
	if err != nil {
		t.Fatalf("startKubernetesRuntimeDiscovery: %v", err)
	}
	defer runtimeDiscovery.Stop()

	firstWatcher := waitForRuntimeWatcher(t, watchers)
	waitForDiscoveredHosts(t, updater, []string{"old.example.com"})

	firstWatcher.Error(&metav1.Status{
		Status:  metav1.StatusFailure,
		Message: "watch failed",
		Reason:  metav1.StatusReasonInternalError,
		Code:    500,
	})
	if delay := sleeper.waitDelay(t); delay != time.Second {
		t.Fatalf("retry delay = %v, want 1s", delay)
	}
	waitForDiscoveredHosts(t, updater, []string{"old.example.com"})

	if err := client.CoreV1().Services("minecraft").Delete(context.Background(), "old", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete old Service: %v", err)
	}
	if _, err := client.CoreV1().Services("minecraft").Create(context.Background(), runtimeAnnotatedService("new", "minecraft", "new.example.com", 25565), metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create new Service: %v", err)
	}
	sleeper.release()
	_ = waitForRuntimeWatcher(t, watchers)
	waitForDiscoveredHosts(t, updater, []string{"new.example.com"})
}

func TestKubernetesRuntimeDiscoveryMetricsRecordWatchRetryAndRunningState(t *testing.T) {
	cfg := runtimeDiscoveryConfig()
	cfg.Metrics.Enabled = true
	client := fake.NewSimpleClientset(runtimeAnnotatedService("old", "minecraft", "old.example.com", 25565))
	watchers := make(chan *watch.FakeWatcher, 2)
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		watcher := watch.NewFake()
		watchers <- watcher
		return true, watcher, nil
	})
	sleeper := newBlockingRuntimeSleeper()
	deps := fakeRuntimeDiscoveryDeps(t, "minecraft", client)
	deps.newWatchSupervisor = func(options k8sdiscovery.WatchSupervisorOptions) (serviceWatchController, error) {
		options.Sleeper = sleeper
		options.Backoff = k8sdiscovery.BackoffPolicy{InitialDelay: time.Second}
		return k8sdiscovery.NewWatchSupervisor(options)
	}
	updater := newRecordingDiscoveredRouteUpdater()
	updater.metrics = gatewaymetrics.NewRecorder(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeDiscovery, err := startKubernetesRuntimeDiscovery(ctx, cfg, updater, discardLogger(), deps)
	if err != nil {
		t.Fatalf("startKubernetesRuntimeDiscovery: %v", err)
	}

	firstWatcher := waitForRuntimeWatcher(t, watchers)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_watch_running", nil, 1)

	firstWatcher.Error(&metav1.Status{
		Status:  metav1.StatusFailure,
		Message: "watch failed",
		Reason:  metav1.StatusReasonInternalError,
		Code:    500,
	})
	_ = sleeper.waitDelay(t)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_watch_restarts_total", map[string]string{"reason": "watch_error"}, 1)
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_discovery_errors_total", map[string]string{"reason": "watch_error"}, 1)

	cancel()
	runtimeDiscovery.Stop()
	waitMainMetricValue(t, updater.metrics, "mc_gateway_kubernetes_watch_running", nil, 0)
}

type fakeStartupServiceLister struct {
	namespace string
	services  []k8sdiscovery.ServiceInput
	err       error
}

func (l *fakeStartupServiceLister) ListServices(ctx context.Context, namespace string) ([]k8sdiscovery.ServiceInput, error) {
	l.namespace = namespace
	if l.err != nil {
		return nil, l.err
	}
	return append([]k8sdiscovery.ServiceInput(nil), l.services...), nil
}

func fakeDiscoveryDeps(t *testing.T, wantNamespace string, lister k8sdiscovery.ServiceLister) discoveryStartupDeps {
	t.Helper()
	return discoveryStartupDeps{
		resolveNamespace: func(configured string, resolver k8sdiscovery.NamespaceResolver) (string, error) {
			if configured != wantNamespace {
				t.Fatalf("configured namespace = %q, want %q", configured, wantNamespace)
			}
			return configured, nil
		},
		inClusterConfig: func() (*rest.Config, error) {
			return &rest.Config{}, nil
		},
		newServiceLister: func(*rest.Config) (k8sdiscovery.ServiceLister, error) {
			if lister == nil {
				t.Fatal("newServiceLister called without a lister")
			}
			return lister, nil
		},
	}
}

func reloadDiscoveryConfig(namespace string, annotationPrefix string) config.Config {
	return config.Config{
		Discovery: config.Discovery{
			Kubernetes: config.KubernetesDiscovery{
				Enabled:          true,
				Namespace:        namespace,
				Mode:             config.KubernetesDiscoveryModeAnnotations,
				AnnotationPrefix: annotationPrefix,
			},
		},
	}
}

func startupService(name string, namespace string, host string, port int) k8sdiscovery.ServiceInput {
	return reloadServiceWithPrefix(name, namespace, host, port, k8sdiscovery.DefaultAnnotationPrefix)
}

func reloadServiceWithPrefix(name string, namespace string, host string, port int, annotationPrefix string) k8sdiscovery.ServiceInput {
	return k8sdiscovery.ServiceInput{
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			annotationPrefix + "/" + k8sdiscovery.AnnotationEnabled: "true",
			annotationPrefix + "/" + k8sdiscovery.AnnotationHost:    host,
			annotationPrefix + "/" + k8sdiscovery.AnnotationPort:    strconv.Itoa(port),
		},
		Ports: []k8sdiscovery.ServicePort{{Name: "minecraft", Port: port}},
	}
}

type recordingDiscoveredRouteUpdater struct {
	mu      sync.RWMutex
	routes  []k8sdiscovery.DiscoveredRoute
	updates int
	err     error
	metrics *gatewaymetrics.Recorder
}

func newRecordingDiscoveredRouteUpdater() *recordingDiscoveredRouteUpdater {
	return &recordingDiscoveredRouteUpdater{}
}

func (u *recordingDiscoveredRouteUpdater) UpdateDiscoveredRoutes(ctx context.Context, provider discovery.RouteProvider) error {
	var routes []k8sdiscovery.DiscoveredRoute
	if provider != nil {
		var err error
		routes, err = provider.Routes(ctx)
		if err != nil {
			return err
		}
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return u.err
	}
	u.routes = append([]k8sdiscovery.DiscoveredRoute(nil), routes...)
	u.updates++
	return nil
}

func (u *recordingDiscoveredRouteUpdater) snapshot() []k8sdiscovery.DiscoveredRoute {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return append([]k8sdiscovery.DiscoveredRoute(nil), u.routes...)
}

func (u *recordingDiscoveredRouteUpdater) updated() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.updates > 0
}

func (u *recordingDiscoveredRouteUpdater) Metrics() *gatewaymetrics.Recorder {
	return u.metrics
}

func waitForDiscoveredHosts(t *testing.T, updater *recordingDiscoveredRouteUpdater, want []string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if updater.updated() && strings.Join(discoveredHosts(updater.snapshot()), ",") == strings.Join(sortedStrings(want), ",") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("discovered hosts = %v, want %v", discoveredHosts(updater.snapshot()), sortedStrings(want))
}

func discoveredHosts(routes []k8sdiscovery.DiscoveredRoute) []string {
	hosts := make([]string, 0, len(routes))
	for _, route := range routes {
		hosts = append(hosts, route.Host)
	}
	return sortedStrings(hosts)
}

func sortedStrings(values []string) []string {
	res := append([]string(nil), values...)
	sort.Strings(res)
	return res
}

func runtimeDiscoveryConfig() config.Config {
	cfg := config.Defaults()
	cfg.Listen = ":0"
	cfg.Discovery.Kubernetes.Enabled = true
	cfg.Discovery.Kubernetes.Namespace = "minecraft"
	return cfg
}

func fakeRuntimeDiscoveryDeps(t *testing.T, wantNamespace string, client *fake.Clientset) discoveryRuntimeDeps {
	t.Helper()
	return discoveryRuntimeDeps{
		resolveNamespace: func(configured string, resolver k8sdiscovery.NamespaceResolver) (string, error) {
			if configured != wantNamespace {
				t.Fatalf("configured namespace = %q, want %q", configured, wantNamespace)
			}
			return configured, nil
		},
		inClusterConfig: func() (*rest.Config, error) {
			return &rest.Config{}, nil
		},
		newKubernetesClient: func(*rest.Config) (k8sclient.Interface, error) {
			return client, nil
		},
	}
}

func waitForRuntimeWatcher(t *testing.T, watchers <-chan *watch.FakeWatcher) *watch.FakeWatcher {
	t.Helper()
	select {
	case watcher := <-watchers:
		return watcher
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Service watch")
		return nil
	}
}

type blockingRuntimeSleeper struct {
	delays    chan time.Duration
	releaseCh chan struct{}
}

func newBlockingRuntimeSleeper() *blockingRuntimeSleeper {
	return &blockingRuntimeSleeper{
		delays:    make(chan time.Duration, 1),
		releaseCh: make(chan struct{}, 1),
	}
}

func (s *blockingRuntimeSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	select {
	case s.delays <- delay:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.releaseCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *blockingRuntimeSleeper) waitDelay(t *testing.T) time.Duration {
	t.Helper()
	select {
	case delay := <-s.delays:
		return delay
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry delay")
		return 0
	}
}

func (s *blockingRuntimeSleeper) release() {
	s.releaseCh <- struct{}{}
}

func waitMainMetricValue(t *testing.T, recorder *gatewaymetrics.Recorder, name string, labels map[string]string, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := mainMetricValue(t, recorder, name, labels)
		if ok && got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, ok := mainMetricValue(t, recorder, name, labels)
	t.Fatalf("metric %s%v = %v (present %v), want %v", name, labels, got, ok, want)
}

func mainMetricValue(t *testing.T, recorder *gatewaymetrics.Recorder, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	if recorder == nil || recorder.Registry() == nil {
		return 0, false
	}
	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !mainMetricLabelsMatch(metric, labels) {
				continue
			}
			if metric.Gauge != nil {
				return metric.Gauge.GetValue(), true
			}
			if metric.Counter != nil {
				return metric.Counter.GetValue(), true
			}
		}
	}
	return 0, false
}

func mainMetricLabelsMatch(metric *dto.Metric, labels map[string]string) bool {
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

func runtimeAnnotatedService(name, namespace, host string, port int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationEnabled: "true",
				k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationHost:    host,
				k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationPort:    strconv.Itoa(port),
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: "minecraft", Port: int32(port)}},
		},
	}
}

func writeDiscoveryConfig(t *testing.T, namespaceLine string) string {
	t.Helper()
	return writeMainConfig(t, `
listen: ":0"
discovery:
  kubernetes:
    enabled: true
    `+namespaceLine+`
unknownHostPolicy: deny
`)
}

func writeMainConfig(t *testing.T, content string) string {
	t.Helper()
	return writeMainFile(t, strings.TrimSpace(content)+"\n")
}

func writeMainFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
