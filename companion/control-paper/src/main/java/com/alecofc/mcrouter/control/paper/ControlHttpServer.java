package com.alecofc.mcrouter.control.paper;

import java.io.BufferedInputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.function.BiConsumer;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

final class ControlHttpServer implements AutoCloseable {
    private static final Pattern AVAILABILITY = Pattern.compile("\\\"availability\\\"\\s*:\\s*\\\"(online|offline)\\\"");
    private final ControlConfig config;
    private final BiConsumer<String, BackendAvailability> updates;
    private final ExecutorService workers = Executors.newFixedThreadPool(2);
    private ServerSocket listener;

    ControlHttpServer(ControlConfig config, BiConsumer<String, BackendAvailability> updates) {
        this.config = config;
        this.updates = updates;
    }

    void start() throws IOException {
        listener = new ServerSocket();
        listener.bind(config.listen());
        Thread acceptor = new Thread(this::acceptLoop, "mc-router-control-accept");
        acceptor.setDaemon(true);
        acceptor.start();
    }

    private void acceptLoop() {
        while (!listener.isClosed()) {
            try {
                Socket socket = listener.accept();
                workers.execute(() -> handle(socket));
            } catch (IOException ignored) {
                // Closing the listener interrupts accept; transient failures may recover.
            }
        }
    }

    private void handle(Socket socket) {
        try (socket; BufferedInputStream input = new BufferedInputStream(socket.getInputStream()); OutputStream output = socket.getOutputStream()) {
            socket.setSoTimeout(5_000);
            String[] request = readLine(input).split(" ", 3);
            Map<String, String> headers = readHeaders(input);
            String backendId = request.length == 3 ? backendId(request[1]) : null;
            if (!"PUT".equals(request[0]) || backendId == null || !config.backendIds().contains(backendId)) { reply(output, 404); return; }
            if (!authorized(headers.getOrDefault("authorization", ""), config.token())) { reply(output, 401); return; }
            int length = contentLength(headers);
            if (length < 0 || length > config.maxBodyBytes()) { reply(output, 400); return; }
            byte[] body = input.readNBytes(length);
            Matcher matcher = AVAILABILITY.matcher(new String(body, StandardCharsets.UTF_8));
            if (body.length != length || !matcher.find()) { reply(output, 400); return; }
            updates.accept(backendId, BackendAvailability.parse(matcher.group(1)));
            reply(output, 204);
        } catch (IllegalArgumentException | IOException ignored) {
            // Malformed requests never reach the Paper server thread.
        }
    }

    private static Map<String, String> readHeaders(BufferedInputStream input) throws IOException {
        Map<String, String> result = new HashMap<>();
        for (;;) {
            String line = readLine(input);
            if (line.isEmpty()) return result;
            int separator = line.indexOf(':');
            if (separator <= 0) throw new IllegalArgumentException("malformed HTTP header");
            result.put(line.substring(0, separator).toLowerCase(), line.substring(separator + 1).trim());
        }
    }

    private static String readLine(BufferedInputStream input) throws IOException {
        StringBuilder line = new StringBuilder();
        while (line.length() <= 8192) {
            int next = input.read();
            if (next == -1) throw new IOException("unexpected end of HTTP request");
            if (next == '\n') return line.toString().replace("\r", "");
            line.append((char) next);
        }
        throw new IllegalArgumentException("HTTP line too long");
    }

    private static String backendId(String path) {
        String[] parts = path.split("\\?", 2)[0].split("/");
        return parts.length == 5 && "v1".equals(parts[1]) && "backends".equals(parts[2]) && "availability".equals(parts[4]) ? parts[3] : null;
    }

    private static boolean authorized(String value, String token) {
        return MessageDigest.isEqual(("Bearer " + token).getBytes(StandardCharsets.UTF_8), value.getBytes(StandardCharsets.UTF_8));
    }

    private static int contentLength(Map<String, String> headers) {
        try { return Integer.parseInt(headers.getOrDefault("content-length", "-1")); }
        catch (NumberFormatException exception) { return -1; }
    }

    private static void reply(OutputStream output, int status) throws IOException {
        output.write(("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
        output.flush();
    }

    @Override
    public void close() throws IOException {
        if (listener != null) listener.close();
        workers.shutdownNow();
    }
}
