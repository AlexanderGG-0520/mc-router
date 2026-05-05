package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	k8sdiscovery "github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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

func startupService(name string, namespace string, host string, port int) k8sdiscovery.ServiceInput {
	return k8sdiscovery.ServiceInput{
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationEnabled: "true",
			k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationHost:    host,
			k8sdiscovery.DefaultAnnotationPrefix + "/" + k8sdiscovery.AnnotationPort:    strconv.Itoa(port),
		},
		Ports: []k8sdiscovery.ServicePort{{Name: "minecraft", Port: port}},
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
