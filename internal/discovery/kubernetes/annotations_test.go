package kubernetes

import "testing"

func TestParseServiceAnnotationsDiscoversRoute(t *testing.T) {
	result := ParseServiceAnnotations(DefaultAnnotationPrefix, validServiceInput())
	if result.Skipped {
		t.Fatalf("ParseServiceAnnotations skipped route: reason=%s err=%v", result.Reason, result.Err)
	}
	if result.Route.Host != "smp.example.com" {
		t.Fatalf("host = %q, want smp.example.com", result.Route.Host)
	}
	if result.Route.Backend != "smp.minecraft.svc.cluster.local:25565" {
		t.Fatalf("backend = %q, want smp.minecraft.svc.cluster.local:25565", result.Route.Backend)
	}
}

func TestParseServiceAnnotationsEnabledMissingSkips(t *testing.T) {
	service := validServiceInput()
	delete(service.Annotations, DefaultAnnotationPrefix+"/"+AnnotationEnabled)

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonDisabled)
}

func TestParseServiceAnnotationsEnabledFalseSkips(t *testing.T) {
	service := validServiceInput()
	service.Annotations[DefaultAnnotationPrefix+"/"+AnnotationEnabled] = "false"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonDisabled)
}

func TestParseServiceAnnotationsHostMissingSkips(t *testing.T) {
	service := validServiceInput()
	delete(service.Annotations, DefaultAnnotationPrefix+"/"+AnnotationHost)

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonMissingHost)
}

func TestParseServiceAnnotationsInvalidHostSkips(t *testing.T) {
	service := validServiceInput()
	service.Annotations[DefaultAnnotationPrefix+"/"+AnnotationHost] = "https://smp.example.com/path"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonInvalidHost)
}

func TestParseServiceAnnotationsPortMissingSkips(t *testing.T) {
	service := validServiceInput()
	delete(service.Annotations, DefaultAnnotationPrefix+"/"+AnnotationPort)

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonMissingPort)
}

func TestParseServiceAnnotationsNonNumericPortSkips(t *testing.T) {
	service := validServiceInput()
	service.Annotations[DefaultAnnotationPrefix+"/"+AnnotationPort] = "minecraft"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonInvalidPort)
}

func TestParseServiceAnnotationsPortOutOfRangeSkips(t *testing.T) {
	service := validServiceInput()
	service.Annotations[DefaultAnnotationPrefix+"/"+AnnotationPort] = "70000"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonInvalidPort)
}

func TestParseServiceAnnotationsPortNotPresentSkips(t *testing.T) {
	service := validServiceInput()
	service.Annotations[DefaultAnnotationPrefix+"/"+AnnotationPort] = "25566"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonPortNotFound)
}

func TestParseServiceAnnotationsInvalidServiceNameSkips(t *testing.T) {
	service := validServiceInput()
	service.Name = "bad/service"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonInvalidServiceName)
}

func TestParseServiceAnnotationsInvalidNamespaceSkips(t *testing.T) {
	service := validServiceInput()
	service.Namespace = "BadNamespace"

	result := ParseServiceAnnotations(DefaultAnnotationPrefix, service)
	assertSkipped(t, result, ReasonInvalidNamespace)
}

func TestParseServiceAnnotationsUsesPrefix(t *testing.T) {
	service := ServiceInput{
		Name:      "smp",
		Namespace: "minecraft",
		Annotations: map[string]string{
			"custom.example.com/enabled": "true",
			"custom.example.com/host":    "Custom.Example.COM.",
			"custom.example.com/port":    "25565",
		},
		Ports: []ServicePort{{Name: "minecraft", Port: 25565}},
	}

	result := ParseServiceAnnotations("custom.example.com", service)
	if result.Skipped {
		t.Fatalf("ParseServiceAnnotations skipped route: reason=%s err=%v", result.Reason, result.Err)
	}
	if result.Route.Host != "custom.example.com" {
		t.Fatalf("host = %q, want custom.example.com", result.Route.Host)
	}
}

func TestDropDuplicateHostsDisablesDuplicatedHost(t *testing.T) {
	routes := []DiscoveredRoute{
		{Host: "SMP.Example.COM", Backend: "smp-a.minecraft.svc.cluster.local:25565"},
		{Host: "smp.example.com.", Backend: "smp-b.minecraft.svc.cluster.local:25565"},
		{Host: "lobby.example.com", Backend: "lobby.minecraft.svc.cluster.local:25565"},
	}

	kept, skipped := DropDuplicateHosts(routes)
	if len(kept) != 1 {
		t.Fatalf("kept routes length = %d, want 1", len(kept))
	}
	if kept[0].Host != "lobby.example.com" {
		t.Fatalf("kept host = %q, want lobby.example.com", kept[0].Host)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped routes length = %d, want 2", len(skipped))
	}
	for _, skip := range skipped {
		if skip.Reason != ReasonDuplicateHost {
			t.Fatalf("skip reason = %q, want %q", skip.Reason, ReasonDuplicateHost)
		}
	}
}

func TestParseServiceAnnotationsDoesNotPanicOnInvalidInput(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ParseServiceAnnotations panicked: %v", recovered)
		}
	}()

	result := ParseServiceAnnotations("", ServiceInput{})
	if !result.Skipped {
		t.Fatal("expected invalid empty input to be skipped")
	}
}

func validServiceInput() ServiceInput {
	return ServiceInput{
		Name:      "smp",
		Namespace: "minecraft",
		Annotations: map[string]string{
			DefaultAnnotationPrefix + "/" + AnnotationEnabled: "true",
			DefaultAnnotationPrefix + "/" + AnnotationHost:    "smp.example.com",
			DefaultAnnotationPrefix + "/" + AnnotationPort:    "25565",
		},
		Ports: []ServicePort{{Name: "minecraft", Port: 25565}},
	}
}

func assertSkipped(t *testing.T, result ParseResult, reason string) {
	t.Helper()
	if !result.Skipped {
		t.Fatalf("expected skipped result")
	}
	if result.Reason != reason {
		t.Fatalf("skip reason = %q, want %q", result.Reason, reason)
	}
}
