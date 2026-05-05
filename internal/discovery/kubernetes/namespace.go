package kubernetes

import (
	"fmt"
	"os"
	"strings"
)

const DefaultNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

type NamespaceResolver struct {
	Path     string
	ReadFile func(string) ([]byte, error)
}

func ResolveNamespace(configured string, resolver NamespaceResolver) (string, error) {
	if configured != "" {
		if err := validateNamespace(configured); err != nil {
			return "", fmt.Errorf("configured namespace: %w", err)
		}
		return configured, nil
	}

	path := resolver.path()
	data, err := resolver.readFile(path)
	if err != nil {
		return "", fmt.Errorf("read current namespace file %q: %w", path, err)
	}

	namespace := strings.TrimSpace(string(data))
	if namespace == "" {
		return "", fmt.Errorf("current namespace file %q is empty", path)
	}
	if err := validateNamespace(namespace); err != nil {
		return "", fmt.Errorf("current namespace file %q: %w", path, err)
	}
	return namespace, nil
}

func (r NamespaceResolver) path() string {
	if r.Path == "" {
		return DefaultNamespacePath
	}
	return r.Path
}

func (r NamespaceResolver) readFile(path string) ([]byte, error) {
	if r.ReadFile != nil {
		return r.ReadFile(path)
	}
	return os.ReadFile(path)
}

func validateNamespace(namespace string) error {
	if strings.TrimSpace(namespace) != namespace {
		return fmt.Errorf("namespace must not contain leading or trailing whitespace")
	}
	if !isDNSLabel(namespace) {
		return fmt.Errorf("namespace must be a DNS label")
	}
	return nil
}
