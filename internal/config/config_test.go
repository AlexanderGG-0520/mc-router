package config

import (
	"testing"
	"time"
)

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

func TestLoadAcceptsConfigReloadWatch(t *testing.T) {
	cfg, err := Load([]byte(`
configReload:
  watch: true
  debounce: "750ms"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.ConfigReload.Watch {
		t.Fatal("config reload watch disabled")
	}
	if got := cfg.ConfigReload.Debounce.Duration; got != 750*time.Millisecond {
		t.Fatalf("config reload debounce = %s, want 750ms", got)
	}
}

func TestLoadRejectsInvalidConfigReloadDebounce(t *testing.T) {
	_, err := Load([]byte(`
configReload:
  watch: true
  debounce: "50ms"
`))
	if err == nil {
		t.Fatal("Load succeeded with a too-short config reload debounce")
	}
}

func TestLoadAcceptsRouteStatusOverride(t *testing.T) {
	cfg, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
    statusOverride:
      motd: "Alec SMP"
      protocolName: "Alec SMP 2"
      protocolVersion: 767
      maxPlayers: 100
      onlinePlayers: 12
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	override := cfg.Routes[0].StatusOverride
	if override == nil {
		t.Fatal("status override is nil")
	}
	if override.MOTD != "Alec SMP" || override.ProtocolName != "Alec SMP 2" || override.ProtocolVersion != 767 || override.MaxPlayers != 100 || override.OnlinePlayers != 12 {
		t.Fatalf("status override = %#v", override)
	}
}

func TestLoadAcceptsRouteStatusBackend(t *testing.T) {
	cfg, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "hub.default.svc.cluster.local:25565"
    statusBackend: "smp.default.svc.cluster.local:25565"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Routes[0].StatusBackend; got != "smp.default.svc.cluster.local:25565" {
		t.Fatalf("status backend = %q", got)
	}
}

func TestLoadRejectsInvalidRouteStatusBackend(t *testing.T) {
	_, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "hub.default.svc.cluster.local:25565"
    statusBackend: "not-a-host-port"
`))
	if err == nil {
		t.Fatal("expected invalid route status backend error")
	}
}

func TestLoadTreatsEmptyRouteStatusBackendAsUnset(t *testing.T) {
	cfg, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "hub.default.svc.cluster.local:25565"
    statusBackend: ""
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.Routes[0].StatusBackend; got != "" {
		t.Fatalf("status backend = %q, want empty", got)
	}
}

func TestLoadRejectsInvalidRouteStatusOverride(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty motd", body: "motd: \"\""},
		{name: "empty protocol name", body: "motd: ok\n      protocolName: \"\""},
		{name: "negative protocol version", body: "motd: ok\n      protocolName: ok\n      protocolVersion: -1"},
		{name: "negative max players", body: "motd: ok\n      protocolName: ok\n      maxPlayers: -1"},
		{name: "negative online players", body: "motd: ok\n      protocolName: ok\n      onlinePlayers: -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(`
unknownHostPolicy: "deny"
routes:
  - serverAddress: "smp.example.com"
    backend: "smp.default.svc.cluster.local:25565"
    statusOverride:
      ` + tt.body + `
`))
			if err == nil {
				t.Fatal("expected invalid route status override error")
			}
		})
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
	if cfg.Bedrock.Enabled {
		t.Fatal("bedrock enabled by default")
	}
	if cfg.Bedrock.Listen != "" {
		t.Fatalf("bedrock listen = %q, want empty", cfg.Bedrock.Listen)
	}
	if cfg.Bedrock.Mode != BedrockModeUDPForward {
		t.Fatalf("bedrock mode = %q, want %q", cfg.Bedrock.Mode, BedrockModeUDPForward)
	}
	if cfg.Bedrock.DefaultBackend != "" {
		t.Fatalf("bedrock default backend = %q, want empty", cfg.Bedrock.DefaultBackend)
	}
	if cfg.Bedrock.SessionTimeout.Duration.String() != "30s" {
		t.Fatalf("bedrock session timeout = %s, want 30s", cfg.Bedrock.SessionTimeout.Duration)
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

func TestLoadIgnoresInvalidBedrockFieldsWhenDisabled(t *testing.T) {
	_, err := Load([]byte(`
bedrock:
  enabled: false
  listen: ""
  defaultBackend: ""
  sessionTimeout: "0s"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error for disabled bedrock: %v", err)
	}
}

func TestLoadAcceptsBedrockConfig(t *testing.T) {
	cfg, err := Load([]byte(`
bedrock:
  enabled: true
  listen: "127.0.0.1:19132"
  defaultBackend: "mc-hub.mc-hub.svc.cluster.local:19132"
  sessionTimeout: "45s"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Bedrock.Enabled {
		t.Fatal("bedrock disabled")
	}
	if cfg.Bedrock.Listen != "127.0.0.1:19132" {
		t.Fatalf("bedrock listen = %q", cfg.Bedrock.Listen)
	}
	if cfg.Bedrock.DefaultBackend != "mc-hub.mc-hub.svc.cluster.local:19132" {
		t.Fatalf("bedrock default backend = %q", cfg.Bedrock.DefaultBackend)
	}
	if len(cfg.Bedrock.Routes) != 0 {
		t.Fatalf("bedrock routes length = %d, want 0", len(cfg.Bedrock.Routes))
	}
	if cfg.Bedrock.SessionTimeout.Duration.String() != "45s" {
		t.Fatalf("bedrock session timeout = %s", cfg.Bedrock.SessionTimeout.Duration)
	}
}

func TestLoadAcceptsBedrockRoutes(t *testing.T) {
	cfg, err := Load([]byte(`
bedrock:
  enabled: true
  mode: "host-proxy"
  listen: "127.0.0.1:19132"
  defaultBackend: "mc-hub.mc-hub.svc.cluster.local:19132"
  sessionTimeout: "45s"
  routes:
    - name: hub
      hosts:
        - "play.example.com"
        - "hub.play.example.com:19132"
      backend: "mc-hub.mc-hub.svc.cluster.local:19132"
    - name: creative
      hosts:
        - "creative.play.example.com"
      backend: "mc-creative.mc-creative.svc.cluster.local:19132"
    - name: survival
      hosts:
        - "survival.play.example.com"
      backend: "mc-survival.mc-survival.svc.cluster.local:19132"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.Bedrock.Routes) != 3 {
		t.Fatalf("bedrock routes length = %d, want 3", len(cfg.Bedrock.Routes))
	}
	if cfg.Bedrock.Mode != BedrockModeHostProxy {
		t.Fatalf("bedrock mode = %q, want host-proxy", cfg.Bedrock.Mode)
	}
	if cfg.Bedrock.Routes[1].Name != "creative" {
		t.Fatalf("second bedrock route name = %q", cfg.Bedrock.Routes[1].Name)
	}
	if cfg.Bedrock.Routes[1].Backend != "mc-creative.mc-creative.svc.cluster.local:19132" {
		t.Fatalf("second bedrock route backend = %q", cfg.Bedrock.Routes[1].Backend)
	}
	if len(cfg.Bedrock.Routes[0].Hosts) != 2 {
		t.Fatalf("first bedrock route hosts length = %d, want 2", len(cfg.Bedrock.Routes[0].Hosts))
	}
}

func TestLoadAcceptsBedrockDefaultSessionTimeout(t *testing.T) {
	cfg, err := Load([]byte(`
bedrock:
  enabled: true
  listen: "127.0.0.1:19132"
  defaultBackend: "mc-hub.mc-hub.svc.cluster.local:19132"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Bedrock.SessionTimeout.Duration != DefaultBedrockSessionTimeout {
		t.Fatalf("bedrock session timeout = %s, want %s", cfg.Bedrock.SessionTimeout.Duration, DefaultBedrockSessionTimeout)
	}
}

func TestLoadRejectsInvalidEnabledBedrockConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty listen", body: `listen: ""`},
		{name: "invalid listen", body: `listen: "bad address"`},
		{name: "invalid mode", body: `mode: "magic"`},
		{name: "empty default backend", body: `defaultBackend: ""`},
		{name: "invalid default backend port", body: `defaultBackend: "hub:not-a-port"`},
		{name: "unspecified default backend", body: `defaultBackend: "0.0.0.0:19132"`},
		{name: "multicast default backend", body: `defaultBackend: "224.0.0.1:19132"`},
		{name: "broadcast default backend", body: `defaultBackend: "255.255.255.255:19132"`},
		{name: "empty route name", body: `routes: [{ name: "", backend: "hub:19132" }]`},
		{name: "duplicate route name", body: `
routes:
  - name: hub
    backend: "hub:19132"
  - name: hub
    backend: "other:19132"`},
		{name: "invalid route backend", body: `routes: [{ name: "hub", backend: "hub:not-a-port" }]`},
		{name: "unspecified route backend", body: `routes: [{ name: "hub", backend: "0.0.0.0:19132" }]`},
		{name: "invalid route host", body: `routes: [{ name: "hub", hosts: ["bad host.example.com"], backend: "hub:19132" }]`},
		{name: "duplicate route host", body: `
routes:
  - name: hub
    hosts: ["play.example.com"]
    backend: "hub:19132"
  - name: survival
    hosts: ["PLAY.EXAMPLE.COM:19132"]
    backend: "survival:19132"`},
		{name: "zero session timeout", body: `sessionTimeout: "0s"`},
		{name: "too large session timeout", body: `sessionTimeout: "25h"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(`
bedrock:
  enabled: true
  listen: "127.0.0.1:19132"
  defaultBackend: "mc-hub.mc-hub.svc.cluster.local:19132"
  sessionTimeout: "30s"
  ` + tt.body + `
unknownHostPolicy: "deny"
`))
			if err == nil {
				t.Fatal("expected invalid bedrock config error")
			}
		})
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

func TestLoadAcceptsClientPolicy(t *testing.T) {
	cfg, err := Load([]byte(`
clientPolicy:
  allow:
    - "203.0.113.0/24"
  deny:
    - "198.51.100.10"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.ClientPolicy.Allow) != 1 || cfg.ClientPolicy.Allow[0] != "203.0.113.0/24" {
		t.Fatalf("clientPolicy.allow = %#v", cfg.ClientPolicy.Allow)
	}
	if len(cfg.ClientPolicy.Deny) != 1 || cfg.ClientPolicy.Deny[0] != "198.51.100.10" {
		t.Fatalf("clientPolicy.deny = %#v", cfg.ClientPolicy.Deny)
	}
}

func TestLoadRejectsInvalidClientPolicyEntry(t *testing.T) {
	_, err := Load([]byte(`
clientPolicy:
  deny:
    - "not-an-address"
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid client policy entry error")
	}
}

func TestLoadAcceptsClientRateLimit(t *testing.T) {
	cfg, err := Load([]byte(`
clientRateLimit:
  enabled: true
  connectionsPerSecond: 2.5
  burst: 4
  idleTimeout: "3m"
  maxEntries: 1000
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.ClientRateLimit.Enabled || cfg.ClientRateLimit.ConnectionsPerSecond != 2.5 || cfg.ClientRateLimit.Burst != 4 || cfg.ClientRateLimit.IdleTimeout.Duration != 3*time.Minute || cfg.ClientRateLimit.MaxEntries != 1000 {
		t.Fatalf("client rate limit = %#v", cfg.ClientRateLimit)
	}
}

func TestLoadRejectsInvalidEnabledClientRateLimit(t *testing.T) {
	_, err := Load([]byte(`
clientRateLimit:
  enabled: true
  connectionsPerSecond: 0
  burst: 0
  idleTimeout: "0s"
  maxEntries: 0
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid client rate limit error")
	}
}

func TestLoadAcceptsStatusHealthThresholds(t *testing.T) {
	cfg, err := Load([]byte(`
status:
  probeInterval: "5s"
  probeTimeout: "2s"
  failureThreshold: 4
  recoveryThreshold: 3
  maxObservationAge: "7s"
unknownHostPolicy: "deny"
`))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Status.FailureThreshold != 4 || cfg.Status.RecoveryThreshold != 3 {
		t.Fatalf("status thresholds = %#v", cfg.Status)
	}
}

func TestLoadRejectsInvalidStatusHealthThresholds(t *testing.T) {
	_, err := Load([]byte(`
status:
  failureThreshold: -1
  recoveryThreshold: -1
unknownHostPolicy: "deny"
`))
	if err == nil {
		t.Fatal("expected invalid status threshold error")
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
