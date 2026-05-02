package kubernetes

import (
	"strconv"
	"testing"
)

func TestBuildDiscoveredRoutesBuildsMultipleRoutes(t *testing.T) {
	result := BuildDiscoveredRoutes([]ServiceInput{
		serviceWithHost("survival", "minecraft", "survival.example.com", 25565),
		serviceWithHost("lobby", "minecraft", "lobby.example.com", 25566),
	}, Options{})

	if len(result.Routes) != 2 {
		t.Fatalf("routes length = %d, want 2", len(result.Routes))
	}
	assertRoute(t, result.Routes[0], "lobby.example.com", "lobby.minecraft.svc.cluster.local:25566")
	assertRoute(t, result.Routes[1], "survival.example.com", "survival.minecraft.svc.cluster.local:25565")
	if len(result.Skipped) != 0 {
		t.Fatalf("skipped length = %d, want 0", len(result.Skipped))
	}
}

func TestBuildDiscoveredRoutesSkipsDisabledService(t *testing.T) {
	service := serviceWithHost("smp", "minecraft", "smp.example.com", 25565)
	delete(service.Annotations, DefaultAnnotationPrefix+"/"+AnnotationEnabled)

	result := BuildDiscoveredRoutes([]ServiceInput{service}, Options{})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	assertSkippedReasonCount(t, result, ReasonDisabled, 1)
}

func TestBuildDiscoveredRoutesSkipsInvalidHostService(t *testing.T) {
	service := serviceWithHost("smp", "minecraft", "https://smp.example.com/path", 25565)

	result := BuildDiscoveredRoutes([]ServiceInput{service}, Options{})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	assertSkippedReasonCount(t, result, ReasonInvalidHost, 1)
}

func TestBuildDiscoveredRoutesSkipsInvalidPortService(t *testing.T) {
	service := serviceWithHost("smp", "minecraft", "smp.example.com", 25565)
	service.Annotations[DefaultAnnotationPrefix+"/"+AnnotationPort] = "not-a-port"

	result := BuildDiscoveredRoutes([]ServiceInput{service}, Options{})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	assertSkippedReasonCount(t, result, ReasonInvalidPort, 1)
}

func TestBuildDiscoveredRoutesDisablesAllDuplicateHostRoutes(t *testing.T) {
	result := BuildDiscoveredRoutes([]ServiceInput{
		serviceWithHost("smp-a", "minecraft", "SMP.Example.COM", 25565),
		serviceWithHost("smp-b", "minecraft", "smp.example.com.", 25566),
		serviceWithHost("lobby", "minecraft", "lobby.example.com", 25567),
	}, Options{})

	if len(result.Routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(result.Routes))
	}
	assertRoute(t, result.Routes[0], "lobby.example.com", "lobby.minecraft.svc.cluster.local:25567")
	assertSkippedReasonCount(t, result, ReasonDuplicateHost, 2)
	if len(result.DuplicateHosts) != 1 || result.DuplicateHosts[0] != "smp.example.com" {
		t.Fatalf("duplicate hosts = %#v, want [smp.example.com]", result.DuplicateHosts)
	}
}

func TestBuildDiscoveredRoutesReturnsDeterministicOrder(t *testing.T) {
	services := []ServiceInput{
		serviceWithHost("zeta", "minecraft", "zeta.example.com", 25565),
		serviceWithHost("alpha", "minecraft", "alpha.example.com", 25566),
		serviceWithHost("dup-b", "minecraft", "dup.example.com", 25567),
		serviceWithHost("bad-port", "minecraft", "bad-port.example.com", 25568),
		serviceWithHost("dup-a", "minecraft", "Dup.Example.COM.", 25569),
	}
	services[3].Annotations[DefaultAnnotationPrefix+"/"+AnnotationPort] = "70000"

	first := BuildDiscoveredRoutes(services, Options{})
	second := BuildDiscoveredRoutes([]ServiceInput{
		services[4],
		services[3],
		services[2],
		services[1],
		services[0],
	}, Options{})

	assertRoutesEqual(t, first.Routes, second.Routes)
	assertSkippedEqual(t, first.Skipped, second.Skipped)
	assertStringsEqual(t, first.DuplicateHosts, second.DuplicateHosts)
}

func TestBuildDiscoveredRoutesUsesAnnotationPrefix(t *testing.T) {
	service := ServiceInput{
		Name:      "smp",
		Namespace: "minecraft",
		Annotations: map[string]string{
			"custom.example.com/enabled": "true",
			"custom.example.com/host":    "custom.example.com",
			"custom.example.com/port":    "25565",
		},
		Ports: []ServicePort{{Name: "minecraft", Port: 25565}},
	}

	result := BuildDiscoveredRoutes([]ServiceInput{service}, Options{AnnotationPrefix: "custom.example.com"})

	if len(result.Routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(result.Routes))
	}
	assertRoute(t, result.Routes[0], "custom.example.com", "smp.minecraft.svc.cluster.local:25565")
}

func TestBuildDiscoveredRoutesEmptyServiceList(t *testing.T) {
	result := BuildDiscoveredRoutes(nil, Options{})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("skipped length = %d, want 0", len(result.Skipped))
	}
	if len(result.DuplicateHosts) != 0 {
		t.Fatalf("duplicate hosts length = %d, want 0", len(result.DuplicateHosts))
	}
	if len(result.SkippedByReason) != 0 {
		t.Fatalf("skipped reason count = %d, want 0", len(result.SkippedByReason))
	}
}

func TestBuildDiscoveredRoutesDoesNotPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("BuildDiscoveredRoutes panicked: %v", recovered)
		}
	}()

	result := BuildDiscoveredRoutes([]ServiceInput{{}}, Options{AnnotationPrefix: ""})
	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	assertSkippedReasonCount(t, result, ReasonDisabled, 1)
}

func serviceWithHost(name, namespace, host string, port int) ServiceInput {
	return ServiceInput{
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			DefaultAnnotationPrefix + "/" + AnnotationEnabled: "true",
			DefaultAnnotationPrefix + "/" + AnnotationHost:    host,
			DefaultAnnotationPrefix + "/" + AnnotationPort:    strconv.Itoa(port),
		},
		Ports: []ServicePort{{Name: "minecraft", Port: port}},
	}
}

func assertRoute(t *testing.T, route DiscoveredRoute, host string, backend string) {
	t.Helper()
	if route.Host != host {
		t.Fatalf("host = %q, want %q", route.Host, host)
	}
	if route.Backend != backend {
		t.Fatalf("backend = %q, want %q", route.Backend, backend)
	}
}

func assertSkippedReasonCount(t *testing.T, result Result, reason string, count int) {
	t.Helper()
	if result.SkippedByReason[reason] != count {
		t.Fatalf("skipped reason %q count = %d, want %d", reason, result.SkippedByReason[reason], count)
	}
}

func assertRoutesEqual(t *testing.T, left, right []DiscoveredRoute) {
	t.Helper()
	if len(left) != len(right) {
		t.Fatalf("routes length mismatch: %d != %d", len(left), len(right))
	}
	for i := range left {
		if left[i] != right[i] {
			t.Fatalf("routes[%d] = %#v, want %#v", i, left[i], right[i])
		}
	}
}

func assertSkippedEqual(t *testing.T, left, right []SkippedResource) {
	t.Helper()
	if len(left) != len(right) {
		t.Fatalf("skipped length mismatch: %d != %d", len(left), len(right))
	}
	for i := range left {
		if left[i].ServiceName != right[i].ServiceName ||
			left[i].Namespace != right[i].Namespace ||
			left[i].Host != right[i].Host ||
			left[i].Backend != right[i].Backend ||
			left[i].Reason != right[i].Reason ||
			errorString(left[i].Err) != errorString(right[i].Err) {
			t.Fatalf("skipped[%d] = %#v, want %#v", i, left[i], right[i])
		}
	}
}

func assertStringsEqual(t *testing.T, left, right []string) {
	t.Helper()
	if len(left) != len(right) {
		t.Fatalf("string length mismatch: %d != %d", len(left), len(right))
	}
	for i := range left {
		if left[i] != right[i] {
			t.Fatalf("strings[%d] = %q, want %q", i, left[i], right[i])
		}
	}
}
