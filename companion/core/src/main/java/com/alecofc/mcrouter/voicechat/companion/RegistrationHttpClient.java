package com.alecofc.mcrouter.voicechat.companion;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.UUID;

public final class RegistrationHttpClient {
    private final HttpClient httpClient;
    private final CompanionConfig config;

    public RegistrationHttpClient(CompanionConfig config) {
        this(HttpClient.newBuilder().connectTimeout(config.requestTimeout()).build(), config);
    }

    RegistrationHttpClient(HttpClient httpClient, CompanionConfig config) {
        this.httpClient = httpClient;
        this.config = config;
    }

    Lease register(UUID playerUuid) throws IOException, InterruptedException {
        String body = "{\"ownerId\":\"" + json(config.instanceId()) + "\"}";
        HttpResponse<String> response = send(
                request(playerUuid, "")
                        .PUT(HttpRequest.BodyPublishers.ofString(body))
                        .build()
        );
        if (response.statusCode() != 200) {
            throw new IOException("registration failed with status " + response.statusCode());
        }
        return Lease.fromJson(playerUuid, response.body());
    }

    Lease refresh(UUID playerUuid, Lease lease) throws IOException, InterruptedException {
        String body = leaseBody(lease);
        HttpResponse<String> response = send(
                request(playerUuid, "/refresh")
                        .POST(HttpRequest.BodyPublishers.ofString(body))
                        .build()
        );
        if (response.statusCode() != 200) {
            throw new IOException("refresh failed with status " + response.statusCode());
        }
        return Lease.fromJson(playerUuid, response.body());
    }

    void unregister(UUID playerUuid, Lease lease) throws IOException, InterruptedException {
        HttpResponse<String> response = send(
                request(playerUuid, "")
                        .method("DELETE", HttpRequest.BodyPublishers.ofString(leaseBody(lease)))
                        .build()
        );
        if (response.statusCode() != 204) {
            throw new IOException("unregister failed with status " + response.statusCode());
        }
    }

    private HttpRequest.Builder request(UUID playerUuid, String suffix) {
        URI uri = config.registrationUrl().resolve("/v1/voicechat/registrations/" + playerUuid + suffix);
        return HttpRequest.newBuilder(uri)
                .timeout(config.requestTimeout())
                .header("Authorization", "Bearer " + config.token())
                .header("Content-Type", "application/json");
    }

    private HttpResponse<String> send(HttpRequest request) throws IOException, InterruptedException {
        return httpClient.send(request, HttpResponse.BodyHandlers.ofString());
    }

    private String leaseBody(Lease lease) {
        return "{\"ownerId\":\"" + json(config.instanceId()) + "\",\"leaseId\":\"" + json(lease.leaseId()) + "\"}";
    }

    private static String json(String value) {
        return value.replace("\\", "\\\\").replace("\"", "\\\"");
    }
}
