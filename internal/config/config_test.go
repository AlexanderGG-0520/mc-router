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
	if cfg.Discovery.Kubernetes.Enabled {
		t.Fatal("kubernetes discovery enabled by default")
	}
	if cfg.Discovery.Kubernetes.Namespace != "" {
		t.Fatalf("kubernetes discovery namespace = %q, want empty", cfg.Discovery.Kubernetes.Namespace)
	}
	if cfg.Discovery.Kubernetes.Mode != KubernetesDiscoveryModeAnnotations {
		t.Fatalf("kubernetes discovery mode = %q, want %q", cfg.Discovery.Kubernetes.Mode, KubernetesDiscoveryModeAnnotations)
	}
	if cfg.Discovery.Kubernetes.AnnotationPrefix != DefaultKubernetesAnnotationPrefix {
		t.Fatalf("kubernetes discovery annotation prefix = %q, want %q", cfg.Discovery.Kubernetes.AnnotationPrefix, DefaultKubernetesAnnotationPrefix)
	}
	if cfg.Fallback.Enabled {
		t.Fatal("fallback enabled by default")
	}
	if cfg.Fallback.Status.Enabled {
		t.Fatal("fallback status enabled by default")
	}
	if cfg.Fallback.Status.MOTD != "Server unavailable" {
		t.Fatalf("fallback status motd = %q", cfg.Fallback.Status.MOTD)
	}
	if cfg.Fallback.Status.ProtocolName != "mc-gateway" {
		t.Fatalf("fallback status protocol name = %q", cfg.Fallback.Status.ProtocolName)
	}
	if cfg.Fallback.Status.ProtocolVersion != 767 {
		t.Fatalf("fallback status protocol version = %d", cfg.Fallback.Status.ProtocolVersion)
	}
	if cfg.Fallback.Status.RespondOnRouteDenied == nil || !*cfg.Fallback.Status.RespondOnRouteDenied {
		t.Fatal("fallback status route denied response disabled by default")
	}
	if cfg.Fallback.Status.RespondOnBackendFailure {
		t.Fatal("fallback status backend failure response enabled by default")
	}
	if cfg.Fallback.Login.Enabled {
		t.Fatal("fallback login enabled by default")
	}
	if cfg.Fallback.Login.Message != "Server unavailable. Please try again later." {
		t.Fatalf("fallback login message = %q", cfg.Fallback.Login.Message)
	}
	if cfg.Fallback.Login.RespondOnRouteDenied == nil || !*cfg.Fallback.Login.RespondOnRouteDenied {
		t.Fatal("fallback login route denied response disabled by default")
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

func TestLoadAcceptsKubernetesDiscoveryConfig(t *testing.T) {
	cfg, err := Load([]byte(`
discovery:
  kubernetes:
    enabled: true
    namespace: "minecraft"
    mode: "service-annotations"
    annotationPrefix: "mc-router.alexandergg.com"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Discovery.Kubernetes.Enabled {
		t.Fatal("kubernetes discovery disabled")
	}
	if cfg.Discovery.Kubernetes.Namespace != "minecraft" {
		t.Fatalf("kubernetes discovery namespace = %q", cfg.Discovery.Kubernetes.Namespace)
	}
}

func TestLoadRejectsUnknownKubernetesDiscoveryMode(t *testing.T) {
	_, err := Load([]byte(`
discovery:
  kubernetes:
    mode: "endpointslices"
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid kubernetes discovery mode error")
	}
}

func TestLoadRejectsEmptyKubernetesAnnotationPrefix(t *testing.T) {
	_, err := Load([]byte(`
discovery:
  kubernetes:
    annotationPrefix: ""
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected empty kubernetes annotation prefix error")
	}
}

func TestLoadRejectsKubernetesAnnotationPrefixWithSlash(t *testing.T) {
	_, err := Load([]byte(`
discovery:
  kubernetes:
    annotationPrefix: "mc-router.alexandergg.com/"
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected kubernetes annotation prefix slash error")
	}
}

func TestLoadRejectsNonCanonicalKubernetesAnnotationPrefix(t *testing.T) {
	_, err := Load([]byte(`
discovery:
  kubernetes:
    annotationPrefix: "MC-Router.alexandergg.com."
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected non-canonical kubernetes annotation prefix error")
	}
}

func TestLoadRejectsInvalidKubernetesNamespace(t *testing.T) {
	_, err := Load([]byte(`
discovery:
  kubernetes:
    namespace: "bad/namespace"
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid kubernetes namespace error")
	}
}

func TestLoadAcceptsEmptyKubernetesNamespace(t *testing.T) {
	cfg, err := Load([]byte(`
discovery:
  kubernetes:
    enabled: true
    namespace: ""
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Discovery.Kubernetes.Namespace != "" {
		t.Fatalf("kubernetes discovery namespace = %q, want empty", cfg.Discovery.Kubernetes.Namespace)
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

func TestLoadAcceptsFallbackStatusConfig(t *testing.T) {
	cfg, err := Load([]byte(`
fallback:
  enabled: true
  status:
    enabled: true
    motd: "Maintenance"
    protocolName: "mc-gateway"
    protocolVersion: 767
    maxPlayers: 10
    onlinePlayers: 2
    respondOnBackendFailure: true
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Fallback.Enabled || !cfg.Fallback.Status.Enabled {
		t.Fatal("fallback status disabled")
	}
	if cfg.Fallback.Status.MOTD != "Maintenance" {
		t.Fatalf("fallback status motd = %q", cfg.Fallback.Status.MOTD)
	}
	if cfg.Fallback.Status.MaxPlayers != 10 {
		t.Fatalf("fallback max players = %d", cfg.Fallback.Status.MaxPlayers)
	}
	if cfg.Fallback.Status.OnlinePlayers != 2 {
		t.Fatalf("fallback online players = %d", cfg.Fallback.Status.OnlinePlayers)
	}
	if cfg.Fallback.Status.RespondOnRouteDenied == nil || !*cfg.Fallback.Status.RespondOnRouteDenied {
		t.Fatal("fallback route denied response disabled")
	}
	if !cfg.Fallback.Status.RespondOnBackendFailure {
		t.Fatal("fallback backend failure response disabled")
	}
}

func TestLoadAcceptsExplicitFallbackRouteDeniedDisable(t *testing.T) {
	cfg, err := Load([]byte(`
fallback:
  enabled: true
  status:
    enabled: true
    respondOnRouteDenied: false
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Fallback.Status.RespondOnRouteDenied == nil {
		t.Fatal("fallback route denied response is nil")
	}
	if *cfg.Fallback.Status.RespondOnRouteDenied {
		t.Fatal("fallback route denied response enabled")
	}
}

func TestLoadAcceptsFallbackLoginConfig(t *testing.T) {
	cfg, err := Load([]byte(`
fallback:
  enabled: true
  login:
    enabled: true
    respondOnRouteDenied: false
    message: "Try again later"
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Fallback.Enabled || !cfg.Fallback.Login.Enabled {
		t.Fatal("fallback login disabled")
	}
	if cfg.Fallback.Login.Message != "Try again later" {
		t.Fatalf("fallback login message = %q", cfg.Fallback.Login.Message)
	}
	if cfg.Fallback.Login.RespondOnRouteDenied == nil {
		t.Fatal("fallback login route denied response is nil")
	}
	if *cfg.Fallback.Login.RespondOnRouteDenied {
		t.Fatal("fallback login route denied response enabled")
	}
}

func TestLoadRejectsInvalidFallbackLoginConfig(t *testing.T) {
	_, err := Load([]byte(`
fallback:
  enabled: true
  login:
    enabled: true
    message: ""
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
`))
	if err == nil {
		t.Fatal("expected invalid fallback login config error")
	}
}

func TestLoadRejectsInvalidFallbackStatusConfig(t *testing.T) {
	_, err := Load([]byte(`
fallback:
  enabled: true
  status:
    enabled: true
    motd: "Maintenance"
    protocolName: "mc-gateway"
    protocolVersion: 767
    maxPlayers: -1
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid fallback status config error")
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
