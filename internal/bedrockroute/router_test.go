package bedrockroute

import "testing"

func TestRouterMatchesHostsCaseInsensitively(t *testing.T) {
	router, err := NewRouter("hub:19132", []Route{
		{Name: "creative", Hosts: []string{"Creative.Play.Example.COM"}, Backend: "creative:19132"},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	selection := router.Select("creative.play.example.com:19132")
	if !selection.Matched {
		t.Fatal("route did not match")
	}
	if selection.RouteName != "creative" {
		t.Fatalf("route name = %q, want creative", selection.RouteName)
	}
	if selection.Backend != "creative:19132" {
		t.Fatalf("backend = %q, want creative:19132", selection.Backend)
	}
}

func TestRouterNormalizesHostPort(t *testing.T) {
	router, err := NewRouter("hub:19132", []Route{
		{Name: "survival", Hosts: []string{"survival.play.example.com:19133"}, Backend: "survival:19132"},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	selection := router.Select("survival.play.example.com:19132")
	if !selection.Matched || selection.Backend != "survival:19132" {
		t.Fatalf("selection = %#v, want survival route", selection)
	}
}

func TestRouterFallsBackForUnknownHost(t *testing.T) {
	router, err := NewRouter("hub:19132", []Route{
		{Name: "creative", Hosts: []string{"creative.play.example.com"}, Backend: "creative:19132"},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	selection := router.Select("unknown.play.example.com:19132")
	if selection.Matched {
		t.Fatal("unknown host unexpectedly matched")
	}
	if selection.RouteName != "default" || selection.Backend != "hub:19132" {
		t.Fatalf("selection = %#v, want default hub", selection)
	}
}

func TestRouterRejectsDuplicateHosts(t *testing.T) {
	_, err := NewRouter("hub:19132", []Route{
		{Name: "creative", Hosts: []string{"play.example.com"}, Backend: "creative:19132"},
		{Name: "survival", Hosts: []string{"PLAY.EXAMPLE.COM:19132"}, Backend: "survival:19132"},
	})
	if err == nil {
		t.Fatal("expected duplicate host error")
	}
}
