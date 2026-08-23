package discovery

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/AlexanderGG-0520/mc-router/internal/config"
	"github.com/AlexanderGG-0520/mc-router/internal/discovery/kubernetes"
)

func TestMergeRoutesStaticRoutesOnly(t *testing.T) {
	result := MergeRoutes([]config.Route{
		{ServerAddress: "SMP.Example.COM.", Backend: "smp-static.example.com:25565"},
	}, nil, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "smp.example.com", Backend: "smp-static.example.com:25565"},
	})
	if len(result.Ignored) != 0 {
		t.Fatalf("ignored length = %d, want 0", len(result.Ignored))
	}
}

func TestMergeRoutesDoesNotMutateStaticRouteAliases(t *testing.T) {
	routes := []config.Route{{
		ServerAddress: "play.example.com",
		Aliases:       []string{"LOCALHOST."},
		Backend:       "static.example.com:25565",
	}}

	result := MergeRoutes(routes, nil, MergeOptions{})

	if got := routes[0].Aliases[0]; got != "LOCALHOST." {
		t.Fatalf("input alias = %q, want LOCALHOST.", got)
	}
	if got := result.Routes[0].Aliases[0]; got != "localhost" {
		t.Fatalf("merged alias = %q, want localhost", got)
	}
}

func TestMergeRoutesDiscoveredRoutesOnly(t *testing.T) {
	result := MergeRoutes(nil, []kubernetes.DiscoveredRoute{
		discoveredRoute("smp.example.com", "smp", "minecraft", 25565),
		discoveredRoute("lobby.example.com", "lobby", "minecraft", 25566),
	}, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "lobby.example.com", Backend: "lobby.minecraft.svc.cluster.local:25566"},
		{ServerAddress: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
	if len(result.Ignored) != 0 {
		t.Fatalf("ignored length = %d, want 0", len(result.Ignored))
	}
}

func TestMergeRoutesMergesStaticAndDiscoveredRoutes(t *testing.T) {
	result := MergeRoutes([]config.Route{
		{ServerAddress: "static.example.com", Backend: "static.example.com:25565"},
	}, []kubernetes.DiscoveredRoute{
		discoveredRoute("smp.example.com", "smp", "minecraft", 25565),
	}, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
		{ServerAddress: "static.example.com", Backend: "static.example.com:25565"},
	})
	if result.Stats.StaticRoutes != 1 || result.Stats.DiscoveredRoutes != 1 || result.Stats.MergedRoutes != 2 {
		t.Fatalf("stats = %#v, want static=1 discovered=1 merged=2", result.Stats)
	}
}

func TestMergeRoutesStaticRouteWinsOnSameHost(t *testing.T) {
	result := MergeRoutes([]config.Route{
		{ServerAddress: "SMP.Example.COM.", Backend: "static.example.com:25565"},
	}, []kubernetes.DiscoveredRoute{
		discoveredRoute("smp.example.com", "smp", "minecraft", 25565),
	}, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "smp.example.com", Backend: "static.example.com:25565"},
	})
	assertIgnoredReasonCount(t, result, ReasonStaticRoutePrecedence, 1)
	assertIgnored(t, result.Ignored[0], "smp.example.com", "smp.minecraft.svc.cluster.local:25565", ReasonStaticRoutePrecedence)
}

func TestMergeRoutesStaticAliasWinsOverDiscoveredHost(t *testing.T) {
	result := MergeRoutes([]config.Route{
		{ServerAddress: "play.example.com", Aliases: []string{"LOCALHOST."}, Backend: "static.example.com:25565"},
	}, []kubernetes.DiscoveredRoute{
		discoveredRoute("localhost", "smp", "minecraft", 25565),
	}, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "play.example.com", Aliases: []string{"localhost"}, Backend: "static.example.com:25565"},
	})
	if got := result.Routes[0].Source; got != RouteSourceStatic {
		t.Fatalf("route source = %q, want %q", got, RouteSourceStatic)
	}
	assertIgnoredReasonCount(t, result, ReasonStaticRoutePrecedence, 1)
}

func TestMergeRoutesIgnoresDuplicateDiscoveredHost(t *testing.T) {
	result := MergeRoutes([]config.Route{
		{ServerAddress: "lobby.example.com", Backend: "static.example.com:25565"},
	}, []kubernetes.DiscoveredRoute{
		discoveredRoute("SMP.Example.COM.", "smp-a", "minecraft", 25565),
		discoveredRoute("smp.example.com", "smp-b", "minecraft", 25566),
	}, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "lobby.example.com", Backend: "static.example.com:25565"},
	})
	assertIgnoredReasonCount(t, result, ReasonDuplicateDiscovered, 2)
	for _, ignored := range result.Ignored {
		if ignored.Reason != ReasonDuplicateDiscovered {
			t.Fatalf("ignored reason = %q, want %q", ignored.Reason, ReasonDuplicateDiscovered)
		}
		if ignored.Host != "smp.example.com" {
			t.Fatalf("ignored host = %q, want smp.example.com", ignored.Host)
		}
	}
}

func TestMergeRoutesIgnoresInvalidDiscoveredRoute(t *testing.T) {
	result := MergeRoutes(nil, []kubernetes.DiscoveredRoute{
		{Host: "https://bad.example.com/path", Backend: "smp.minecraft.svc.cluster.local:25565"},
		{Host: "bad-backend.example.com", Backend: "backend.example.com:25565"},
		{Host: "bad-port.example.com", Backend: "smp.minecraft.svc.cluster.local:70000"},
	}, MergeOptions{})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	assertIgnoredReasonCount(t, result, ReasonInvalidDiscovered, 3)
}

func TestMergeRoutesDoesNotInsertDefaultRouteIntoRouteList(t *testing.T) {
	result := MergeRoutes(nil, nil, MergeOptions{
		DefaultRoute: config.DefaultRoute{Backend: "default.example.com:25565", Mode: config.RouteModeAllow},
	})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	if !result.Stats.HasDefaultRoute {
		t.Fatal("HasDefaultRoute = false, want true")
	}
}

func TestMergeRoutesReturnsDeterministicOutputOrder(t *testing.T) {
	staticRoutes := []config.Route{
		{ServerAddress: "zeta.example.com", Backend: "zeta.example.com:25565"},
		{ServerAddress: "alpha.example.com", Backend: "alpha.example.com:25565"},
	}
	discovered := []kubernetes.DiscoveredRoute{
		discoveredRoute("dup.example.com", "dup-a", "minecraft", 25565),
		discoveredRoute("beta.example.com", "beta", "minecraft", 25566),
		discoveredRoute("Dup.Example.COM.", "dup-b", "minecraft", 25567),
		{Host: "invalid.example.com", Backend: "not-a-backend"},
	}

	first := MergeRoutes(staticRoutes, discovered, MergeOptions{})
	second := MergeRoutes([]config.Route{
		staticRoutes[1],
		staticRoutes[0],
	}, []kubernetes.DiscoveredRoute{
		discovered[3],
		discovered[2],
		discovered[1],
		discovered[0],
	}, MergeOptions{})

	assertRoutes(t, first.Routes, second.Routes)
	assertIgnoredEqual(t, first.Ignored, second.Ignored)
}

func TestMergeRoutesNormalizesDiscoveredHostAndBackend(t *testing.T) {
	result := MergeRoutes(nil, []kubernetes.DiscoveredRoute{
		{Host: "SMP.Example.COM.", Backend: "SMP.Minecraft.SVC.Cluster.Local.:25565"},
	}, MergeOptions{})

	assertRoutes(t, result.Routes, []config.Route{
		{ServerAddress: "smp.example.com", Backend: "smp.minecraft.svc.cluster.local:25565"},
	})
}

func TestMergeRoutesEmptyInputs(t *testing.T) {
	result := MergeRoutes(nil, nil, MergeOptions{})

	if len(result.Routes) != 0 {
		t.Fatalf("routes length = %d, want 0", len(result.Routes))
	}
	if len(result.Ignored) != 0 {
		t.Fatalf("ignored length = %d, want 0", len(result.Ignored))
	}
	if len(result.Stats.IgnoredByReason) != 0 {
		t.Fatalf("ignored reason count = %d, want 0", len(result.Stats.IgnoredByReason))
	}
}

func TestMergeRoutesDoesNotPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("MergeRoutes panicked: %v", recovered)
		}
	}()

	result := MergeRoutes([]config.Route{{}}, []kubernetes.DiscoveredRoute{{}}, MergeOptions{})
	if len(result.Routes) != 1 {
		t.Fatalf("routes length = %d, want 1", len(result.Routes))
	}
	assertIgnoredReasonCount(t, result, ReasonInvalidDiscovered, 1)
}

func discoveredRoute(host, service, namespace string, port int) kubernetes.DiscoveredRoute {
	return kubernetes.DiscoveredRoute{
		Host:    host,
		Backend: service + "." + namespace + ".svc.cluster.local:" + strconv.Itoa(port),
	}
}

func assertRoutes(t *testing.T, got, want []config.Route) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("routes length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range got {
		gotRoute, wantRoute := got[i], want[i]
		gotRoute.Source = ""
		wantRoute.Source = ""
		if !reflect.DeepEqual(gotRoute, wantRoute) {
			t.Fatalf("routes[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func assertIgnored(t *testing.T, got IgnoredDiscoveredRoute, host, backend, reason string) {
	t.Helper()
	if got.Host != host || got.Backend != backend || got.Reason != reason {
		t.Fatalf("ignored = %#v, want host=%q backend=%q reason=%q", got, host, backend, reason)
	}
}

func assertIgnoredReasonCount(t *testing.T, result MergeResult, reason string, count int) {
	t.Helper()
	if result.Stats.IgnoredByReason[reason] != count {
		t.Fatalf("ignored reason %q count = %d, want %d", reason, result.Stats.IgnoredByReason[reason], count)
	}
}

func assertIgnoredEqual(t *testing.T, got, want []IgnoredDiscoveredRoute) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ignored length = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Host != want[i].Host ||
			got[i].Backend != want[i].Backend ||
			got[i].Reason != want[i].Reason ||
			errorString(got[i].Err) != errorString(want[i].Err) {
			t.Fatalf("ignored[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
