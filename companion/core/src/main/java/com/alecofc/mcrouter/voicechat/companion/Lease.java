package com.alecofc.mcrouter.voicechat.companion;

import java.time.Instant;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class Lease {
    private static final Pattern LEASE_ID = Pattern.compile("\"leaseId\"\\s*:\\s*\"([^\"]+)\"");
    private static final Pattern EXPIRES_AT = Pattern.compile("\"expiresAt\"\\s*:\\s*\"([^\"]+)\"");

    private final UUID playerUuid;
    private final String leaseId;
    private final Instant expiresAt;

    Lease(UUID playerUuid, String leaseId, Instant expiresAt) {
        this.playerUuid = playerUuid;
        this.leaseId = leaseId;
        this.expiresAt = expiresAt;
    }

    static Lease fromJson(UUID playerUuid, String json) {
        String leaseId = require(LEASE_ID.matcher(json), "leaseId");
        String expiresAt = require(EXPIRES_AT.matcher(json), "expiresAt");
        return new Lease(playerUuid, leaseId, Instant.parse(expiresAt));
    }

    public UUID playerUuid() {
        return playerUuid;
    }

    public String leaseId() {
        return leaseId;
    }

    public Instant expiresAt() {
        return expiresAt;
    }

    private static String require(Matcher matcher, String field) {
        if (!matcher.find()) {
            throw new IllegalArgumentException("registration response missing " + field);
        }
        return matcher.group(1);
    }
}
