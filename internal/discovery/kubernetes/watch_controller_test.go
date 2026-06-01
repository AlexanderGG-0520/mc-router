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

func TestServiceWatchControllerPublishesCompleteSnapshotResult(t *testing.T) {
	client := fake.NewSimpleClientset(
		annotatedService("lobby", "minecraft", "lobby.example.com"),
		annotatedService("dup-a", "minecraft", "dup.example.com"),
		annotatedService("dup-b", "minecraft", "DUP.Example.COM."),
		invalidAnnotatedService("bad", "minecraft"),
	)
	sink := newRecordingResultSink()
	cancel, done := startServiceWatchControllerWithSink(t, client, "minecraft", sink)
	defer stopServiceWatchController(t, cancel, done)

	waitForResult(t, sink, func(result Result) bool {
		return len(result.Routes) == 1 &&
			routeHosts(result.Routes)[0] == "lobby.example.com" &&
			reflect.DeepEqual(result.DuplicateHosts, []string{"dup.example.com"}) &&
			result.SkippedByReason[ReasonDuplicateHost] == 2 &&
			result.SkippedByReason[ReasonPortNotFound] == 1 &&
			len(result.Skipped) == 3 &&
			result.Skipped[0].Reason == ReasonDuplicateHost &&
			result.Skipped[0].ServiceName == "dup-a" &&
			result.Skipped[1].Reason == ReasonDuplicateHost &&
			result.Skipped[1].ServiceName == "dup-b" &&
			result.Skipped[2].Reason == ReasonPortNotFound &&
			result.Skipped[2].ServiceName == "bad"
	})

	if err := client.CoreV1().Services("minecraft").Delete(context.Background(), "dup-b", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete Service error: %v", err)
	}

	waitForResult(t, sink, func(result Result) bool {
		return reflect.DeepEqual(routeHosts(result.Routes), []string{"dup.example.com", "lobby.example.com"}) &&
			len(result.DuplicateHosts) == 0 &&
			result.SkippedByReason[ReasonDuplicateHost] == 0 &&
			result.SkippedByReason[ReasonPortNotFound] == 1 &&
			len(result.Skipped) == 1 &&
			result.Skipped[0].Reason == ReasonPortNotFound &&
			result.Skipped[0].ServiceName == "bad"
	})
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

func TestServiceWatchControllerWatchErrorEventReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	watcher := watch.NewFake()
	watchStarted := make(chan struct{})
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		close(watchStarted)
		return true, watcher, nil
	})
	sink := newRecordingRouteSink()

	controller, err := NewServiceWatchController(client, "minecraft", sink, ServiceWatchControllerOptions{})
	if err != nil {
		t.Fatalf("NewServiceWatchController error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- controller.Run(ctx)
	}()

	select {
	case <-watchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch to start")
	}

	watcher.Error(&metav1.Status{
		Status:  metav1.StatusFailure,
		Message: "watch stream failed",
		Reason:  metav1.StatusReasonInternalError,
		Code:    500,
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run() error = nil, want error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return after watch.Error event")
	}
}

func TestServiceWatchControllerClosedWatchChannelReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	watcher := watch.NewFake()
	watchStarted := make(chan struct{})
	client.Fake.PrependWatchReactor("services", func(k8stesting.Action) (bool, watch.Interface, error) {
		close(watchStarted)
		return true, watcher, nil
	})
	sink := newRecordingRouteSink()

	controller, err := NewServiceWatchController(client, "minecraft", sink, ServiceWatchControllerOptions{})
	if err != nil {
		t.Fatalf("NewServiceWatchController error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- controller.Run(ctx)
	}()

	select {
	case <-watchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch to start")
	}

	watcher.Stop()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run() error = nil, want error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return after watch channel closed")
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
	return startServiceWatchControllerWithSink(t, client, namespace, sink)
}

func startServiceWatchControllerWithSink(t *testing.T, client *fake.Clientset, namespace string, sink RouteSink) (context.CancelFunc, <-chan error) {
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

func waitForResult(t *testing.T, sink *recordingResultSink, match func(Result) bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if result, ok := sink.snapshot(); ok && match(result) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, _ := sink.snapshot()
	t.Fatalf("result = %#v did not match expectation", result)
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

type recordingResultSink struct {
	mu      sync.RWMutex
	result  Result
	updates int
}

func newRecordingResultSink() *recordingResultSink {
	return &recordingResultSink{}
}

func (s *recordingResultSink) Update(routes []DiscoveredRoute) {
	s.UpdateResult(Result{Routes: append([]DiscoveredRoute(nil), routes...)})
}

func (s *recordingResultSink) UpdateResult(result Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = cloneResult(result)
	s.updates++
}

func (s *recordingResultSink) snapshot() (Result, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneResult(s.result), s.updates > 0
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

func cloneResult(result Result) Result {
	result.Routes = append([]DiscoveredRoute(nil), result.Routes...)
	result.Skipped = append([]SkippedResource(nil), result.Skipped...)
	result.DuplicateHosts = append([]string(nil), result.DuplicateHosts...)
	result.SkippedByReason = cloneStringIntMap(result.SkippedByReason)
	return result
}

func cloneStringIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
