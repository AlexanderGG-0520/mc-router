package com.alecofc.mcrouter.voicechat.companion;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class VoiceChatRegistrationManagerTest {
    private final List<String> requests = new ArrayList<>();
    private HttpServer server;

    @AfterEach
    void stopServer() {
        if (server != null) {
            server.stop(0);
        }
    }

    @Test
    void registerRefreshAndUnregister() throws Exception {
        CompanionConfig config = config(startServer(200, 200, 204));
        try (VoiceChatRegistrationManager manager = new VoiceChatRegistrationManager(config)) {
            UUID playerUuid = UUID.fromString("00112233-4455-6677-8899-aabbccddeeff");

            manager.registerBeforeUdp(playerUuid);
            assertEquals(1, manager.trackedPlayers());

            Thread.sleep(250);
            manager.unregister(playerUuid);
            assertEquals(0, manager.trackedPlayers());
        }

        waitForRequest("DELETE /v1/voicechat/registrations/");
        assertTrue(requests.stream().anyMatch(r -> r.startsWith("PUT /v1/voicechat/registrations/")));
        assertTrue(requests.stream().anyMatch(r -> r.startsWith("POST /v1/voicechat/registrations/")));
        assertTrue(requests.stream().anyMatch(r -> r.startsWith("DELETE /v1/voicechat/registrations/")));
        assertTrue(requests.stream().allMatch(r -> r.contains("auth=true")));
    }

    @Test
    void boundedTrackingRejectsNewPlayers() throws Exception {
        CompanionConfig config = new CompanionConfig(
                startServer(200),
                "hub",
                "token",
                "instance-a",
                Duration.ofSeconds(30),
                Duration.ofSeconds(15),
                Duration.ofSeconds(1),
                1
        );
        try (VoiceChatRegistrationManager manager = new VoiceChatRegistrationManager(config)) {
            manager.registerBeforeUdp(UUID.fromString("00112233-4455-6677-8899-aabbccddeeff"));
            IOException error = null;
            try {
                manager.registerBeforeUdp(UUID.fromString("11112233-4455-6677-8899-aabbccddeeff"));
            } catch (IOException e) {
                error = e;
            }
            assertTrue(error != null && error.getMessage().contains("tracked player limit"));
        }
    }

    private CompanionConfig config(URI uri) {
        return new CompanionConfig(
                uri,
                "hub",
                "token",
                "instance-a",
                Duration.ofMillis(400),
                Duration.ofMillis(100),
                Duration.ofSeconds(1),
                8
        );
    }

    private URI startServer(int... statuses) throws IOException {
        server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> handle(exchange, statuses));
        server.start();
        return URI.create("http://" + server.getAddress().getHostString() + ":" + server.getAddress().getPort());
    }

    private void waitForRequest(String prefix) throws InterruptedException {
        long deadline = System.currentTimeMillis() + 2000;
        while (System.currentTimeMillis() < deadline) {
            if (requests.stream().anyMatch(r -> r.startsWith(prefix))) {
                return;
            }
            Thread.sleep(10);
        }
    }

    private void handle(HttpExchange exchange, int[] statuses) throws IOException {
        boolean auth = "Bearer token".equals(exchange.getRequestHeaders().getFirst("Authorization"));
        requests.add(exchange.getRequestMethod() + " " + exchange.getRequestURI().getPath() + " auth=" + auth);
        int index = Math.min(requests.size() - 1, statuses.length - 1);
        int status = statuses[index];
        String body = "";
        if (status == 200) {
            body = "{\"backendId\":\"hub\",\"leaseId\":\"lease-" + requests.size() + "\",\"expiresAt\":\"" + Instant.now().plusSeconds(30) + "\"}";
        }
        byte[] data = body.getBytes(StandardCharsets.UTF_8);
        exchange.sendResponseHeaders(status, data.length);
        exchange.getResponseBody().write(data);
        exchange.close();
    }
}
