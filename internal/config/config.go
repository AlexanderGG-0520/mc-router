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
	UnknownHostDeny    = "deny"
	UnknownHostDefault = "default"
	RouteModeAllow     = "allow"
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
	DefaultRoute       DefaultRoute `yaml:"defaultRoute"`
	Routes             []Route      `yaml:"routes"`
	UnknownHostPolicy  string       `yaml:"unknownHostPolicy"`
}

type Metrics struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
	Path    string `yaml:"path"`
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
		DefaultRoute: DefaultRoute{
			Mode: RouteModeAllow,
		},
	}
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
