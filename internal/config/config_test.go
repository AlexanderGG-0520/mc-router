package config

import "testing"

func TestLoadValidConfig(t *testing.T) {
	cfg, err := Load([]byte(`
listen: ":25565"
handshakeTimeout: "3s"
backendDialTimeout: "2s"
unknownHostPolicy: "default"
defaultRoute:
  backend: "lobby.default.svc.cluster.local:25565"
  mode: "allow"
routes:
  - serverAddress: "SMP.Example.COM"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Listen != ":25565" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	if cfg.HandshakeTimeout.Duration.String() != "3s" {
		t.Fatalf("handshake timeout = %s", cfg.HandshakeTimeout.Duration)
	}
	if len(cfg.Routes) != 1 {
		t.Fatalf("routes length = %d", len(cfg.Routes))
	}
}

func TestLoadUsesDefaultListenWhenOmitted(t *testing.T) {
	cfg, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Listen != ":25565" {
		t.Fatalf("listen = %q, want default :25565", cfg.Listen)
	}
	if cfg.Metrics.Enabled {
		t.Fatal("metrics enabled by default")
	}
	if cfg.Metrics.Listen != ":9090" {
		t.Fatalf("metrics listen = %q, want :9090", cfg.Metrics.Listen)
	}
	if cfg.Metrics.Path != "/metrics" {
		t.Fatalf("metrics path = %q, want /metrics", cfg.Metrics.Path)
	}
}

func TestLoadAcceptsMetricsConfig(t *testing.T) {
	cfg, err := Load([]byte(`
metrics:
  enabled: true
  listen: "127.0.0.1:9091"
  path: "/prometheus"
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Metrics.Enabled {
		t.Fatal("metrics disabled")
	}
	if cfg.Metrics.Listen != "127.0.0.1:9091" {
		t.Fatalf("metrics listen = %q", cfg.Metrics.Listen)
	}
	if cfg.Metrics.Path != "/prometheus" {
		t.Fatalf("metrics path = %q", cfg.Metrics.Path)
	}
}

func TestLoadRejectsInvalidMetricsPath(t *testing.T) {
	_, err := Load([]byte(`
metrics:
  enabled: true
  path: "metrics"
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid metrics path error")
	}
}

func TestLoadRejectsExplicitEmptyListen(t *testing.T) {
	_, err := Load([]byte(`
listen: ""
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected empty listen error")
	}
}

func TestLoadRejectsUnknownPolicyWithoutDefault(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "default"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadRejectsDuplicateRoute(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
  - serverAddress: "SMP.EXAMPLE.COM."
    backend: "smp2.default.svc.cluster.local:25565"
`))
	if err == nil {
		t.Fatal("expected duplicate route error")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "deny"
unexpectedField: true
`))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestLoadRejectsInvalidPolicy(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "open"
`))
	if err == nil {
		t.Fatal("expected invalid policy error")
	}
}

func TestLoadRejectsInvalidBackend(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:not-a-port"
`))
	if err == nil {
		t.Fatal("expected invalid backend error")
	}
}

func TestLoadRejectsInvalidServerAddress(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "bad host.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err == nil {
		t.Fatal("expected invalid server address error")
	}
}

func TestLoadRejectsTooLongServerAddress(t *testing.T) {
	tooLong := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com"
	tooLong += ".bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.example.com"
	tooLong += ".cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.example.com"
	_, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "` + tooLong + `"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err == nil {
		t.Fatal("expected too-long server address error")
	}
}
