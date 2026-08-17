package com.alecofc.mcrouter.control.paper;

import java.net.InetSocketAddress;
import java.util.Arrays;
import java.util.Set;
import java.util.stream.Collectors;

final class ControlConfig {
    private final InetSocketAddress listen;
    private final String token;
    private final Set<String> backendIds;
    private final int maxBodyBytes;

    private ControlConfig(InetSocketAddress listen, String token, Set<String> backendIds, int maxBodyBytes) {
        this.listen = listen;
        this.token = token;
        this.backendIds = backendIds;
        this.maxBodyBytes = maxBodyBytes;
    }

    static ControlConfig fromEnvironment() {
        String backendIds = required("MC_ROUTER_CONTROL_BACKENDS");
        Set<String> allowedBackends = Arrays.stream(backendIds.split(","))
                .map(String::trim)
                .filter(value -> !value.isEmpty())
                .peek(ControlConfig::validateBackendId)
                .collect(Collectors.toUnmodifiableSet());
        if (allowedBackends.isEmpty()) {
            throw new IllegalArgumentException("MC_ROUTER_CONTROL_BACKENDS must contain at least one backend ID");
        }
        return new ControlConfig(
                parseListen(required("MC_ROUTER_CONTROL_LISTEN")),
                required("MC_ROUTER_CONTROL_TOKEN"),
                allowedBackends,
                positiveInt("MC_ROUTER_CONTROL_MAX_BODY_BYTES", 8192)
        );
    }

    static InetSocketAddress parseListen(String value) {
        String trimmed = value.trim();
        int separator = trimmed.lastIndexOf(':');
        if (separator < 0 || separator == trimmed.length() - 1) {
            throw new IllegalArgumentException("MC_ROUTER_CONTROL_LISTEN must be host:port or :port");
        }
        int port = Integer.parseInt(trimmed.substring(separator + 1));
        if (port < 1 || port > 65535) {
            throw new IllegalArgumentException("MC_ROUTER_CONTROL_LISTEN port must be between 1 and 65535");
        }
        String host = trimmed.substring(0, separator);
        return host.isEmpty() ? new InetSocketAddress(port) : new InetSocketAddress(host, port);
    }

    InetSocketAddress listen() { return listen; }
    String token() { return token; }
    Set<String> backendIds() { return backendIds; }
    int maxBodyBytes() { return maxBodyBytes; }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) throw new IllegalArgumentException(name + " must not be empty");
        return value;
    }

    private static int positiveInt(String name, int fallback) {
        String value = System.getenv(name);
        int result = value == null || value.isBlank() ? fallback : Integer.parseInt(value);
        if (result <= 0) throw new IllegalArgumentException(name + " must be positive");
        return result;
    }

    private static void validateBackendId(String value) {
        if (!value.matches("[a-z][a-z0-9-]{0,62}")) {
            throw new IllegalArgumentException("invalid backend ID: " + value);
        }
    }
}
