package com.alecofc.mcrouter.voicechat.companion;

import java.net.URI;
import java.time.Duration;
import java.util.Objects;
import java.util.UUID;

public final class CompanionConfig {
    private final URI registrationUrl;
    private final String backendId;
    private final String token;
    private final String instanceId;
    private final Duration registrationTtl;
    private final Duration refreshInterval;
    private final Duration requestTimeout;
    private final int maxTrackedPlayers;

    public CompanionConfig(
            URI registrationUrl,
            String backendId,
            String token,
            String instanceId,
            Duration registrationTtl,
            Duration refreshInterval,
            Duration requestTimeout,
            int maxTrackedPlayers
    ) {
        this.registrationUrl = Objects.requireNonNull(registrationUrl, "registrationUrl");
        this.backendId = requireText(backendId, "backendId");
        this.token = requireText(token, "token");
        this.instanceId = instanceId == null || instanceId.isBlank() ? UUID.randomUUID().toString() : instanceId;
        this.registrationTtl = requirePositive(registrationTtl, "registrationTtl");
        this.refreshInterval = requirePositive(refreshInterval, "refreshInterval");
        this.requestTimeout = requirePositive(requestTimeout, "requestTimeout");
        if (maxTrackedPlayers <= 0) {
            throw new IllegalArgumentException("maxTrackedPlayers must be positive");
        }
        this.maxTrackedPlayers = maxTrackedPlayers;
    }

    public static CompanionConfig fromEnvironment() {
        Duration ttl = durationEnv("MC_ROUTER_VOICECHAT_TTL", Duration.ofSeconds(30));
        return new CompanionConfig(
                URI.create(requireEnv("MC_ROUTER_VOICECHAT_REGISTRATION_URL")),
                requireEnv("MC_ROUTER_VOICECHAT_BACKEND_ID"),
                requireEnv("MC_ROUTER_VOICECHAT_TOKEN"),
                env("MC_ROUTER_VOICECHAT_INSTANCE_ID", UUID.randomUUID().toString()),
                ttl,
                durationEnv("MC_ROUTER_VOICECHAT_REFRESH_INTERVAL", ttl.dividedBy(2)),
                durationEnv("MC_ROUTER_VOICECHAT_REQUEST_TIMEOUT", Duration.ofSeconds(5)),
                intEnv("MC_ROUTER_VOICECHAT_MAX_TRACKED_PLAYERS", 4096)
        );
    }

    public URI registrationUrl() {
        return registrationUrl;
    }

    public String backendId() {
        return backendId;
    }

    public String token() {
        return token;
    }

    public String instanceId() {
        return instanceId;
    }

    public Duration registrationTtl() {
        return registrationTtl;
    }

    public Duration refreshInterval() {
        return refreshInterval;
    }

    public Duration requestTimeout() {
        return requestTimeout;
    }

    public int maxTrackedPlayers() {
        return maxTrackedPlayers;
    }

    private static String requireEnv(String name) {
        return requireText(System.getenv(name), name);
    }

    private static String env(String name, String fallback) {
        String value = System.getenv(name);
        return value == null || value.isBlank() ? fallback : value;
    }

    private static int intEnv(String name, int fallback) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            return fallback;
        }
        return Integer.parseInt(value);
    }

    private static Duration durationEnv(String name, Duration fallback) {
        String value = System.getenv(name);
        if (value == null || value.isBlank()) {
            return fallback;
        }
        return Duration.parse(value);
    }

    private static String requireText(String value, String name) {
        if (value == null || value.isBlank()) {
            throw new IllegalArgumentException(name + " must not be empty");
        }
        return value;
    }

    private static Duration requirePositive(Duration value, String name) {
        Objects.requireNonNull(value, name);
        if (value.isZero() || value.isNegative()) {
            throw new IllegalArgumentException(name + " must be positive");
        }
        return value;
    }
}
