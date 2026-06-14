package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
	"gopkg.in/yaml.v3"
)

const (
	UnknownHostDeny                    = "deny"
	UnknownHostDefault                 = "default"
	RouteModeAllow                     = "allow"
	KubernetesDiscoveryModeAnnotations = "service-annotations"
	DefaultKubernetesAnnotationPrefix  = "mc-router.alexandergg.com"
	MaxUDPRelayIdleTimeout             = 24 * time.Hour
	MaxUDPRelaySessions                = 65536
	MaxUDPRelayPacketSize              = 65535
	MaxVoiceChatRegistrations          = 65536
	DefaultBedrockSessionTimeout       = 30 * time.Second
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == 0 {
		return nil
	}
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

type Config struct {
	Listen             string       `yaml:"listen"`
	HandshakeTimeout   Duration     `yaml:"handshakeTimeout"`
	BackendDialTimeout Duration     `yaml:"backendDialTimeout"`
	Metrics            Metrics      `yaml:"metrics"`
	UDPRelay           UDPRelay     `yaml:"udpRelay"`
	Bedrock            Bedrock      `yaml:"bedrock"`
	VoiceChat          VoiceChat    `yaml:"voiceChat"`
	Fallback           Fallback     `yaml:"fallback"`
	Discovery          Discovery    `yaml:"discovery"`
	DefaultRoute       DefaultRoute `yaml:"defaultRoute"`
	Routes             []Route      `yaml:"routes"`
	UnknownHostPolicy  string       `yaml:"unknownHostPolicy"`
}

type Fallback struct {
	Enabled bool           `yaml:"enabled"`
	Login   FallbackLogin  `yaml:"login"`
	Status  FallbackStatus `yaml:"status"`
}

type FallbackLogin struct {
	Enabled              bool   `yaml:"enabled"`
	RespondOnRouteDenied *bool  `yaml:"respondOnRouteDenied"`
	Message              string `yaml:"message"`
}

type FallbackStatus struct {
	Enabled                 bool   `yaml:"enabled"`
	RespondOnRouteDenied    *bool  `yaml:"respondOnRouteDenied"`
	RespondOnBackendFailure bool   `yaml:"respondOnBackendFailure"`
	MOTD                    string `yaml:"motd"`
	ProtocolName            string `yaml:"protocolName"`
	ProtocolVersion         int    `yaml:"protocolVersion"`
	MaxPlayers              int    `yaml:"maxPlayers"`
	OnlinePlayers           int    `yaml:"onlinePlayers"`
}

type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
	Path    string `yaml:"path"`
}

type UDPRelay struct {
	Enabled            bool     `yaml:"enabled"`
	Listen             string   `yaml:"listen"`
	Backend            string   `yaml:"backend"`
	IdleTimeout        Duration `yaml:"idleTimeout"`
	BackendDialTimeout Duration `yaml:"backendDialTimeout"`
	MaxSessions        int      `yaml:"maxSessions"`
	MaxPacketSize      int      `yaml:"maxPacketSize"`
}

type Bedrock struct {
	Enabled        bool     `yaml:"enabled"`
	Listen         string   `yaml:"listen"`
	DefaultBackend string   `yaml:"defaultBackend"`
	SessionTimeout Duration `yaml:"sessionTimeout"`
}

type VoiceChat struct {
	Enabled      bool                    `yaml:"enabled"`
	Listen       string                  `yaml:"listen"`
	Registration VoiceRegistration       `yaml:"registration"`
	Session      VoiceSession            `yaml:"session"`
	Backends     map[string]VoiceBackend `yaml:"backends"`
}

type VoiceRegistration struct {
	Listen           string   `yaml:"listen"`
	RegistrationTTL  Duration `yaml:"registrationTTL"`
	RequestTimeout   Duration `yaml:"requestTimeout"`
	MaxRegistrations int      `yaml:"maxRegistrations"`
}

type VoiceSession struct {
	IdleTimeout        Duration `yaml:"idleTimeout"`
	BackendDialTimeout Duration `yaml:"backendDialTimeout"`
	MaxSessions        int      `yaml:"maxSessions"`
	MaxPacketSize      int      `yaml:"maxPacketSize"`
}

type VoiceBackend struct {
	UDP      string `yaml:"udp"`
	TokenEnv string `yaml:"tokenEnv"`
	token    string
}

func (b VoiceBackend) Token() string {
	return b.token
}

type Discovery struct {
	Kubernetes KubernetesDiscovery `yaml:"kubernetes"`
}

type KubernetesDiscovery struct {
	Enabled          bool   `yaml:"enabled"`
	Namespace        string `yaml:"namespace"`
	Mode             string `yaml:"mode"`
	AnnotationPrefix string `yaml:"annotationPrefix"`
}

type DefaultRoute struct {
	Backend string `yaml:"backend"`
	Mode    string `yaml:"mode"`
}

type Route struct {
	ServerAddress string `yaml:"serverAddress"`
	Backend       string `yaml:"backend"`
}

func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	cfg, err := Load(data)
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}
	return cfg, nil
}

func Load(data []byte) (Config, error) {
	cfg := Defaults()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse yaml: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func Defaults() Config {
	return Config{
		Listen:             ":25565",
		HandshakeTimeout:   Duration{Duration: 5 * time.Second},
		BackendDialTimeout: Duration{Duration: 5 * time.Second},
		UnknownHostPolicy:  UnknownHostDeny,
		Metrics: Metrics{
			Listen: ":9090",
			Path:   "/metrics",
		},
		UDPRelay: UDPRelay{
			Listen:             ":24454",
			Backend:            "127.0.0.1:24454",
			IdleTimeout:        Duration{Duration: 30 * time.Second},
			BackendDialTimeout: Duration{Duration: 5 * time.Second},
			MaxSessions:        4096,
			MaxPacketSize:      MaxUDPRelayPacketSize,
		},
		Bedrock: Bedrock{
			SessionTimeout: Duration{Duration: DefaultBedrockSessionTimeout},
		},
		VoiceChat: VoiceChat{
			Listen: ":24454",
			Registration: VoiceRegistration{
				Listen:           "127.0.0.1:9091",
				RegistrationTTL:  Duration{Duration: 30 * time.Second},
				RequestTimeout:   Duration{Duration: 5 * time.Second},
				MaxRegistrations: 4096,
			},
			Session: VoiceSession{
				IdleTimeout:        Duration{Duration: 30 * time.Second},
				BackendDialTimeout: Duration{Duration: 5 * time.Second},
				MaxSessions:        4096,
				MaxPacketSize:      MaxUDPRelayPacketSize,
			},
		},
		Discovery: Discovery{
			Kubernetes: KubernetesDiscovery{
				Mode:             KubernetesDiscoveryModeAnnotations,
				AnnotationPrefix: DefaultKubernetesAnnotationPrefix,
			},
		},
		Fallback: Fallback{
			Login: FallbackLogin{
				RespondOnRouteDenied: boolPtr(true),
				Message:              "Server unavailable. Please try again later.",
			},
			Status: FallbackStatus{
				RespondOnRouteDenied: boolPtr(true),
				MOTD:                 "Server unavailable",
				ProtocolName:         "mc-gateway",
				ProtocolVersion:      767,
			},
		},
		DefaultRoute: DefaultRoute{
			Mode: RouteModeAllow,
		},
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Listen) == "" {
		errs = append(errs, errors.New("listen must not be empty"))
	}
	if c.HandshakeTimeout.Duration <= 0 {
		errs = append(errs, errors.New("handshakeTimeout must be positive"))
	}
	if c.BackendDialTimeout.Duration <= 0 {
		errs = append(errs, errors.New("backendDialTimeout must be positive"))
	}
	if c.Metrics.Enabled {
		if strings.TrimSpace(c.Metrics.Listen) == "" {
			errs = append(errs, errors.New("metrics.listen must not be empty when metrics.enabled is true"))
		}
		if strings.TrimSpace(c.Metrics.Path) == "" {
			errs = append(errs, errors.New("metrics.path must not be empty when metrics.enabled is true"))
		}
		if !strings.HasPrefix(c.Metrics.Path, "/") {
			errs = append(errs, errors.New("metrics.path must start with /"))
		}
	}
	if err := validateUDPRelay(c.UDPRelay); err != nil {
		errs = append(errs, err)
	}
	if err := validateBedrock(c.Bedrock); err != nil {
		errs = append(errs, err)
	}
	if err := validateVoiceChat(&c.VoiceChat); err != nil {
		errs = append(errs, err)
	}
	if c.UDPRelay.Enabled && c.VoiceChat.Enabled && c.UDPRelay.Listen == c.VoiceChat.Listen {
		errs = append(errs, errors.New("udpRelay and voiceChat cannot use the same listen address"))
	}
	if c.Bedrock.Enabled && c.UDPRelay.Enabled && c.Bedrock.Listen == c.UDPRelay.Listen {
		errs = append(errs, errors.New("bedrock and udpRelay cannot use the same listen address"))
	}
	if c.Bedrock.Enabled && c.VoiceChat.Enabled && c.Bedrock.Listen == c.VoiceChat.Listen {
		errs = append(errs, errors.New("bedrock and voiceChat cannot use the same listen address"))
	}
	if err := validateKubernetesDiscovery(c.Discovery.Kubernetes); err != nil {
		errs = append(errs, err)
	}
	if c.Fallback.Enabled && c.Fallback.Status.Enabled {
		if strings.TrimSpace(c.Fallback.Status.MOTD) == "" {
			errs = append(errs, errors.New("fallback.status.motd must not be empty when fallback.enabled and fallback.status.enabled are true"))
		}
		if strings.TrimSpace(c.Fallback.Status.ProtocolName) == "" {
			errs = append(errs, errors.New("fallback.status.protocolName must not be empty when fallback.enabled and fallback.status.enabled are true"))
		}
		if c.Fallback.Status.ProtocolVersion < 0 {
			errs = append(errs, errors.New("fallback.status.protocolVersion must not be negative"))
		}
		if c.Fallback.Status.MaxPlayers < 0 {
			errs = append(errs, errors.New("fallback.status.maxPlayers must not be negative"))
		}
		if c.Fallback.Status.OnlinePlayers < 0 {
			errs = append(errs, errors.New("fallback.status.onlinePlayers must not be negative"))
		}
	}
	if c.Fallback.Enabled && c.Fallback.Login.Enabled && strings.TrimSpace(c.Fallback.Login.Message) == "" {
		errs = append(errs, errors.New("fallback.login.message must not be empty when fallback.enabled and fallback.login.enabled are true"))
	}
	switch c.UnknownHostPolicy {
	case UnknownHostDeny:
	case UnknownHostDefault:
		if strings.TrimSpace(c.DefaultRoute.Backend) == "" {
			errs = append(errs, errors.New("defaultRoute.backend is required when unknownHostPolicy is default"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknownHostPolicy must be %q or %q", UnknownHostDeny, UnknownHostDefault))
	}
	if c.DefaultRoute.Backend != "" {
		if err := validateBackend(c.DefaultRoute.Backend); err != nil {
			errs = append(errs, fmt.Errorf("defaultRoute.backend: %w", err))
		}
		if c.DefaultRoute.Mode == "" {
			errs = append(errs, errors.New("defaultRoute.mode must not be empty when defaultRoute.backend is set"))
		}
		if c.DefaultRoute.Mode != RouteModeAllow {
			errs = append(errs, fmt.Errorf("defaultRoute.mode only supports %q in the MVP", RouteModeAllow))
		}
	}
	seen := map[string]struct{}{}
	for i, route := range c.Routes {
		addr, err := hostaddr.Normalize(route.ServerAddress)
		if err != nil {
			errs = append(errs, fmt.Errorf("routes[%d].serverAddress: %w", i, err))
		}
		if _, ok := seen[addr]; ok {
			errs = append(errs, fmt.Errorf("routes[%d].serverAddress duplicates %q", i, addr))
		}
		seen[addr] = struct{}{}
		if err := validateBackend(route.Backend); err != nil {
			errs = append(errs, fmt.Errorf("routes[%d].backend: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

func validateVoiceChat(voice *VoiceChat) error {
	if !voice.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(voice.Listen) == "" {
		errs = append(errs, errors.New("voiceChat.listen must not be empty when voiceChat.enabled is true"))
	} else if _, err := net.ResolveUDPAddr("udp", voice.Listen); err != nil {
		errs = append(errs, fmt.Errorf("voiceChat.listen must be a valid UDP listen address: %w", err))
	}
	if strings.TrimSpace(voice.Registration.Listen) == "" {
		errs = append(errs, errors.New("voiceChat.registration.listen must not be empty when voiceChat.enabled is true"))
	} else if _, err := net.ResolveTCPAddr("tcp", voice.Registration.Listen); err != nil {
		errs = append(errs, fmt.Errorf("voiceChat.registration.listen must be a valid TCP listen address: %w", err))
	}
	if voice.Registration.RegistrationTTL.Duration <= 0 {
		errs = append(errs, errors.New("voiceChat.registration.registrationTTL must be positive when voiceChat.enabled is true"))
	}
	if voice.Registration.RegistrationTTL.Duration > MaxUDPRelayIdleTimeout {
		errs = append(errs, fmt.Errorf("voiceChat.registration.registrationTTL must be no greater than %s", MaxUDPRelayIdleTimeout))
	}
	if voice.Registration.RequestTimeout.Duration <= 0 {
		errs = append(errs, errors.New("voiceChat.registration.requestTimeout must be positive when voiceChat.enabled is true"))
	}
	if voice.Registration.MaxRegistrations <= 0 {
		errs = append(errs, errors.New("voiceChat.registration.maxRegistrations must be positive when voiceChat.enabled is true"))
	}
	if voice.Registration.MaxRegistrations > MaxVoiceChatRegistrations {
		errs = append(errs, fmt.Errorf("voiceChat.registration.maxRegistrations must be no greater than %d", MaxVoiceChatRegistrations))
	}
	if voice.Session.IdleTimeout.Duration <= 0 {
		errs = append(errs, errors.New("voiceChat.session.idleTimeout must be positive when voiceChat.enabled is true"))
	}
	if voice.Session.IdleTimeout.Duration > MaxUDPRelayIdleTimeout {
		errs = append(errs, fmt.Errorf("voiceChat.session.idleTimeout must be no greater than %s", MaxUDPRelayIdleTimeout))
	}
	if voice.Session.BackendDialTimeout.Duration <= 0 {
		errs = append(errs, errors.New("voiceChat.session.backendDialTimeout must be positive when voiceChat.enabled is true"))
	}
	if voice.Session.MaxSessions <= 0 {
		errs = append(errs, errors.New("voiceChat.session.maxSessions must be positive when voiceChat.enabled is true"))
	}
	if voice.Session.MaxSessions > MaxUDPRelaySessions {
		errs = append(errs, fmt.Errorf("voiceChat.session.maxSessions must be no greater than %d", MaxUDPRelaySessions))
	}
	if voice.Session.MaxPacketSize <= 0 {
		errs = append(errs, errors.New("voiceChat.session.maxPacketSize must be positive when voiceChat.enabled is true"))
	}
	if voice.Session.MaxPacketSize > MaxUDPRelayPacketSize {
		errs = append(errs, fmt.Errorf("voiceChat.session.maxPacketSize must be no greater than %d", MaxUDPRelayPacketSize))
	}
	if len(voice.Backends) == 0 {
		errs = append(errs, errors.New("voiceChat.backends must contain at least one backend when voiceChat.enabled is true"))
	}
	seenTokens := map[string]string{}
	for id, backend := range voice.Backends {
		if strings.TrimSpace(id) == "" {
			errs = append(errs, errors.New("voiceChat.backends contains an empty backend ID"))
		}
		if strings.ContainsAny(id, " \t\r\n/") {
			errs = append(errs, fmt.Errorf("voiceChat.backends[%q] backend ID must not contain whitespace or /", id))
		}
		if err := validateBackend(backend.UDP); err != nil {
			errs = append(errs, fmt.Errorf("voiceChat.backends[%q].udp: %w", id, err))
		} else if err := validateUDPBackendAddress(backend.UDP); err != nil {
			errs = append(errs, fmt.Errorf("voiceChat.backends[%q].udp: %w", id, err))
		}
		if strings.TrimSpace(backend.TokenEnv) == "" {
			errs = append(errs, fmt.Errorf("voiceChat.backends[%q].tokenEnv must not be empty", id))
			continue
		}
		token := os.Getenv(backend.TokenEnv)
		if strings.TrimSpace(token) == "" {
			errs = append(errs, fmt.Errorf("voiceChat.backends[%q].tokenEnv %q is not set or is empty", id, backend.TokenEnv))
			continue
		}
		if other, ok := seenTokens[token]; ok {
			errs = append(errs, fmt.Errorf("voiceChat.backends[%q].tokenEnv duplicates token for backend %q", id, other))
			continue
		}
		seenTokens[token] = id
		backend.token = token
		voice.Backends[id] = backend
	}
	return errors.Join(errs...)
}

func validateUDPRelay(relay UDPRelay) error {
	if !relay.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(relay.Listen) == "" {
		errs = append(errs, errors.New("udpRelay.listen must not be empty when udpRelay.enabled is true"))
	} else if _, err := net.ResolveUDPAddr("udp", relay.Listen); err != nil {
		errs = append(errs, fmt.Errorf("udpRelay.listen must be a valid UDP listen address: %w", err))
	}
	if err := validateBackend(relay.Backend); err != nil {
		errs = append(errs, fmt.Errorf("udpRelay.backend: %w", err))
	} else if err := validateUDPBackendAddress(relay.Backend); err != nil {
		errs = append(errs, fmt.Errorf("udpRelay.backend: %w", err))
	}
	if relay.IdleTimeout.Duration <= 0 {
		errs = append(errs, errors.New("udpRelay.idleTimeout must be positive when udpRelay.enabled is true"))
	}
	if relay.IdleTimeout.Duration > MaxUDPRelayIdleTimeout {
		errs = append(errs, fmt.Errorf("udpRelay.idleTimeout must be no greater than %s", MaxUDPRelayIdleTimeout))
	}
	if relay.BackendDialTimeout.Duration <= 0 {
		errs = append(errs, errors.New("udpRelay.backendDialTimeout must be positive when udpRelay.enabled is true"))
	}
	if relay.MaxSessions <= 0 {
		errs = append(errs, errors.New("udpRelay.maxSessions must be positive when udpRelay.enabled is true"))
	}
	if relay.MaxSessions > MaxUDPRelaySessions {
		errs = append(errs, fmt.Errorf("udpRelay.maxSessions must be no greater than %d", MaxUDPRelaySessions))
	}
	if relay.MaxPacketSize <= 0 {
		errs = append(errs, errors.New("udpRelay.maxPacketSize must be positive when udpRelay.enabled is true"))
	}
	if relay.MaxPacketSize > MaxUDPRelayPacketSize {
		errs = append(errs, fmt.Errorf("udpRelay.maxPacketSize must be no greater than %d", MaxUDPRelayPacketSize))
	}
	return errors.Join(errs...)
}

func validateBedrock(bedrock Bedrock) error {
	if !bedrock.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(bedrock.Listen) == "" {
		errs = append(errs, errors.New("bedrock.listen must not be empty when bedrock.enabled is true"))
	} else if _, err := net.ResolveUDPAddr("udp", bedrock.Listen); err != nil {
		errs = append(errs, fmt.Errorf("bedrock.listen must be a valid UDP listen address: %w", err))
	}
	if err := validateBackend(bedrock.DefaultBackend); err != nil {
		errs = append(errs, fmt.Errorf("bedrock.defaultBackend: %w", err))
	} else if err := validateUDPBackendAddress(bedrock.DefaultBackend); err != nil {
		errs = append(errs, fmt.Errorf("bedrock.defaultBackend: %w", err))
	}
	if bedrock.SessionTimeout.Duration <= 0 {
		errs = append(errs, errors.New("bedrock.sessionTimeout must be positive when bedrock.enabled is true"))
	}
	if bedrock.SessionTimeout.Duration > MaxUDPRelayIdleTimeout {
		errs = append(errs, fmt.Errorf("bedrock.sessionTimeout must be no greater than %s", MaxUDPRelayIdleTimeout))
	}
	return errors.Join(errs...)
}

func validateUDPBackendAddress(backend string) error {
	host, _, err := net.SplitHostPort(backend)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsUnspecified() {
		return errors.New("host must not be an unspecified address")
	}
	if ip.IsMulticast() {
		return errors.New("host must not be a multicast address")
	}
	if ip4 := ip.To4(); ip4 != nil && ip4.Equal(net.IPv4bcast) {
		return errors.New("host must not be the IPv4 broadcast address")
	}
	return nil
}

func validateKubernetesDiscovery(k KubernetesDiscovery) error {
	if !k.Enabled && k.Namespace == "" && k.Mode == "" && k.AnnotationPrefix == "" {
		return nil
	}
	var errs []error
	if k.Mode != KubernetesDiscoveryModeAnnotations {
		errs = append(errs, fmt.Errorf("discovery.kubernetes.mode only supports %q", KubernetesDiscoveryModeAnnotations))
	}
	if err := validateAnnotationPrefix(k.AnnotationPrefix); err != nil {
		errs = append(errs, fmt.Errorf("discovery.kubernetes.annotationPrefix: %w", err))
	}
	if namespace := strings.TrimSpace(k.Namespace); namespace != "" {
		if namespace != k.Namespace {
			errs = append(errs, errors.New("discovery.kubernetes.namespace must not contain leading or trailing whitespace"))
		}
		if !isDNSLabel(namespace) {
			errs = append(errs, errors.New("discovery.kubernetes.namespace must be empty or a DNS label"))
		}
	}
	return errors.Join(errs...)
}

func validateAnnotationPrefix(prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return errors.New("must not be empty")
	}
	if strings.TrimSpace(prefix) != prefix {
		return errors.New("must not contain leading or trailing whitespace")
	}
	if strings.Contains(prefix, "/") {
		return errors.New("must not contain /")
	}
	normalized, err := hostaddr.Normalize(prefix)
	if err != nil {
		return err
	}
	if normalized != prefix {
		return errors.New("must be a canonical lowercase DNS subdomain")
	}
	return nil
}

func isDNSLabel(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		valid := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !valid {
			return false
		}
	}
	return value[0] != '-' && value[len(value)-1] != '-'
}

func validateBackend(backend string) error {
	host, port, err := net.SplitHostPort(backend)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("host must not be empty")
	}
	if strings.TrimSpace(port) == "" {
		return errors.New("port must not be empty")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return fmt.Errorf("port must be an integer from 1 to 65535")
	}
	if _, err := hostaddr.Normalize(host); err != nil {
		return fmt.Errorf("host: %w", err)
	}
	return nil
}
