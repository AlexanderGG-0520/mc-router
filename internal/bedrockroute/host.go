package bedrockroute

import (
	"fmt"
	"net"
	"strings"

	"github.com/AlexanderGG-0520/mc-router/internal/hostaddr"
)

func NormalizeHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("must not be empty")
	}

	host := value
	if splitHost, _, err := net.SplitHostPort(value); err == nil {
		host = splitHost
	} else if strings.Count(value, ":") == 1 {
		before, after, ok := strings.Cut(value, ":")
		if ok && before != "" && after != "" {
			host = before
		}
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("host must not be empty")
	}

	normalized, err := hostaddr.Normalize(host)
	if err != nil {
		return "", err
	}
	return normalized, nil
}
