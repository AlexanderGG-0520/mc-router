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
	if cfg.UDPRelay.Enabled {
		t.Fatal("udp relay enabled by default")
	}
	if cfg.UDPRelay.Listen != ":24454" {
		t.Fatalf("udp relay listen = %q, want :24454", cfg.UDPRelay.Listen)
	}
	if cfg.UDPRelay.Backend != "127.0.0.1:24454" {
		t.Fatalf("udp relay backend = %q, want 127.0.0.1:24454", cfg.UDPRelay.Backend)
	}
	if cfg.UDPRelay.IdleTimeout.Duration.String() != "30s" {
		t.Fatalf("udp relay idle timeout = %s, want 30s", cfg.UDPRelay.IdleTimeout.Duration)
	}
	if cfg.UDPRelay.BackendDialTimeout.Duration.String() != "5s" {
		t.Fatalf("udp relay backend dial timeout = %s, want 5s", cfg.UDPRelay.BackendDialTimeout.Duration)
	}
	if cfg.UDPRelay.MaxSessions != 4096 {
		t.Fatalf("udp relay max sessions = %d, want 4096", cfg.UDPRelay.MaxSessions)
	}
	if cfg.UDPRelay.MaxPacketSize != MaxUDPRelayPacketSize {
		t.Fatalf("udp relay max packet size = %d, want %d", cfg.UDPRelay.MaxPacketSize, MaxUDPRelayPacketSize)
	}
	if cfg.VoiceChat.Enabled {
		t.Fatal("voicechat enabled by default")
	}
	if cfg.VoiceChat.Listen != ":24454" {
		t.Fatalf("voicechat listen = %q, want :24454", cfg.VoiceChat.Listen)
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

func TestLoadAcceptsVoiceChatConfig(t *testing.T) {
	t.Setenv("VOICECHAT_TOKEN_HUB", "hub-secret")
	t.Setenv("VOICECHAT_TOKEN_SURVIVAL", "survival-secret")
	cfg, err := Load([]byte(`
voiceChat:
  enabled: true
  listen: "127.0.0.1:24454"
  registration:
    listen: "127.0.0.1:9091"
    registrationTTL: "45s"
    requestTimeout: "2s"
    maxRegistrations: 128
  session:
    idleTimeout: "40s"
    backendDialTimeout: "3s"
    maxSessions: 256
    maxPacketSize: 1200
  backends:
    hub:
      udp: "hub:24454"
      tokenEnv: "VOICECHAT_TOKEN_HUB"
    survival:
      udp: "survival:24454"
      tokenEnv: "VOICECHAT_TOKEN_SURVIVAL"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.VoiceChat.Enabled {
		t.Fatal("voicechat disabled")
	}
	if cfg.VoiceChat.Backends["hub"].Token() != "hub-secret" {
		t.Fatal("hub token was not loaded from environment")
	}
	if cfg.VoiceChat.Backends["survival"].Token() != "survival-secret" {
		t.Fatal("survival token was not loaded from environment")
	}
}

func TestLoadRejectsInvalidVoiceChatConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		env  map[string]string
	}{
		{name: "empty listen", body: `listen: ""`},
		{name: "invalid registration listen", body: `registration: { listen: "bad address" }`},
		{name: "zero registration ttl", body: `registration: { registrationTTL: "0s" }`},
		{name: "zero request timeout", body: `registration: { requestTimeout: "0s" }`},
		{name: "zero registration limit", body: `registration: { maxRegistrations: 0 }`},
		{name: "zero session idle timeout", body: `session: { idleTimeout: "0s" }`},
		{name: "zero backend dial timeout", body: `session: { backendDialTimeout: "0s" }`},
		{name: "zero max sessions", body: `session: { maxSessions: 0 }`},
		{name: "too large packet size", body: `session: { maxPacketSize: 65536 }`},
		{name: "empty backend map", body: `backends: {}`},
		{name: "invalid backend udp", body: `backends: { hub: { udp: "hub:not-a-port", tokenEnv: "VOICECHAT_TOKEN_HUB" } }`},
		{name: "missing token env", body: `backends: { hub: { udp: "hub:24454", tokenEnv: "VOICECHAT_TOKEN_MISSING" } }`},
		{name: "duplicate token", body: `
backends:
  hub: { udp: "hub:24454", tokenEnv: "VOICECHAT_TOKEN_HUB" }
  survival: { udp: "survival:24454", tokenEnv: "VOICECHAT_TOKEN_SURVIVAL" }
`, env: map[string]string{"VOICECHAT_TOKEN_HUB": "same-token", "VOICECHAT_TOKEN_SURVIVAL": "same-token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VOICECHAT_TOKEN_HUB", "hub-secret")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load([]byte(`
voiceChat:
  enabled: true
  listen: "127.0.0.1:24454"
  registration:
    listen: "127.0.0.1:9091"
    registrationTTL: "30s"
    requestTimeout: "5s"
    maxRegistrations: 4096
  session:
    idleTimeout: "30s"
    backendDialTimeout: "5s"
    maxSessions: 4096
    maxPacketSize: 65535
  backends:
    hub:
      udp: "hub:24454"
      tokenEnv: "VOICECHAT_TOKEN_HUB"
  ` + tt.body + `
unknownHostPolicy: "deny"
`))
			if err == nil {
				t.Fatal("expected invalid voicechat config error")
			}
		})
	}
}

func TestLoadRejectsFixedAndDynamicVoiceChatListenConflict(t *testing.T) {
	t.Setenv("VOICECHAT_TOKEN_HUB", "hub-secret")
	_, err := Load([]byte(`
udpRelay:
  enabled: true
  listen: "127.0.0.1:24454"
  backend: "hub:24454"
voiceChat:
  enabled: true
  listen: "127.0.0.1:24454"
  backends:
    hub:
      udp: "hub:24454"
      tokenEnv: "VOICECHAT_TOKEN_HUB"
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected fixed and dynamic voicechat conflict")
	}
}

func TestLoadAcceptsUDPRelayConfig(t *testing.T) {
	cfg, err := Load([]byte(`
udpRelay:
  enabled: true
  listen: "127.0.0.1:24454"
  backend: "hub:24454"
  idleTimeout: "45s"
  backendDialTimeout: "3s"
  maxSessions: 128
  maxPacketSize: 1200
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.UDPRelay.Enabled {
		t.Fatal("udp relay disabled")
	}
	if cfg.UDPRelay.Listen != "127.0.0.1:24454" {
		t.Fatalf("udp relay listen = %q", cfg.UDPRelay.Listen)
	}
	if cfg.UDPRelay.Backend != "hub:24454" {
		t.Fatalf("udp relay backend = %q", cfg.UDPRelay.Backend)
	}
	if cfg.UDPRelay.IdleTimeout.Duration.String() != "45s" {
		t.Fatalf("udp relay idle timeout = %s", cfg.UDPRelay.IdleTimeout.Duration)
	}
	if cfg.UDPRelay.BackendDialTimeout.Duration.String() != "3s" {
		t.Fatalf("udp relay backend dial timeout = %s", cfg.UDPRelay.BackendDialTimeout.Duration)
	}
	if cfg.UDPRelay.MaxSessions != 128 {
		t.Fatalf("udp relay max sessions = %d", cfg.UDPRelay.MaxSessions)
	}
	if cfg.UDPRelay.MaxPacketSize != 1200 {
		t.Fatalf("udp relay max packet size = %d", cfg.UDPRelay.MaxPacketSize)
	}
}

func TestLoadIgnoresInvalidUDPRelayFieldsWhenDisabled(t *testing.T) {
	_, err := Load([]byte(`
udpRelay:
  enabled: false
  listen: ""
  backend: ""
  idleTimeout: "0s"
  backendDialTimeout: "0s"
  maxSessions: 0
  maxPacketSize: 0
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error for disabled UDP relay: %v", err)
	}
}

func TestLoadRejectsInvalidEnabledUDPRelayConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "empty listen",
			body: `listen: ""`,
		},
		{
			name: "invalid listen",
			body: `listen: "bad address"`,
		},
		{
			name: "empty backend",
			body: `backend: ""`,
		},
		{
			name: "invalid backend port",
			body: `backend: "hub:not-a-port"`,
		},
		{
			name: "unspecified backend",
			body: `backend: "0.0.0.0:24454"`,
		},
		{
			name: "multicast backend",
			body: `backend: "224.0.0.1:24454"`,
		},
		{
			name: "broadcast backend",
			body: `backend: "255.255.255.255:24454"`,
		},
		{
			name: "zero idle timeout",
			body: `idleTimeout: "0s"`,
		},
		{
			name: "too large idle timeout",
			body: `idleTimeout: "25h"`,
		},
		{
			name: "zero backend dial timeout",
			body: `backendDialTimeout: "0s"`,
		},
		{
			name: "zero max sessions",
			body: `maxSessions: 0`,
		},
		{
			name: "too many sessions",
			body: `maxSessions: 65537`,
		},
		{
			name: "zero packet size",
			body: `maxPacketSize: 0`,
		},
		{
			name: "too large packet size",
			body: `maxPacketSize: 65536`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(`
udpRelay:
  enabled: true
  listen: "127.0.0.1:24454"
  backend: "hub:24454"
  idleTimeout: "30s"
  backendDialTimeout: "5s"
  maxSessions: 4096
  maxPacketSize: 65535
  ` + tt.body + `
unknownHostPolicy: "deny"
`))
			if err == nil {
				t.Fatal("expected invalid UDP relay config error")
			}
		})
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
