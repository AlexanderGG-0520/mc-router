package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestServiceWatchControllerInitialObjects(t *testing.T) {
	client := fake.NewSimpleClientset(annotatedService("smp", "minecraft", "smp.example.com"))
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, []string{"smp.example.com"})
}

func TestServiceWatchControllerAddServiceUpdatesSink(t *testing.T) {
	client := fake.NewSimpleClientset()
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, nil)

	if _, err := client.CoreV1().Services("minecraft").Create(context.Background(), annotatedService("smp", "minecraft", "smp.example.com"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create Service error: %v", err)
	}

	waitForRouteHosts(t, sink, []string{"smp.example.com"})
}

func TestServiceWatchControllerUpdateServiceUpdatesSink(t *testing.T) {
	svc := annotatedService("smp", "minecraft", "old.example.com")
	client := fake.NewSimpleClientset(svc)
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, []string{"old.example.com"})

	updated := svc.DeepCopy()
	updated.Annotations[DefaultAnnotationPrefix+"/host"] = "new.example.com"
	if _, err := client.CoreV1().Services("minecraft").Update(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("Update Service error: %v", err)
	}

	waitForRouteHosts(t, sink, []string{"new.example.com"})
}

func TestServiceWatchControllerDeleteServiceRemovesRoute(t *testing.T) {
	client := fake.NewSimpleClientset(annotatedService("smp", "minecraft", "smp.example.com"))
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, []string{"smp.example.com"})

	if err := client.CoreV1().Services("minecraft").Delete(context.Background(), "smp", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete Service error: %v", err)
	}

	waitForRouteHosts(t, sink, nil)
}

func TestServiceWatchControllerSkipsInvalidAndKeepsValid(t *testing.T) {
	client := fake.NewSimpleClientset(
		annotatedService("smp", "minecraft", "smp.example.com"),
		invalidAnnotatedService("bad", "minecraft"),
	)
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, []string{"smp.example.com"})
}

func TestServiceWatchControllerDuplicateHostDisablesHost(t *testing.T) {
	client := fake.NewSimpleClientset(
		annotatedService("smp-a", "minecraft", "smp.example.com"),
		annotatedService("smp-b", "minecraft", "smp.example.com"),
	)
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, nil)
}

func TestServiceWatchControllerSkipsExternalNameService(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external",
			Namespace: "minecraft",
			Annotations: map[string]string{
				DefaultAnnotationPrefix + "/enabled": "true",
				DefaultAnnotationPrefix + "/host":    "external.example.com",
				DefaultAnnotationPrefix + "/port":    "25565",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeExternalName,
			Ports: []corev1.ServicePort{
				{Name: "minecraft", Port: 25565},
			},
		},
	})
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForRouteHosts(t, sink, nil)
}

func TestServiceWatchControllerContextCancellationStopsController(t *testing.T) {
	client := fake.NewSimpleClientset()
	sink := newRecordingRouteSink()
	cancel, done := startServiceWatchController(t, client, "minecraft", sink)

	waitForRouteHosts(t, sink, nil)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not stop after context cancellation")
	}
}

func TestServiceWatchControllerListFailureReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.Fake.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("list failed")
	})
	sink := newRecordingRouteSink()

	controller, err := NewServiceWatchController(client, "minecraft", sink, ServiceWatchControllerOptions{})
	if err != nil {
		t.Fatalf("NewServiceWatchController error: %v", err)
	}

	err = controller.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
}

func TestServiceWatchControllerWatchFailureReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.New("watch failed")
	})
	sink := newRecordingRouteSink()

	controller, err := NewServiceWatchController(client, "minecraft", sink, ServiceWatchControllerOptions{})
	if err != nil {
		t.Fatalf("NewServiceWatchController error: %v", err)
	}

	err = controller.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
}

func TestNewServiceWatchControllerRejectsAllNamespacesSentinel(t *testing.T) {
	client := fake.NewSimpleClientset()
	sink := newRecordingRouteSink()

	_, err := NewServiceWatchController(client, "", sink, ServiceWatchControllerOptions{})
	if err == nil {
		t.Fatal("NewServiceWatchController() error = nil, want error")
	}
}

func TestServiceWatchControllerRejectsNilSink(t *testing.T) {
	client := fake.NewSimpleClientset()

	_, err := NewServiceWatchController(client, "minecraft", nil, ServiceWatchControllerOptions{})
	if err == nil {
		t.Fatal("NewServiceWatchController() error = nil, want error")
	}
}

func startServiceWatchController(t *testing.T, client *fake.Clientset, namespace string, sink *recordingRouteSink) (context.CancelFunc, <-chan error) {
	t.Helper()

	controller, err := NewServiceWatchController(client, namespace, sink, ServiceWatchControllerOptions{})
	if err != nil {
		t.Fatalf("NewServiceWatchController error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- controller.Run(ctx)
	}()
	return cancel, done
}

func stopServiceWatchController(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not stop")
	}
}

func waitForRouteHosts(t *testing.T, sink *recordingRouteSink, want []string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.updated() && reflect.DeepEqual(routeHosts(sink.snapshot()), sortedStrings(want)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("route hosts = %v, want %v", routeHosts(sink.snapshot()), sortedStrings(want))
}

type recordingRouteSink struct {
	mu      sync.RWMutex
	routes  []DiscoveredRoute
	updates int
}

func newRecordingRouteSink() *recordingRouteSink {
	return &recordingRouteSink{}
}

func (s *recordingRouteSink) Update(routes []DiscoveredRoute) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append([]DiscoveredRoute(nil), routes...)
	s.updates++
}

func (s *recordingRouteSink) snapshot() []DiscoveredRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]DiscoveredRoute(nil), s.routes...)
}

func (s *recordingRouteSink) updated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updates > 0
}

func annotatedService(name, namespace, host string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				DefaultAnnotationPrefix + "/enabled": "true",
				DefaultAnnotationPrefix + "/host":    host,
				DefaultAnnotationPrefix + "/port":    "25565",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "minecraft", Port: 25565},
			},
		},
	}
}

func invalidAnnotatedService(name, namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				DefaultAnnotationPrefix + "/enabled": "true",
				DefaultAnnotationPrefix + "/host":    "bad.example.com",
				DefaultAnnotationPrefix + "/port":    "25566",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "minecraft", Port: 25565},
			},
		},
	}
}

func routeHosts(routes []DiscoveredRoute) []string {
	hosts := make([]string, 0, len(routes))
	for _, route := range routes {
		hosts = append(hosts, route.Host)
	}
	sort.Strings(hosts)
	return hosts
}

func sortedStrings(values []string) []string {
	res := make([]string, len(values))
	copy(res, values)
	sort.Strings(res)
	return res
}
