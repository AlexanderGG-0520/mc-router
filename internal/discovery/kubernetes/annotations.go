package kubernetes

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
)

const (
	ReasonDisabled           = "disabled"
	ReasonInvalidPrefix      = "invalid_annotation_prefix"
	ReasonInvalidServiceName = "invalid_service_name"
	ReasonInvalidNamespace   = "invalid_namespace"
	ReasonMissingHost        = "missing_host"
	ReasonInvalidHost        = "invalid_host"
	ReasonMissingPort        = "missing_port"
	ReasonInvalidPort        = "invalid_port"
	ReasonPortNotFound       = "port_not_found"
	ReasonDuplicateHost      = "duplicate_host"
	DefaultAnnotationPrefix  = "mc-router.alexandergg.com"
	AnnotationEnabled        = "enabled"
	AnnotationHost           = "host"
	AnnotationPort           = "port"
)

type ServiceInput struct {
	Name        string
	Namespace   string
	Annotations map[string]string
	Ports       []ServicePort
}

type ServicePort struct {
	Name string
	Port int
}

type DiscoveredRoute struct {
	Host    string
	Backend string
}

type ParseResult struct {
	Route   DiscoveredRoute
	Skipped bool
	Reason  string
	Err     error
}

type SkippedRoute struct {
	Host    string
	Backend string
	Reason  string
	Err     error
}

func ParseServiceAnnotations(prefix string, service ServiceInput) ParseResult {
	if err := validateAnnotationPrefix(prefix); err != nil {
		return skip(ReasonInvalidPrefix, err)
	}

	if service.Annotations[prefix+"/"+AnnotationEnabled] != "true" {
		return skip(ReasonDisabled, nil)
	}
	if !isDNSLabel(service.Name) {
		return skip(ReasonInvalidServiceName, fmt.Errorf("service name %q must be a DNS label", service.Name))
	}
	if !isDNSLabel(service.Namespace) {
		return skip(ReasonInvalidNamespace, fmt.Errorf("namespace %q must be a DNS label", service.Namespace))
	}

	hostValue, ok := service.Annotations[prefix+"/"+AnnotationHost]
	if !ok || strings.TrimSpace(hostValue) == "" {
		return skip(ReasonMissingHost, errors.New("host annotation is required"))
	}
	host, err := hostaddr.Normalize(hostValue)
	if err != nil {
		return skip(ReasonInvalidHost, err)
	}

	portValue, ok := service.Annotations[prefix+"/"+AnnotationPort]
	if !ok || strings.TrimSpace(portValue) == "" {
		return skip(ReasonMissingPort, errors.New("port annotation is required"))
	}
	port, err := strconv.Atoi(strings.TrimSpace(portValue))
	if err != nil || port < 1 || port > 65535 {
		return skip(ReasonInvalidPort, errors.New("port annotation must be an integer from 1 to 65535"))
	}
	if !serviceHasPort(service.Ports, port) {
		return skip(ReasonPortNotFound, fmt.Errorf("port %d is not present in service ports", port))
	}

	return ParseResult{
		Route: DiscoveredRoute{
			Host:    host,
			Backend: net.JoinHostPort(service.Name+"."+service.Namespace+".svc.cluster.local", strconv.Itoa(port)),
		},
	}
}

func DropDuplicateHosts(routes []DiscoveredRoute) ([]DiscoveredRoute, []SkippedRoute) {
	counts := make(map[string]int, len(routes))
	normalized := make([]DiscoveredRoute, 0, len(routes))
	skipped := make([]SkippedRoute, 0)

	for _, route := range routes {
		host, err := hostaddr.Normalize(route.Host)
		if err != nil {
			skipped = append(skipped, SkippedRoute{
				Host:    route.Host,
				Backend: route.Backend,
				Reason:  ReasonInvalidHost,
				Err:     err,
			})
			continue
		}
		route.Host = host
		normalized = append(normalized, route)
		counts[host]++
	}

	kept := make([]DiscoveredRoute, 0, len(normalized))
	for _, route := range normalized {
		if counts[route.Host] > 1 {
			skipped = append(skipped, SkippedRoute{
				Host:    route.Host,
				Backend: route.Backend,
				Reason:  ReasonDuplicateHost,
				Err:     fmt.Errorf("host %q is discovered more than once", route.Host),
			})
			continue
		}
		kept = append(kept, route)
	}
	return kept, skipped
}

func serviceHasPort(ports []ServicePort, port int) bool {
	for _, servicePort := range ports {
		if servicePort.Port == port {
			return true
		}
	}
	return false
}

func skip(reason string, err error) ParseResult {
	return ParseResult{
		Skipped: true,
		Reason:  reason,
		Err:     err,
	}
}

func validateAnnotationPrefix(prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return errors.New("annotation prefix must not be empty")
	}
	if strings.TrimSpace(prefix) != prefix {
		return errors.New("annotation prefix must not contain leading or trailing whitespace")
	}
	if strings.Contains(prefix, "/") {
		return errors.New("annotation prefix must not contain /")
	}
	if _, err := hostaddr.Normalize(prefix); err != nil {
		return err
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
